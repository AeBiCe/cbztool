// Package command implements cb's CLI command handlers. Each exported function
// (List, Validate, Info, Strip, Tag, Pack) parses its own flags and operates on
// a single .cbz file or, where it makes sense, a directory. The handlers
// orchestrate the archive, comicinfo, filename, and provider packages.
package command

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"cb/internal/archive"
	"cb/internal/comicinfo"
)

// bidiOverrides are Unicode formatting characters that can reorder how text is
// displayed (e.g. an RTL override making "gpj.exe" render as "exe.jpg"). They
// are neutralized on output alongside control characters.
var bidiOverrides = map[rune]bool{
	0x202A: true, 0x202B: true, 0x202C: true, 0x202D: true, 0x202E: true,
	0x2066: true, 0x2067: true, 0x2068: true, 0x2069: true,
	0x200E: true, 0x200F: true, 0x061C: true,
}

// sanitize makes an untrusted string safe to print to a terminal: every control
// character (including ESC, the basis of ANSI escape sequences) and every
// bidirectional override is replaced with U+FFFD. Apply it to any string that
// originates from archive contents or a provider before printing — ZIP entry
// names, ComicInfo field values, provider-supplied series names, etc.
func sanitize(s string) string {
	hasUnsafe := false
	for _, r := range s {
		if unicode.IsControl(r) || bidiOverrides[r] {
			hasUnsafe = true
			break
		}
	}
	if !hasUnsafe {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsControl(r) || bidiOverrides[r] {
			b.WriteRune('�')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cbzFiles resolves a path argument to the set of .cbz files it addresses:
// the file itself, or every .cbz directly inside a directory (non-recursive).
func cbzFiles(p string) ([]string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if !strings.EqualFold(filepath.Ext(p), ".cbz") {
			return nil, fmt.Errorf("not a .cbz file: %s", p)
		}
		return []string{p}, nil
	}

	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".cbz") {
			out = append(out, filepath.Join(p, e.Name()))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no .cbz files in %s", p)
	}
	sort.Strings(out)
	return out, nil
}

// writeComicInfo marshals ci and writes it into the archive, replacing any
// existing ComicInfo.xml, via the shared atomic rewrite.
func writeComicInfo(path string, ci *comicinfo.ComicInfo) error {
	data, err := ci.Marshal()
	if err != nil {
		return err
	}
	keep := func(name string) bool { return !strings.EqualFold(name, archive.ComicInfoName) }
	if err := archive.RewriteArchive(path, keep, map[string][]byte{archive.ComicInfoName: data}); err != nil {
		return err
	}
	fmt.Printf("%s: wrote ComicInfo.xml\n", path)
	return nil
}

// cleanCSV trims each comma-separated token and drops empties, rejoining with
// ", " so the comma-delimited ComicInfo fields are stored consistently.
func cleanCSV(s string) string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ", ")
}

// prompter reads single-line answers from stdin.
type prompter struct{ r *bufio.Reader }

func newPrompter() *prompter { return &prompter{r: bufio.NewReader(os.Stdin)} }

// maxLineBytes bounds a single prompt answer. Beyond this the extra input is
// discarded rather than buffered, so piping a huge line at the prompt can't
// exhaust memory (local robustness only).
const maxLineBytes = 64 << 10

// readLine reads one line from stdin, keeping at most maxLineBytes in memory and
// draining any overflow up to the newline. EOF ends the line without an error.
func (p *prompter) readLine() (string, error) {
	var b strings.Builder
	for {
		c, err := p.r.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		if c == '\n' {
			break
		}
		if b.Len() < maxLineBytes {
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

// ask prints "label: " and returns the trimmed line. EOF yields an empty answer.
func (p *prompter) ask(label string) (string, error) {
	fmt.Printf("%s: ", label)
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// askDefault prompts with a default value shown in brackets; empty input keeps
// the default. Used to pre-fill fields parsed from the filename.
func (p *prompter) askDefault(label, def string) (string, error) {
	prompt := label
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]", label, def)
	}
	v, err := p.ask(prompt)
	if err != nil {
		return "", err
	}
	if v == "" {
		return def, nil
	}
	return v, nil
}

// askAgeRating shows the enum as a numbered menu. Empty input or choice 0
// (Unknown) leaves AgeRating unset; any other valid index returns its value.
func (p *prompter) askAgeRating() (string, error) {
	fmt.Println("AgeRating (Enter or 0 to leave unset):")
	for i, v := range comicinfo.AgeRatings {
		fmt.Printf("  %2d) %s\n", i, v)
	}
	for {
		s, err := p.ask("  choice")
		if err != nil {
			return "", err
		}
		if s == "" {
			return "", nil
		}
		n, cerr := strconv.Atoi(s)
		if cerr == nil && n >= 0 && n < len(comicinfo.AgeRatings) {
			if n == 0 {
				return "", nil // Unknown -> omit
			}
			return comicinfo.AgeRatings[n], nil
		}
		fmt.Println("  invalid choice")
	}
}

// askIndex reads a menu choice in [0, n); empty input defaults to 0.
func (p *prompter) askIndex(label string, n int) (int, error) {
	s, err := p.ask(label)
	if err != nil {
		return 0, err
	}
	if s == "" {
		return 0, nil
	}
	i, err := strconv.Atoi(s)
	if err != nil || i < 0 || i >= n {
		return 0, fmt.Errorf("invalid choice %q", s)
	}
	return i, nil
}

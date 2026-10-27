// Package command implements cbz's CLI command handlers. Each exported function
// (List, Validate, Info, Strip, Tag, Pack) parses its own flags and operates on
// a single .cbz file or, where it makes sense, a directory. The handlers
// orchestrate the archive, comicinfo, filename, and provider packages.
package command

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
)

// bidiOverrides are Unicode formatting characters that can reorder how text is
// displayed (e.g. an RTL override making "gpj.exe" render as "exe.jpg"). They
// are neutralized on output alongside control characters.
var bidiOverrides = map[rune]bool{
	0x202A: true, 0x202B: true, 0x202C: true, 0x202D: true, 0x202E: true,
	0x2066: true, 0x2067: true, 0x2068: true, 0x2069: true,
	0x200E: true, 0x200F: true, 0x061C: true,
}

// Sanitize makes an untrusted string safe to print to a terminal: every control
// character (including ESC, the basis of ANSI escape sequences) and every
// bidirectional override is replaced with U+FFFD. Apply it to any string that
// originates from archive contents, a provider, or a filename before printing —
// ZIP entry names, ComicInfo field values, provider-supplied series names, and
// paths discovered by directory listing (downloaded comics carry attacker-chosen
// filenames). Exported so main can sanitize the final error message; strings
// printed via %q are already escaped and don't need it.
func Sanitize(s string) string {
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

// libraryFiles is cbzFiles with optional recursion (-r): with recursive set, a
// directory yields every .cbz anywhere beneath it, in path order.
// filepath.WalkDir doesn't follow symlinked directories, so a link loop can't
// make the walk run forever.
func libraryFiles(p string, recursive bool) ([]string, error) {
	if !recursive {
		return cbzFiles(p)
	}
	if info, err := os.Stat(p); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return cbzFiles(p)
	}
	var out []string
	err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".cbz") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no .cbz files in or below %s", p)
	}
	sort.Strings(out)
	return out, nil
}

// failedErr is the error a multi-file command returns when some files failed
// (each already reported), so the process exits non-zero; nil when none did.
func failedErr(failed, total int) error {
	if failed == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d file(s) failed", failed, total)
}

// writeComicInfo saves ci into the archive (saveComicInfo) and reports it.
func writeComicInfo(path string, ci *comicinfo.ComicInfo) error {
	if err := saveComicInfo(path, ci); err != nil {
		return err
	}
	fmt.Printf("%s: wrote ComicInfo.xml\n", Sanitize(path))
	return nil
}

// saveComicInfo marshals ci and writes it into the archive, replacing any
// existing ComicInfo.xml, via the shared atomic rewrite. It prints nothing, so
// batch runs can report the outcome in their own format.
//
// Every write goes through here, so this is where the archive-derived fields
// are filled in: PageCount, and <Pages> (archive.ComicPages) — keeping any
// hand-set page types from ci.Pages, or else from the ComicInfo being replaced.
func saveComicInfo(path string, ci *comicinfo.ComicInfo) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	existing := ci.Pages
	if existing == nil {
		if old, _, _, _ := a.ReadComicInfo(); old != nil {
			existing = old.Pages
		}
	}
	pages := a.Pages(false)
	a.Close()
	ci.PageCount = len(pages)
	ci.Pages = archive.ComicPages(pages, existing)

	data, err := ci.Marshal()
	if err != nil {
		return err
	}
	keep := func(name string) bool { return !strings.EqualFold(name, archive.ComicInfoName) }
	return archive.RewriteArchive(path, keep, map[string][]byte{archive.ComicInfoName: data})
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

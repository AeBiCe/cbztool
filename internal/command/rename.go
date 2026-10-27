package command

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// Rename implements `cbz rename [-t template] [-n] <path>`: rename archives
// from their ComicInfo.xml using a name template (rename.template in the
// config, or -t). Renames stay in the same directory and never overwrite an
// existing file. Files without metadata, or missing a field the template
// requires, are skipped.
func Rename(args []string) error {
	fs := flag.NewFlagSet("rename", flag.ExitOnError)
	tmplFlag := fs.String("template", "", "name `template` (default: rename.template from the config)")
	fs.StringVar(tmplFlag, "t", "", "shorthand for --template")
	dryRun := fs.Bool("dry-run", false, "show the new names without renaming")
	fs.BoolVar(dryRun, "n", false, "shorthand for --dry-run")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz rename [-t template] [-n] <path>")
		fmt.Fprintln(fs.Output(), `
A template is text with ComicInfo fields in braces:
  {Series}      a field's value (any ComicInfo field, e.g. {Title}, {Year})
  {Number:3}    zero-pad the number's integer part to 3 digits (1 -> 001, 1.5 -> 001.5)
  [ ({Year})]   an optional group, dropped when any field inside it is empty
  \{  \[        a literal brace or bracket
A field outside a group must have a value, or the file is skipped. Characters
that aren't allowed in filenames are replaced (":" -> " -", "/" -> "-").`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("rename: expected exactly one <path>")
	}

	src := *tmplFlag
	if src == "" {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		src = cfg.Rename.Template
	}
	tmpl, err := parseNameTemplate(src)
	if err != nil {
		return fmt.Errorf("rename: template %q: %w", src, err)
	}

	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	var renamed, unchanged, skipped, failed int
	planned := map[string]bool{} // dry-run: targets already claimed by an earlier file
	for _, f := range files {
		newPath, skip, err := renameOne(f, tmpl, *dryRun, planned)
		old := Sanitize(filepath.Base(f))
		switch {
		case err != nil:
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
		case skip != "":
			fmt.Printf("%s %s — skipped: %s\n", term.Warn(os.Stdout), old, Sanitize(skip))
			skipped++
		case newPath == filepath.Clean(f):
			unchanged++
		default:
			suffix := ""
			if *dryRun {
				suffix = " (dry-run)"
			}
			fmt.Printf("%s → %s%s\n", old, Sanitize(filepath.Base(newPath)), suffix)
			renamed++
		}
	}
	verb := "renamed"
	if *dryRun {
		verb = "to rename"
	}
	fmt.Printf("done: %d %s, %d already named, %d skipped, %d failed\n", renamed, verb, unchanged, skipped, failed)
	return failedErr(failed, len(files))
}

// renameOne works out path's new name and renames it (unless dryRun). It
// returns the new path (== path when the name is already right), or a reason
// the file was skipped.
func renameOne(path string, tmpl *nameTemplate, dryRun bool, planned map[string]bool) (newPath, skip string, err error) {
	a, err := archive.Open(path)
	if err != nil {
		return "", "", err
	}
	ci, _, found, cerr := a.ReadComicInfo()
	a.Close()
	switch {
	case !found:
		return "", "no ComicInfo.xml (tag it first)", nil
	case cerr != nil:
		return "", "", fmt.Errorf("ComicInfo.xml: %w", cerr)
	}

	name, err := tmpl.render(ci)
	if err != nil {
		return "", err.Error(), nil
	}
	name = safeFilename(name)
	if name == "" {
		return "", "the template produced an empty name", nil
	}
	path = filepath.Clean(path)
	newPath = filepath.Join(filepath.Dir(path), name+".cbz")
	if newPath == path {
		return path, "", nil
	}

	// Never overwrite: an existing target blocks the rename, unless it is this
	// very file under a different case (a case-only rename on a
	// case-insensitive filesystem). Lstat, so a dangling symlink counts too.
	if info, err := os.Lstat(newPath); err == nil {
		self, serr := os.Lstat(path)
		if serr != nil || !os.SameFile(info, self) {
			return "", fmt.Sprintf("%s already exists", filepath.Base(newPath)), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if dryRun {
		if planned[newPath] {
			return "", fmt.Sprintf("%s is also the new name of another file", filepath.Base(newPath)), nil
		}
		planned[newPath] = true
		return newPath, "", nil
	}
	if err := os.Rename(path, newPath); err != nil {
		return "", "", err
	}
	return newPath, "", nil
}

// nameTemplate is a parsed rename template: literal text and field
// references, some inside optional groups.
type nameTemplate struct {
	parts []tmplPart
}

type tmplPart struct {
	lit   string // literal text (when field is "")
	field string // canonical ComicInfo field name
	pad   int    // zero-pad width for the integer part (0: none)
	group int    // optional group index, or -1 outside any group
}

// parseNameTemplate parses a template (see `cbz rename -h`). Unknown fields,
// unbalanced or nested brackets, and stray braces are errors.
func parseNameTemplate(s string) (*nameTemplate, error) {
	t := &nameTemplate{}
	group, groups := -1, 0
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			t.parts = append(t.parts, tmplPart{lit: lit.String(), group: group})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			if i+1 >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			i++
			lit.WriteByte(s[i])
		case '[':
			if group >= 0 {
				return nil, errors.New("optional groups can't be nested")
			}
			flush()
			group, groups = groups, groups+1
		case ']':
			if group < 0 {
				return nil, errors.New("unmatched ]")
			}
			flush()
			group = -1
		case '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return nil, errors.New("unclosed {")
			}
			ref := s[i+1 : i+end]
			i += end
			name, padStr, hasPad := strings.Cut(ref, ":")
			field, ok := comicinfo.CanonicalField(name)
			if !ok {
				return nil, fmt.Errorf("unknown field {%s}", name)
			}
			part := tmplPart{field: field, group: group}
			if hasPad {
				n, err := strconv.Atoi(padStr)
				if err != nil || n < 1 || n > 10 {
					return nil, fmt.Errorf("{%s}: padding must be a number from 1 to 10", ref)
				}
				part.pad = n
			}
			flush()
			t.parts = append(t.parts, part)
		case '}':
			return nil, errors.New("unmatched }")
		default:
			lit.WriteByte(c)
		}
	}
	if group >= 0 {
		return nil, errors.New("unclosed [")
	}
	flush()
	return t, nil
}

// render fills the template from ci. An optional group is dropped when any
// field in it is empty; an empty field outside a group is an error naming it.
func (t *nameTemplate) render(ci *comicinfo.ComicInfo) (string, error) {
	values := make([]string, len(t.parts))
	dropped := map[int]bool{}
	var missing []string
	for i, p := range t.parts {
		if p.field == "" {
			values[i] = p.lit
			continue
		}
		v := strings.TrimSpace(ci.Get(p.field))
		if v != "" && p.pad > 0 {
			v = padNumber(v, p.pad)
		}
		values[i] = v
		if v == "" {
			if p.group >= 0 {
				dropped[p.group] = true
			} else {
				missing = append(missing, p.field)
			}
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("no %s in ComicInfo.xml", strings.Join(missing, ", "))
	}
	var b strings.Builder
	for i, p := range t.parts {
		if p.group < 0 || !dropped[p.group] {
			b.WriteString(values[i])
		}
	}
	return b.String(), nil
}

// padNumber zero-pads the leading run of digits in v to width: "1" -> "001",
// "1.5" -> "001.5", "12a" -> "012a". A value not starting with a digit is
// returned unchanged.
func padNumber(v string, width int) string {
	n := 0
	for n < len(v) && v[n] >= '0' && v[n] <= '9' {
		n++
	}
	if n == 0 || n >= width {
		return v
	}
	return strings.Repeat("0", width-n) + v
}

// filenameReplacer maps characters that aren't allowed in filenames (on any
// common OS) to readable substitutes.
var filenameReplacer = strings.NewReplacer(
	":", " -", "/", "-", "\\", "-", "|", "-", `"`, "'",
	"?", "", "*", "", "<", "", ">", "",
)

// maxNameBytes leaves room for ".cbz" within the usual 255-byte name limit.
const maxNameBytes = 250

// safeFilename turns a rendered template into a usable file name: illegal
// characters replaced, control characters dropped, whitespace runs collapsed,
// leading dots (hidden files) and trailing dots/spaces (invalid on Windows)
// trimmed, and the result cut to maxNameBytes on a character boundary.
func safeFilename(s string) string {
	s = filenameReplacer.Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || bidiOverrides[r] {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, ". ")
	s = strings.TrimRight(s, ". ")
	for len(s) > maxNameBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimRight(s, ". ")
}

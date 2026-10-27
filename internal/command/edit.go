package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/filename"
)

// Edit implements `cbz edit <file.cbz>`: open the archive's ComicInfo as JSON
// in $VISUAL / $EDITOR (default vi) and write back what was saved. The JSON is
// the whole record — a field emptied in the editor is removed — and PageCount
// and <Pages> are recomputed from the archive as on every write.
func Edit(args []string) error {
	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz edit <file.cbz>")
		fmt.Fprintln(fs.Output(), "Opens the metadata as JSON in $VISUAL or $EDITOR (default vi). Every field is listed;")
		fmt.Fprintln(fs.Output(), "empty a value to remove that field. To edit many files at once, use cbz tag -m -t / -f.")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("edit: expected exactly one <file.cbz>")
	}
	path := fs.Arg(0)
	if info, err := os.Stat(path); err != nil {
		return err
	} else if info.IsDir() {
		return fmt.Errorf("edit: %s is a directory; edit takes one .cbz (for many, use cbz tag -m -t / -f)", path)
	}
	if _, err := cbzFiles(path); err != nil {
		return err
	}
	return editOne(path, runEditor, newPrompter())
}

// runEditor opens path in the user's editor and waits for it to exit. The
// command comes from $VISUAL or $EDITOR (split on spaces, so "code -w" works)
// and is run directly, not through a shell.
func runEditor(path string) error {
	argv := []string{"vi"}
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(v)); len(f) > 0 {
			argv = f
			break
		}
	}
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %s: %w", argv[0], err)
	}
	return nil
}

// editOne runs the edit cycle for one archive: write the JSON to a private
// temp file, run edit on it until it parses (or the user gives up), then save
// if anything changed.
func editOne(path string, edit func(string) error, p *prompter) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	orig, _, found, err := a.ReadComicInfo()
	a.Close()
	switch {
	case err != nil:
		return fmt.Errorf("existing ComicInfo.xml can't be parsed (%w); fix it or remove it with cbz strip", err)
	case !found:
		parsed := filename.Parse(path)
		orig = &comicinfo.ComicInfo{Series: parsed.Series, Number: parsed.Number}
	}

	doc := editDoc(orig)
	// Compare against the document as cbz itself would read it back, so value
	// tidying (list spacing, enum case) never shows up as an edit.
	base, err := parseEditDoc(doc)
	if err != nil {
		base = orig
	}

	tmp, err := os.CreateTemp("", "cbz-edit-*.json") // 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(doc)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmpName)
		return werr
	}

	var edited *comicinfo.ComicInfo
	for {
		if err := edit(tmpName); err != nil {
			os.Remove(tmpName)
			return err
		}
		data, err := os.ReadFile(tmpName)
		if err == nil {
			edited, err = parseEditDoc(data)
		}
		if err == nil {
			break
		}
		fmt.Printf("invalid: %s\n", Sanitize(err.Error()))
		again, aerr := p.ask("edit again? [Y/n]")
		if aerr != nil || strings.HasPrefix(strings.ToLower(again), "n") {
			return fmt.Errorf("edit aborted; nothing written (your edits are in %s)", tmpName)
		}
	}
	os.Remove(tmpName)

	changes := fieldChanges(base, edited)
	if len(changes) == 0 {
		fmt.Printf("%s: no changes\n", Sanitize(path))
		return nil
	}
	if err := saveComicInfo(path, edited); err != nil {
		return err
	}
	fmt.Printf("%s: wrote ComicInfo.xml\n", Sanitize(path))
	for _, c := range changes {
		fmt.Printf("  %s\n", c)
	}
	return nil
}

// editDoc renders ci as a flat JSON object of every settable field, in
// schema order, empty ones included so they can be filled in.
func editDoc(ci *comicinfo.ComicInfo) []byte {
	var fields []kv
	for _, f := range comicinfo.Fields() {
		fields = append(fields, kv{f, ci.Get(f)})
	}
	var b strings.Builder
	writeObject(&b, "", fields)
	b.WriteString("\n")
	return []byte(b.String())
}

// parseEditDoc reads an edited document onto an empty ComicInfo, so a field
// left empty (or deleted) ends up unset.
func parseEditDoc(data []byte) (*comicinfo.ComicInfo, error) {
	var block map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&block); err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("not valid JSON: unexpected data after the object")
	}
	if block == nil {
		return nil, errors.New("expected a JSON object of fields")
	}
	return decodeBlock("metadata", block)
}

// fieldChanges lists the fields that differ between before and after as
// "Field: old → new" lines (values shortened for display).
func fieldChanges(before, after *comicinfo.ComicInfo) []string {
	show := func(s string) string {
		if s == "" {
			return "(empty)"
		}
		if r := []rune(s); len(r) > 60 {
			s = string(r[:57]) + "…"
		}
		return Sanitize(s)
	}
	var out []string
	for _, f := range comicinfo.Fields() {
		if o, n := before.Get(f), after.Get(f); o != n {
			out = append(out, fmt.Sprintf("%s: %s → %s", f, show(o), show(n)))
		}
	}
	return out
}

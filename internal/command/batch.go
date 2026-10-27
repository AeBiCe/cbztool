package command

import (
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/Nikode/cbztool/internal/term"
)

// skipAlreadyTagged is the routine skip of a re-run; it's the one skip that
// isn't flagged as a warning.
const skipAlreadyTagged = "already tagged (--force to overwrite)"

// batch reports a multi-file run: each file's outcome gets one line with its
// position ("[12/140]"), optionally grouped under section headers; skipped and
// failed files are listed again at the end, and done returns an error when
// anything failed so the process exits non-zero (a skip is not a failure).
// Warnings are marked [!] and failures [X].
type batch struct {
	total, n        int
	written         int
	dryRun          bool
	indent          string // set by header: file lines are nested under it
	numWidth        int    // width of the "#N" column; 0 omits the column
	skipped, failed []batchNote
}

type batchNote struct {
	path, reason string
	warn         bool
}

func newBatch(total int, dryRun bool) *batch { return &batch{total: total, dryRun: dryRun} }

// fileOutcome is what happened to one file: written (or resolved, under
// dry-run), skipped for a reason, or failed with err.
type fileOutcome struct {
	written bool
	number  string // issue number, for the "#N" column
	skip    string // why the file was left untouched
	detail  string // extra context for a write, e.g. the contributing providers
	notes   []note // printed indented under the file's line
	dump    []byte // dry-run: the resolved ComicInfo.xml, printed after the notes
	err     error
}

// note is a line of extra context about one file: a warning, or (under -v) a
// detail of how it was resolved.
type note struct {
	warn bool
	text string
}

func (o fileOutcome) warned() bool {
	if o.skip != "" && o.skip != skipAlreadyTagged {
		return true
	}
	for _, n := range o.notes {
		if n.warn {
			return true
		}
	}
	return false
}

// summary is the outcome text after the file name.
func (o fileOutcome) summary(dryRun bool) string {
	switch {
	case o.err != nil:
		return "failed: " + o.err.Error()
	case o.skip != "":
		return "skipped: " + o.skip
	}
	s := "tagged"
	if dryRun {
		s = "resolved (dry-run)"
	}
	if o.detail != "" {
		s += " (" + o.detail + ")"
	}
	return s
}

// marker returns the 3-column status marker for a line written to f.
func marker(f *os.File, warn, fail bool) string {
	switch {
	case fail:
		return term.Error(f)
	case warn:
		return term.Warn(f)
	}
	return "   "
}

// header starts a section (e.g. one series); file lines that follow are
// indented under it. A warning header is marked [!], a failed one [X].
func (b *batch) header(text string, warn, fail bool) {
	m := ""
	if warn || fail {
		m = marker(os.Stdout, warn, fail) + " "
	}
	fmt.Printf("%s%s\n", m, Sanitize(text))
	b.indent = "  "
}

// record reports o as the next file's outcome: one line, then its notes.
func (b *batch) record(path string, o fileOutcome) {
	b.n++
	name := filepath.Base(path)
	switch {
	case o.err != nil:
		b.failed = append(b.failed, batchNote{name, o.err.Error(), true})
	case o.skip != "":
		b.skipped = append(b.skipped, batchNote{name, o.skip, o.warned()})
	case o.written:
		b.written++
	}

	out := os.Stdout
	if o.err != nil {
		out = os.Stderr
	}
	w := len(fmt.Sprint(b.total))
	num := ""
	if b.numWidth > 0 {
		n := o.number
		if n == "" {
			n = "?"
		}
		num = fmt.Sprintf("#%-*s ", b.numWidth, Sanitize(n))
	}
	fmt.Fprintf(out, "%s%s [%*d/%d] %s%s — %s\n", b.indent, marker(out, o.warned(), o.err != nil),
		w, b.n, b.total, num, Sanitize(name), Sanitize(o.summary(b.dryRun)))
	printFileNotes(b.indent+"      ", o.notes)
	os.Stdout.Write(o.dump)
}

// reportOne prints a single file's outcome (no numbering or summary) and
// returns its error, for commands run on one file.
func reportOne(path string, o fileOutcome, dryRun bool) error {
	if o.err != nil {
		return o.err
	}
	m := ""
	if o.warned() {
		m = term.Warn(os.Stdout) + " "
	}
	fmt.Printf("%s%s — %s\n", m, Sanitize(filepath.Base(path)), Sanitize(o.summary(dryRun)))
	printFileNotes("  ", o.notes)
	os.Stdout.Write(o.dump)
	return nil
}

// printFileNotes prints notes indented under a file's line; warnings are
// marked [!].
func printFileNotes(indent string, notes []note) {
	for _, n := range notes {
		m := "-"
		if n.warn {
			m = term.Warn(os.Stdout)
		}
		fmt.Printf("%s%s %s\n", indent, m, Sanitize(n.text))
	}
}

// done prints the summary — counts, then every skipped and failed file with
// its reason — and returns an error if any file failed.
func (b *batch) done() error {
	verb := "tagged"
	if b.dryRun {
		verb = "resolved"
	}
	fmt.Printf("done: %d %s, %d skipped, %d failed\n", b.written, verb, len(b.skipped), len(b.failed))
	if len(b.skipped) > 0 {
		fmt.Println("skipped:")
		for _, n := range b.skipped {
			fmt.Printf("  %s %s — %s\n", marker(os.Stdout, n.warn, false), Sanitize(n.path), Sanitize(n.reason))
		}
	}
	if len(b.failed) > 0 {
		fmt.Println("failed:")
		for _, n := range b.failed {
			fmt.Printf("  %s %s — %s\n", marker(os.Stdout, false, true), Sanitize(n.path), Sanitize(n.reason))
		}
	}
	if len(b.failed) > 0 {
		return fmt.Errorf("%d of %d file(s) failed", len(b.failed), b.total)
	}
	return nil
}

// numColumn returns the width of the "#N" column for the given issue numbers
// (at least 1, for "?").
func numColumn(numbers []string) int {
	w := 1
	for _, n := range numbers {
		w = max(w, len(n))
	}
	return w
}

// counted renders "1 file" / "5 files".
func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

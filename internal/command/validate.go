package command

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// Validate implements `cbz validate|vl [--full] <path>`: check each archive's
// page sequence and that every page is a readable image; --full also verifies
// every entry's checksum. OS junk and extra files are reported but don't fail.
func Validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	full := fs.Bool("full", false, "also read every page to the end, verifying its checksum (slower)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz validate [--full] <path>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("usage: cbz validate [--full] <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range files {
		ok, err := validateOne(f, *full)
		if err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
			continue
		}
		if !ok {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d archive(s) failed validation", failed, len(files))
	}
	return nil
}

// validateOne checks a single archive: a contiguous 1..N page sequence
// (missing numbers, duplicates, a non-1 start, unnumbered images), and that
// every page's header decodes as an image — with full, that every page also
// reads to the end with a matching checksum. Junk and extra entries are
// printed as warnings without failing. It returns false (without an error)
// when the archive is reachable but invalid.
func validateOne(path string, full bool) (bool, error) {
	a, err := archive.Open(path)
	if err != nil {
		return false, err
	}
	defer a.Close()

	warnings := entryWarnings(a)
	pages := a.Pages(full)
	if len(pages) == 0 {
		fmt.Printf("%s %s: no page images found\n", term.Error(os.Stdout), Sanitize(path))
		printNotes(true, warnings)
		return false, nil
	}

	images := make([]string, len(pages))
	var unreadable []string
	for i, p := range pages {
		images[i] = p.Name
		if p.Err != nil {
			unreadable = append(unreadable, fmt.Sprintf("unreadable page %s: %s", Sanitize(p.Name), Sanitize(p.Err.Error())))
		}
	}
	problems, maxNum := pageProblems(images)
	problems = append(problems, unreadable...)
	if len(problems) == 0 {
		fmt.Printf("%s: OK (%d pages, 1-%d)\n", Sanitize(path), len(images), maxNum)
		printNotes(true, warnings)
		return true, nil
	}
	fmt.Printf("%s %s: %d page images, problems found:\n", term.Error(os.Stdout), Sanitize(path), len(images))
	printNotes(false, problems)
	printNotes(true, warnings)
	return false, nil
}

// entryWarnings describes the non-page entries cbz clean would remove: OS junk,
// and extra files (removed only with --extra).
func entryWarnings(a *archive.Archive) []string {
	var junk, extra []string
	for _, f := range a.Files() {
		switch archive.Classify(f.Name) {
		case archive.KindJunk:
			junk = append(junk, f.Name)
		case archive.KindExtra:
			extra = append(extra, f.Name)
		}
	}
	var out []string
	if len(junk) > 0 {
		out = append(out, fmt.Sprintf("OS junk: %s (remove with cbz clean)", Sanitize(strings.Join(junk, ", "))))
	}
	if len(extra) > 0 {
		out = append(out, fmt.Sprintf("extra files: %s (remove with cbz clean --extra)", Sanitize(strings.Join(extra, ", "))))
	}
	return out
}

// printNotes prints each note as an indented list item, marked [!] for a
// warning or [X] for a problem.
func printNotes(warn bool, notes []string) {
	m := term.Error(os.Stdout)
	if warn {
		m = term.Warn(os.Stdout)
	}
	for _, n := range notes {
		fmt.Printf("  %s %s\n", m, n)
	}
}

// pageProblems analyzes image entry names for a contiguous 1..N page sequence
// and returns the human-readable problems plus the highest valid page number.
// The page number of an image is the trailing run of digits in its name (image
// extensions never end in digits, so no stripping is needed).
func pageProblems(images []string) (problems []string, maxNum int) {
	// Outlier guard: a "page number" far beyond the image count is almost
	// certainly not a page index — e.g. a cover named "Batman (2016).jpg" whose
	// trailing digits are a year. Counting it would inflate the missing list
	// with a huge bogus gap (4..2015), so such images are treated as unnumbered.
	pageLimit := len(images)*2 + 20

	byNum := make(map[int][]string)
	var unnumbered []string
	minNum := -1
	for _, name := range images {
		n, _, ok := archive.LastNumber(name)
		if !ok || n > pageLimit {
			unnumbered = append(unnumbered, name)
			continue
		}
		byNum[n] = append(byNum[n], name)
		if n > maxNum {
			maxNum = n
		}
		if minNum == -1 || n < minNum {
			minNum = n
		}
	}

	var missing []int
	for i := 1; i <= maxNum; i++ {
		if _, ok := byNum[i]; !ok {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		problems = append(problems, "missing: "+joinInts(missing))
	}

	for _, n := range sortedKeys(byNum) {
		if len(byNum[n]) > 1 {
			problems = append(problems, fmt.Sprintf("duplicate page %d: %s", n, Sanitize(strings.Join(byNum[n], ", "))))
		}
	}

	if minNum > 1 {
		problems = append(problems, fmt.Sprintf("does not start at page 1 (first is %d)", minNum))
	}

	if len(unnumbered) > 0 {
		problems = append(problems, "no page number: "+Sanitize(strings.Join(unnumbered, ", ")))
	}

	return problems, maxNum
}

// joinInts renders a list of ints as a comma-separated string.
func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys(m map[int][]string) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

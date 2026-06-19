package command

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"cb/internal/archive"
)

// Validate implements `cb validate|vl <path>`: check for missing pages.
func Validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cb validate <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range files {
		ok, err := validateOne(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, err)
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

// validateOne checks a single archive for a contiguous 1..N page sequence,
// reporting missing numbers, duplicates, a non-1 start, and unnumbered images.
// It returns false (without an error) when the archive is reachable but invalid.
func validateOne(path string) (bool, error) {
	a, err := archive.Open(path)
	if err != nil {
		return false, err
	}
	defer a.Close()

	images := a.ImageEntries()
	if len(images) == 0 {
		fmt.Printf("%s: no page images found\n", path)
		return false, nil
	}

	problems, maxNum := pageProblems(images)
	if len(problems) == 0 {
		fmt.Printf("%s: OK (%d pages, 1-%d)\n", path, len(images), maxNum)
		return true, nil
	}
	fmt.Printf("%s: %d page images, problems found:\n", path, len(images))
	for _, p := range problems {
		fmt.Printf("  - %s\n", p)
	}
	return false, nil
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
			problems = append(problems, fmt.Sprintf("duplicate page %d: %s", n, sanitize(strings.Join(byNum[n], ", "))))
		}
	}

	if minNum > 1 {
		problems = append(problems, fmt.Sprintf("does not start at page 1 (first is %d)", minNum))
	}

	if len(unnumbered) > 0 {
		problems = append(problems, "no page number: "+sanitize(strings.Join(unnumbered, ", ")))
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

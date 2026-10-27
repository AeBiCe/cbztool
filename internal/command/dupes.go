package command

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// Dupes implements `cbz dupes [-r] <dir>`: report archives with identical
// pages. Matching is exact — the same page images byte for byte, compared via
// archive.Fingerprint from the ZIP directory, so no page is decompressed —
// and renamed or repacked copies are found. Nothing is ever deleted.
func Dupes(args []string) error {
	fs := flag.NewFlagSet("dupes", flag.ExitOnError)
	recursive := fs.Bool("r", false, "include .cbz files in subdirectories")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz dupes [-r] <dir>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("dupes: expected exactly one <dir>")
	}
	files, err := libraryFiles(fs.Arg(0), *recursive)
	if err != nil {
		return err
	}

	type copyInfo struct {
		path   string
		size   int64
		pages  int
		tagged bool
	}
	byPrint := map[string][]copyInfo{}
	failed := 0
	for _, f := range files {
		a, err := archive.Open(f)
		if err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
			continue
		}
		fp := a.Fingerprint()
		c := copyInfo{path: f, pages: a.PageCount(), tagged: a.HasComicInfo()}
		a.Close()
		if fp == "" {
			continue // no pages: nothing to compare
		}
		if info, err := os.Stat(f); err == nil {
			c.size = info.Size()
		}
		byPrint[fp] = append(byPrint[fp], c)
	}

	var groups [][]copyInfo
	for _, g := range byPrint {
		if len(g) > 1 {
			groups = append(groups, g) // files are already in path order
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0].path < groups[j][0].path })
	for _, g := range groups {
		fmt.Printf("%d identical archives (%d pages):\n", len(g), g[0].pages)
		for _, c := range g {
			note := ""
			if c.tagged {
				note = ", tagged"
			}
			fmt.Printf("  %s  (%s%s)\n", Sanitize(c.path), humanSize(c.size), note)
		}
	}
	if len(groups) == 0 {
		fmt.Printf("no duplicates among %d archive(s)\n", len(files)-failed)
	} else {
		fmt.Printf("%d set(s) of duplicates among %d archive(s)\n", len(groups), len(files)-failed)
	}
	return failedErr(failed, len(files))
}

// humanSize renders a byte count with a binary unit: 512 B, 1.5 KiB, 3.2 GiB.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

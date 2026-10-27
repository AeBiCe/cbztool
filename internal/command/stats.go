package command

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/filename"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// Stats implements `cbz stats [-r] [-v] <path>`: a summary of a library — how
// many archives, pages and series, how many are tagged, and how many have
// page-numbering problems or OS junk. It reads only each archive's ZIP
// directory and ComicInfo.xml, so it stays fast on large libraries; use
// cbz validate for per-archive detail (and page image checks).
func Stats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	recursive := fs.Bool("r", false, "include .cbz files in subdirectories")
	verbose := fs.Bool("v", false, "list the files behind each count (untagged, problems, junk)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz stats [-r] [-v] <path>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("stats: expected exactly one <path>")
	}
	files, err := libraryFiles(fs.Arg(0), *recursive)
	if err != nil {
		return err
	}

	var (
		size                                     int64
		pages                                    int
		tagged                                   int
		untagged, malformed, numbering, withJunk []string
		failed                                   []string
		series                                   = map[string]bool{} // matchKey of each series name
	)
	for _, f := range files {
		a, err := archive.Open(f)
		if err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed = append(failed, f)
			continue
		}
		if info, err := os.Stat(f); err == nil {
			size += info.Size()
		}
		images := a.ImageEntries()
		pages += len(images)
		if probs, _ := pageProblems(images); len(probs) > 0 || len(images) == 0 {
			numbering = append(numbering, f)
		}
		for _, e := range a.Files() {
			if archive.Classify(e.Name) == archive.KindJunk {
				withJunk = append(withJunk, f)
				break
			}
		}

		name := ""
		ci, _, found, cerr := a.ReadComicInfo()
		switch {
		case found && cerr == nil:
			tagged++
			name = ci.Series
		case found:
			malformed = append(malformed, f)
		default:
			untagged = append(untagged, f)
		}
		a.Close()
		if name == "" {
			name = filename.Parse(f).Series
		}
		if k := matchKey(name); k != "" {
			series[k] = true
		}
	}

	n := len(files) - len(failed)
	row := func(label, value string) { fmt.Printf("%-12s %s\n", label, value) }
	row("archives", fmt.Sprint(n))
	row("size", humanSize(size))
	if n > 0 {
		row("pages", fmt.Sprintf("%d (%d per archive on average)", pages, pages/n))
		row("tagged", fmt.Sprintf("%d (%d%%)", tagged, tagged*100/n))
	}
	row("untagged", fmt.Sprint(len(untagged)))
	row("series", fmt.Sprint(len(series)))
	row("numbering", fmt.Sprintf("%d with page-numbering problems (see cbz validate)", len(numbering)))
	row("OS junk", fmt.Sprintf("%d with junk files (see cbz clean)", len(withJunk)))
	if len(malformed) > 0 {
		row("malformed", fmt.Sprintf("%d with an unreadable ComicInfo.xml", len(malformed)))
	}
	if len(failed) > 0 {
		row("unreadable", fmt.Sprintf("%d archive(s) could not be opened", len(failed)))
	}

	if *verbose {
		for _, l := range []struct {
			title string
			paths []string
		}{
			{"untagged", untagged}, {"malformed ComicInfo.xml", malformed},
			{"page-numbering problems", numbering}, {"OS junk", withJunk}, {"unreadable", failed},
		} {
			if len(l.paths) == 0 {
				continue
			}
			sort.Strings(l.paths)
			fmt.Printf("\n%s:\n", l.title)
			for _, p := range l.paths {
				fmt.Printf("  %s\n", Sanitize(p))
			}
		}
	}
	return failedErr(len(failed), len(files))
}

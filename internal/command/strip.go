package command

import (
	"flag"
	"fmt"
	"strings"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// Strip implements `cbz strip <path>`: remove ComicInfo.xml.
func Strip(args []string) error {
	fs := flag.NewFlagSet("strip", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cbz strip <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range files {
		if err := stripOne(f); err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
		}
	}
	return failedErr(failed, len(files))
}

func stripOne(path string) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	// Only the presence of the entry matters; strip even a malformed one.
	_, _, found, _ := a.ReadComicInfo()
	a.Close()
	if !found {
		fmt.Printf("%s: no ComicInfo.xml\n", Sanitize(path))
		return nil
	}

	keep := func(name string) bool { return !strings.EqualFold(name, archive.ComicInfoName) }
	if err := archive.RewriteArchive(path, keep, nil); err != nil {
		return err
	}
	fmt.Printf("%s: removed ComicInfo.xml\n", Sanitize(path))
	return nil
}

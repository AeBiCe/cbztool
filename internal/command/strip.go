package command

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"cb/internal/archive"
)

// Strip implements `cb strip <path>`: remove ComicInfo.xml.
func Strip(args []string) error {
	fs := flag.NewFlagSet("strip", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cb strip <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := stripOne(f); err != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, err)
		}
	}
	return nil
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
		fmt.Printf("%s: no ComicInfo.xml\n", path)
		return nil
	}

	keep := func(name string) bool { return !strings.EqualFold(name, archive.ComicInfoName) }
	if err := archive.RewriteArchive(path, keep, nil); err != nil {
		return err
	}
	fmt.Printf("%s: removed ComicInfo.xml\n", path)
	return nil
}

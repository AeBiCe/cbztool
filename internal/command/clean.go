package command

import (
	"flag"
	"fmt"
	"strings"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/term"
)

// Clean implements `cbz clean [--extra] [--dry-run] <path>`: remove OS junk
// (__MACOSX/, ._*, .DS_Store, Thumbs.db, desktop.ini) from archives, and with
// --extra every other entry that is neither a page nor ComicInfo.xml. Entries
// are only ever dropped; everything kept is copied byte-for-byte.
func Clean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	extra := fs.Bool("extra", false, "also remove files that are neither pages nor ComicInfo.xml (.nfo, .txt, …)")
	dryRun := fs.Bool("dry-run", false, "list what would be removed without changing anything")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz clean [--extra] [--dry-run] <path>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("usage: cbz clean [--extra] [--dry-run] <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range files {
		if err := cleanOne(f, *extra, *dryRun); err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
		}
	}
	return failedErr(failed, len(files))
}

// cleanOne removes the junk (and, with extra, the extra) entries from one
// archive via the shared atomic rewrite.
func cleanOne(path string, extra, dryRun bool) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	var remove []string
	for _, f := range a.Files() {
		if removable(f.Name, extra) {
			remove = append(remove, f.Name)
		}
	}
	a.Close()

	list := Sanitize(strings.Join(remove, ", "))
	switch {
	case len(remove) == 0:
		fmt.Printf("%s: nothing to clean\n", Sanitize(path))
		return nil
	case dryRun:
		fmt.Printf("%s: would remove %d entr%s: %s\n", Sanitize(path), len(remove), plural(len(remove)), list)
		return nil
	}
	keep := func(name string) bool { return !removable(name, extra) }
	if err := archive.RewriteArchive(path, keep, nil); err != nil {
		return err
	}
	fmt.Printf("%s: removed %d entr%s: %s\n", Sanitize(path), len(remove), plural(len(remove)), list)
	return nil
}

// removable reports whether clean drops the entry name.
func removable(name string, extra bool) bool {
	switch archive.Classify(name) {
	case archive.KindJunk:
		return true
	case archive.KindExtra:
		return extra
	}
	return false
}

// plural is the suffix for "entr…": "y" for one, "ies" otherwise.
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

package command

import (
	"flag"
	"fmt"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// List implements `cbz list|ls <path>`: print the contents of each archive.
func List(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cbz list <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range files {
		if err := listOne(f); err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
		}
	}
	return failedErr(failed, len(files))
}

func listOne(path string) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	defer a.Close()

	fmt.Println(Sanitize(path))
	for _, f := range a.Files() {
		if f.FileInfo().IsDir() {
			continue
		}
		fmt.Printf("  %10d  %s\n", f.UncompressedSize64, Sanitize(f.Name))
	}
	return nil
}

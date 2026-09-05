package command

import (
	"flag"
	"fmt"
	"os"

	"cb/internal/archive"
)

// List implements `cb list|ls <path>`: print the contents of each archive.
func List(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cb list <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := listOne(f); err != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %s\n", Sanitize(f), Sanitize(err.Error()))
		}
	}
	return nil
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

package command

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"

	"cb/internal/archive"
	"cb/internal/comicinfo"
)

// Info implements `cb info <path>`: print ComicInfo.xml metadata.
func Info(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cb info <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := infoOne(f); err != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %s\n", Sanitize(f), Sanitize(err.Error()))
		}
	}
	return nil
}

func infoOne(path string) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	defer a.Close()

	ci, _, found, err := a.ReadComicInfo()
	if err != nil {
		return err
	}
	fmt.Println(Sanitize(path))
	if !found {
		fmt.Println("  (no ComicInfo.xml)")
		return nil
	}
	printComicInfo(ci)
	return nil
}

// printComicInfo prints every populated (non-zero) scalar field of ci, labeled
// by its XML element name, so it reflects whatever tag wrote.
func printComicInfo(ci *comicinfo.ComicInfo) {
	v := reflect.ValueOf(*ci)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == "XMLName" || f.Name == "Pages" {
			continue
		}
		name := xmlLocalName(f.Tag.Get("xml"))
		switch val := v.Field(i).Interface().(type) {
		case string:
			if val != "" {
				fmt.Printf("  %-20s %s\n", name, Sanitize(val))
			}
		case int:
			if val != 0 {
				fmt.Printf("  %-20s %d\n", name, val)
			}
		case float64:
			if val != 0 {
				fmt.Printf("  %-20s %g\n", name, val)
			}
		}
	}
}

// xmlLocalName extracts the element name from an xml struct tag (dropping any
// "parent>" prefix and ",option" suffixes).
func xmlLocalName(tag string) string {
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if i := strings.IndexByte(tag, '>'); i >= 0 {
		tag = tag[i+1:]
	}
	return tag
}

package command

import (
	"flag"
	"fmt"
	"reflect"
	"strings"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/term"
)

// Info implements `cbz info <path>`: print ComicInfo.xml metadata.
func Info(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cbz info <path>")
	}
	files, err := cbzFiles(fs.Arg(0))
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range files {
		if err := infoOne(f); err != nil {
			term.Errorf("%s: %s", Sanitize(f), Sanitize(err.Error()))
			failed++
		}
	}
	return failedErr(failed, len(files))
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
	if ci.Pages != nil {
		fmt.Printf("  %-20s %s\n", "Pages", pagesSummary(ci.Pages))
	}
}

// pagesSummary condenses <Pages> to one line: how many pages are described,
// how many are double spreads, and any page types set.
func pagesSummary(p *comicinfo.Pages) string {
	double := 0
	var types []string
	for _, pg := range p.Page {
		if pg.DoublePage {
			double++
		}
		if pg.Type != "" {
			types = append(types, fmt.Sprintf("%d=%s", pg.Image+1, pg.Type))
		}
	}
	s := fmt.Sprintf("%d described", len(p.Page))
	if double > 0 {
		s += fmt.Sprintf(", %d double-page", double)
	}
	if len(types) > 0 {
		s += "; " + Sanitize(strings.Join(types, ", "))
	}
	return s
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

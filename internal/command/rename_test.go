package command

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/config"
)

func TestParseNameTemplateErrors(t *testing.T) {
	cases := map[string]string{
		"{Nope}":            "unknown field",
		"{Series":           "unclosed {",
		"Series}":           "unmatched }",
		"[a [b]]":           "nested",
		"a]":                "unmatched ]",
		"[ ({Year})":        "unclosed [",
		"{Number:0}":        "padding",
		"{Number:x}":        "padding",
		`trailing \`:        "backslash",
		"{PageCount}":       "unknown field", // computed, not a settable field
		"{Series} {Pages}":  "unknown field",
		"ok {Series} [{x}]": "unknown field",
	}
	for in, want := range cases {
		if _, err := parseNameTemplate(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseNameTemplate(%q) err = %v, want containing %q", in, err, want)
		}
	}
}

func TestRenderNameTemplate(t *testing.T) {
	saga := &comicinfo.ComicInfo{Series: "Saga", Number: "1", Year: 2012, Title: "Chapter One"}
	cases := []struct {
		tmpl    string
		ci      *comicinfo.ComicInfo
		want    string
		wantErr string
	}{
		{config.DefaultTemplate, saga, "Saga (2012) #001", ""},
		{config.DefaultTemplate, &comicinfo.ComicInfo{Series: "Saga", Number: "1.5"}, "Saga #001.5", ""}, // group dropped
		{config.DefaultTemplate, &comicinfo.ComicInfo{Series: "Saga", Number: "1000"}, "Saga #1000", ""},
		{config.DefaultTemplate, &comicinfo.ComicInfo{Series: "Saga"}, "", "no Number"},
		{"{series} - {title}", saga, "Saga - Chapter One", ""}, // field names are case-insensitive
		{"{Series}[ v{Volume}][ - {Title}]", saga, "Saga - Chapter One", ""},
		{`\{{Series}\} \[x\]`, saga, "{Saga} [x]", ""},
		{"{Series} [({Publisher} {Year})]", saga, "Saga ", ""}, // any empty field drops the group
	}
	for _, c := range cases {
		tmpl, err := parseNameTemplate(c.tmpl)
		if err != nil {
			t.Fatalf("parse %q: %v", c.tmpl, err)
		}
		got, err := tmpl.render(c.ci)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("render(%q) err = %v, want %q", c.tmpl, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("render(%q) = %q, %v; want %q", c.tmpl, got, err, c.want)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"Batman: Year One #001":     "Batman - Year One #001",
		"Aliens / Vampirella #001":  "Aliens - Vampirella #001",
		`What If? "Spider*Man" <1>`: "What If 'SpiderMan' 1",
		"  spaced   out  ":          "spaced out",
		"...hidden":                 "hidden",
		"trailing dot.":             "trailing dot",
		"tab\tand\x1bescape":        "tab andescape",
		"":                          "",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := safeFilename(strings.Repeat("é", 300))
	if len(long) > maxNameBytes || !strings.HasPrefix(strings.Repeat("é", 125), long) {
		t.Errorf("long name cut badly: %d bytes", len(long))
	}
}

func TestRenameOne(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, ci *comicinfo.ComicInfo) string {
		p := filepath.Join(dir, name)
		entries := map[string][]byte{"001.jpg": []byte("page")}
		if ci != nil {
			data, err := ci.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			entries[archive.ComicInfoName] = data
		}
		if err := archive.BuildArchive(p, entries, false); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tmpl, err := parseNameTemplate(config.DefaultTemplate)
	if err != nil {
		t.Fatal(err)
	}

	a := mk("saga_01.cbz", &comicinfo.ComicInfo{Series: "Saga", Number: "1", Year: 2012})
	b := mk("saga 1 copy.cbz", &comicinfo.ComicInfo{Series: "Saga", Number: "1", Year: 2012})
	c := mk("untagged.cbz", nil)
	d := mk("Saga (2012) #002.cbz", &comicinfo.ComicInfo{Series: "Saga", Number: "2", Year: 2012})

	t.Run("dry run claims targets and touches nothing", func(t *testing.T) {
		planned := map[string]bool{}
		np, skip, err := renameOne(a, tmpl, true, planned)
		if err != nil || skip != "" || filepath.Base(np) != "Saga (2012) #001.cbz" {
			t.Fatalf("a: %q %q %v", np, skip, err)
		}
		if _, skip, _ := renameOne(b, tmpl, true, planned); !strings.Contains(skip, "another file") {
			t.Errorf("b should collide with a's planned name, skip=%q", skip)
		}
		if _, err := os.Stat(a); err != nil {
			t.Error("dry run renamed a file")
		}
	})
	t.Run("rename, collision, untagged, already named", func(t *testing.T) {
		np, skip, err := renameOne(a, tmpl, false, nil)
		if err != nil || skip != "" {
			t.Fatalf("a: %q %v", skip, err)
		}
		if _, skip, _ := renameOne(b, tmpl, false, nil); !strings.Contains(skip, "already exists") {
			t.Errorf("b overwrote a? skip=%q", skip)
		}
		if _, skip, _ := renameOne(c, tmpl, false, nil); !strings.Contains(skip, "no ComicInfo") {
			t.Errorf("untagged skip=%q", skip)
		}
		if got, skip, err := renameOne(d, tmpl, false, nil); got != d || skip != "" || err != nil {
			t.Errorf("already-named file: %q %q %v", got, skip, err)
		}
		var names []string
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			names = append(names, e.Name())
		}
		want := []string{"Saga (2012) #001.cbz", "Saga (2012) #002.cbz", "saga 1 copy.cbz", "untagged.cbz"}
		if !slices.Equal(names, want) || filepath.Base(np) != want[0] {
			t.Errorf("dir = %v", names)
		}
	})
}

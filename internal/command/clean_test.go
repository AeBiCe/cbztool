package command

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"

	"codeberg.org/Nikode/cbztool/internal/archive"
)

func entryNames(t *testing.T, path string) []string {
	t.Helper()
	a, err := archive.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var names []string
	for _, f := range a.Files() {
		names = append(names, f.Name)
	}
	return names
}

func TestClean(t *testing.T) {
	page := []byte("page bytes")
	build := func(t *testing.T) string {
		out := filepath.Join(t.TempDir(), "book.cbz")
		err := archive.BuildArchive(out, map[string][]byte{
			"001.jpg":            page,
			"ComicInfo.xml":      []byte("<ComicInfo/>"),
			"__MACOSX/._001.jpg": []byte("fork"),
			".DS_Store":          []byte("ds"),
			"release.nfo":        []byte("nfo"),
		}, false)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("junk only", func(t *testing.T) {
		out := build(t)
		if err := cleanOne(out, false, false); err != nil {
			t.Fatal(err)
		}
		if got := entryNames(t, out); !slices.Equal(got, []string{"001.jpg", "ComicInfo.xml", "release.nfo"}) {
			t.Errorf("entries = %v", got)
		}
		// Kept entries are copied byte-for-byte.
		a, _ := archive.Open(out)
		defer a.Close()
		rc, err := a.Files()[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		if !bytes.Equal(b.Bytes(), page) {
			t.Errorf("page changed: %q", b.Bytes())
		}
	})
	t.Run("with extra", func(t *testing.T) {
		out := build(t)
		if err := cleanOne(out, true, false); err != nil {
			t.Fatal(err)
		}
		if got := entryNames(t, out); !slices.Equal(got, []string{"001.jpg", "ComicInfo.xml"}) {
			t.Errorf("entries = %v", got)
		}
	})
	t.Run("dry run changes nothing", func(t *testing.T) {
		out := build(t)
		before := entryNames(t, out)
		if err := cleanOne(out, true, true); err != nil {
			t.Fatal(err)
		}
		if got := entryNames(t, out); !slices.Equal(got, before) {
			t.Errorf("dry run changed entries: %v", got)
		}
	})
}

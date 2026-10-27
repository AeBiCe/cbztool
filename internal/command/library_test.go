package command

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/comicinfo"
)

// captureStdout returns what f prints to stdout.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	os.Stdout = old
	w.Close()
	return <-done
}

// library builds:
//
//	root/a.cbz           pages one,two, tagged "Saga"
//	root/sub/b.cbz       same pages (a duplicate), untagged, with junk
//	root/sub/deep/c.cbz  different pages, untagged "Paper Girls 001.cbz"
//	root/sub/loop -> root (a symlink loop the walk must not follow)
func library(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel string, entries map[string][]byte) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := archive.BuildArchive(p, entries, false); err != nil {
			t.Fatal(err)
		}
	}
	ci, _ := (&comicinfo.ComicInfo{Series: "Saga", Number: "1"}).Marshal()
	mk("a.cbz", map[string][]byte{"001.jpg": []byte("one"), "002.jpg": []byte("two"), archive.ComicInfoName: ci})
	mk("sub/b.cbz", map[string][]byte{"p1.jpg": []byte("one"), "p2.jpg": []byte("two"), "Thumbs.db": []byte("x")})
	mk("sub/deep/Paper Girls 001.cbz", map[string][]byte{"001.jpg": []byte("three"), "003.jpg": []byte("four")})
	if err := os.Symlink(root, filepath.Join(root, "sub", "loop")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLibraryFiles(t *testing.T) {
	root := library(t)
	flat, err := libraryFiles(root, false)
	if err != nil || len(flat) != 1 {
		t.Errorf("non-recursive = %v, %v", flat, err)
	}
	all, err := libraryFiles(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, p := range all {
		r, _ := filepath.Rel(root, p)
		rel = append(rel, r)
	}
	if want := []string{"a.cbz", "sub/b.cbz", "sub/deep/Paper Girls 001.cbz"}; !slices.Equal(rel, want) {
		t.Errorf("recursive = %v, want %v", rel, want)
	}
	if _, err := libraryFiles(t.TempDir(), true); err == nil {
		t.Error("an empty tree should be an error")
	}
}

func TestDupes(t *testing.T) {
	root := library(t)
	out := captureStdout(t, func() {
		if err := Dupes([]string{"-r", root}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "2 identical archives (2 pages)") ||
		!strings.Contains(out, "a.cbz  (") || !strings.Contains(out, "b.cbz  (") ||
		strings.Contains(out, "Paper Girls") || !strings.Contains(out, "1 set(s) of duplicates among 3") {
		t.Errorf("output:\n%s", out)
	}
	out = captureStdout(t, func() { _ = Dupes([]string{root}) })
	if !strings.Contains(out, "no duplicates among 1") {
		t.Errorf("non-recursive output:\n%s", out)
	}
}

func TestStats(t *testing.T) {
	root := library(t)
	out := captureStdout(t, func() {
		if err := Stats([]string{"-r", "-v", root}); err != nil {
			t.Error(err)
		}
	})
	for _, want := range []string{
		"archives     3", "pages        6 (2 per archive on average)", "tagged       1 (33%)",
		"untagged     2",
		"series       3",      // Saga (ComicInfo), "b" and "Paper Girls" (from the file names)
		"numbering    1 with", // only Paper Girls (001, 003: page 2 missing)
		"OS junk      1 with",
		"untagged:\n", "Paper Girls 001.cbz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 5 << 30: "5.0 GiB"}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

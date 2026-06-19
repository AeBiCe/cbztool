package archive

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"
)

func TestLastNumber(t *testing.T) {
	cases := []struct {
		in      string
		wantN   int
		wantRaw string
		wantOK  bool
	}{
		{"squirrel-man 3", 3, "3", true},
		{"Saga 012", 12, "012", true},
		{"chapter-7", 7, "7", true},
		{"cover", 0, "", false},
	}
	for _, c := range cases {
		n, raw, ok := LastNumber(c.in)
		if n != c.wantN || raw != c.wantRaw || ok != c.wantOK {
			t.Errorf("LastNumber(%q) = (%d, %q, %v), want (%d, %q, %v)",
				c.in, n, raw, ok, c.wantN, c.wantRaw, c.wantOK)
		}
	}
}

func TestSafeName(t *testing.T) {
	bad := []string{"/etc/passwd", "../escape.jpg", "a/../../b", `a\b.jpg`, ""}
	for _, n := range bad {
		if err := safeName(n); err == nil {
			t.Errorf("safeName(%q) = nil, want error", n)
		}
	}
	good := []string{"001.jpg", "pages/001.jpg", "ComicInfo.xml"}
	for _, n := range good {
		if err := safeName(n); err != nil {
			t.Errorf("safeName(%q) = %v, want nil", n, err)
		}
	}
}

// TestBuildArchiveRoundTrip packs a few entries and reads them back, checking
// byte fidelity, page counting, and the no-overwrite guard.
func TestBuildArchiveRoundTrip(t *testing.T) {
	out := filepath.Join(t.TempDir(), "book.cbz")
	entries := map[string][]byte{
		"002.jpg":     []byte("two"),
		"001.jpg":     []byte("one"),
		ComicInfoName: []byte("<ComicInfo/>"),
	}
	if err := BuildArchive(out, entries, false); err != nil {
		t.Fatalf("BuildArchive: %v", err)
	}

	a, err := Open(out)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()

	if got := a.PageCount(); got != 2 {
		t.Errorf("PageCount = %d, want 2", got)
	}
	got := make(map[string][]byte)
	for _, f := range a.Files() {
		rc, oerr := f.Open()
		if oerr != nil {
			t.Fatalf("open entry %q: %v", f.Name, oerr)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = data
	}
	for name, want := range entries {
		if !bytes.Equal(got[name], want) {
			t.Errorf("entry %q = %q, want %q", name, got[name], want)
		}
	}

	// Refuse to overwrite without force; succeed with it.
	if err := BuildArchive(out, entries, false); err == nil {
		t.Error("BuildArchive overwrote an existing file without --force")
	}
	if err := BuildArchive(out, entries, true); err != nil {
		t.Errorf("BuildArchive --force: %v", err)
	}
}

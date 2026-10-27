package archive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// deflated writes entries (in the given order) with Deflate compression, to
// prove the fingerprint ignores compression and entry order.
func deflated(t *testing.T, path string, entries [][2]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e[0], Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func fingerprint(t *testing.T, path string) string {
	t.Helper()
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return a.Fingerprint()
}

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.cbz")
	if err := BuildArchive(orig, map[string][]byte{"001.jpg": []byte("one"), "002.jpg": []byte("two")}, false); err != nil {
		t.Fatal(err)
	}
	// Same pages: renamed entries, reversed order, Deflate, plus metadata and junk.
	repack := filepath.Join(dir, "repack.cbz")
	deflated(t, repack, [][2]string{
		{"pages/b.png", "two"}, {"pages/a.png", "one"},
		{"ComicInfo.xml", "<ComicInfo/>"}, {".DS_Store", "x"}, {"__MACOSX/._a.png", "fork"},
	})
	other := filepath.Join(dir, "other.cbz")
	if err := BuildArchive(other, map[string][]byte{"001.jpg": []byte("one"), "002.jpg": []byte("TWO")}, false); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.cbz")
	if err := BuildArchive(empty, map[string][]byte{"notes.txt": []byte("no pages")}, false); err != nil {
		t.Fatal(err)
	}

	if a, b := fingerprint(t, orig), fingerprint(t, repack); a == "" || a != b {
		t.Errorf("repacked copy differs: %q vs %q", a, b)
	}
	if fingerprint(t, orig) == fingerprint(t, other) {
		t.Error("a changed page kept the same fingerprint")
	}
	if fp := fingerprint(t, empty); fp != "" {
		t.Errorf("no pages: fingerprint %q, want empty", fp)
	}
}

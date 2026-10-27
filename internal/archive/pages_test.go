package archive

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := map[string]Kind{
		"001.jpg":                 KindPage,
		"pages/002.PNG":           KindPage,
		"ComicInfo.xml":           KindComicInfo,
		"comicinfo.xml":           KindComicInfo,
		"__MACOSX/":               KindJunk,
		"__MACOSX/._001.jpg":      KindJunk,
		"pages/._003.jpg":         KindJunk,
		".DS_Store":               KindJunk,
		"pages/Thumbs.db":         KindJunk,
		"Desktop.ini":             KindJunk,
		"pages/":                  KindDir,
		"release.nfo":             KindExtra,
		"pages/ComicInfo.xml":     KindExtra, // only the root one is metadata
		"scanner-credits.txt":     KindExtra,
		"pages/__macosx_note.txt": KindExtra,
	}
	for name, want := range cases {
		if got := Classify(name); got != want {
			t.Errorf("Classify(%q) = %d, want %d", name, got, want)
		}
	}
}

// pngBytes encodes a w×h PNG.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPages(t *testing.T) {
	out := filepath.Join(t.TempDir(), "book.cbz")
	err := BuildArchive(out, map[string][]byte{
		"001.png":            pngBytes(t, 7, 5),
		"002.jpg":            []byte("not really a jpeg"),
		"003.png":            pngBytes(t, 10, 4),
		"__MACOSX/._001.png": []byte("resource fork"),
		"notes.txt":          []byte("hi"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	pages := a.Pages(false)
	if len(pages) != 3 || a.PageCount() != 3 {
		t.Fatalf("pages = %+v (junk must not count as a page)", pages)
	}
	if p := pages[0]; p.Name != "001.png" || p.Err != nil || p.Width != 7 || p.Height != 5 || p.Format != "png" || p.Size == 0 {
		t.Errorf("page 1 = %+v", p)
	}
	if pages[1].Err == nil {
		t.Errorf("garbage page decoded: %+v", pages[1])
	}
	if p := pages[2]; p.Err != nil || p.Width != 10 || p.Height != 4 {
		t.Errorf("page 3 = %+v", p)
	}
}

// TestPagesFullChecksum corrupts a byte past a page's header: the header
// still decodes, so only the full read (CRC-32 check) catches it.
func TestPagesFullChecksum(t *testing.T) {
	out := filepath.Join(t.TempDir(), "book.cbz")
	if err := BuildArchive(out, map[string][]byte{"001.png": pngBytes(t, 64, 64)}, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	f := zr.File[0]
	off, err := f.DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	data[off+int64(f.UncompressedSize64)-6] ^= 0xFF // inside the trailing IEND chunk
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if p := a.Pages(false)[0]; p.Err != nil {
		t.Errorf("header-only read failed: %v", p.Err)
	}
	if p := a.Pages(true)[0]; !errors.Is(p.Err, zip.ErrChecksum) {
		t.Errorf("full read err = %v, want zip.ErrChecksum", p.Err)
	}
}

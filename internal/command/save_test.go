package command

import (
	"bytes"
	"image"
	"image/png"
	"path/filepath"
	"testing"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
)

func pngPage(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestSaveComicInfoPages checks that every write fills PageCount and <Pages>
// from the archive, and that a later write (e.g. a re-tag, whose ComicInfo
// carries no <Pages>) keeps page types someone set by hand.
func TestSaveComicInfoPages(t *testing.T) {
	out := filepath.Join(t.TempDir(), "book.cbz")
	err := archive.BuildArchive(out, map[string][]byte{
		"001.png":    pngPage(t, 6, 9),
		"002.png":    pngPage(t, 12, 9),
		"003.png":    pngPage(t, 6, 9),
		"._junk.png": []byte("x"),
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := saveComicInfo(out, &comicinfo.ComicInfo{Series: "Saga", PageCount: 99}); err != nil {
		t.Fatal(err)
	}
	ci := readCI(t, out)
	if ci.PageCount != 3 || ci.Pages == nil || len(ci.Pages.Page) != 3 {
		t.Fatalf("PageCount=%d Pages=%+v", ci.PageCount, ci.Pages)
	}
	p := ci.Pages.Page
	if p[0].Type != "FrontCover" || p[0].ImageWidth != 6 || p[0].ImageHeight != 9 || p[0].DoublePage ||
		!p[1].DoublePage || p[1].ImageWidth != 12 || p[2].ImageSize == 0 {
		t.Errorf("pages = %+v", p)
	}

	// Hand-edit the types, then re-save fresh metadata with no <Pages>.
	ci.Pages.Page[0].Type, ci.Pages.Page[2].Type = "InnerCover", "BackCover"
	if err := saveComicInfo(out, ci); err != nil {
		t.Fatal(err)
	}
	if err := saveComicInfo(out, &comicinfo.ComicInfo{Series: "Saga", Title: "Retagged"}); err != nil {
		t.Fatal(err)
	}
	ci = readCI(t, out)
	if ci.Title != "Retagged" || ci.Pages.Page[0].Type != "InnerCover" || ci.Pages.Page[2].Type != "BackCover" {
		t.Errorf("hand-set types lost: %+v", ci.Pages.Page)
	}
}

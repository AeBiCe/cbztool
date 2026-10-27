package archive

import (
	"errors"
	"reflect"
	"testing"

	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/imagemeta"
)

func TestComicPages(t *testing.T) {
	pages := []Page{
		{Name: "001.jpg", Size: 100, Info: imagemeta.Info{Format: "jpeg", Width: 800, Height: 1200}},
		{Name: "002.jpg", Size: 200, Info: imagemeta.Info{Format: "jpeg", Width: 1600, Height: 1200}}, // spread
		{Name: "003.jpg", Size: 300, Err: errors.New("unreadable")},
	}
	fresh := []comicinfo.PageInfo{
		{Image: 0, Type: "FrontCover", ImageSize: 100, ImageWidth: 800, ImageHeight: 1200},
		{Image: 1, DoublePage: true, ImageSize: 200, ImageWidth: 1600, ImageHeight: 1200},
		{Image: 2, ImageSize: 300}, // no dimensions for an unreadable page
	}

	cases := []struct {
		name     string
		existing *comicinfo.Pages
		want     []comicinfo.PageInfo
	}{
		{"no existing pages", nil, fresh},
		{"existing with a different count is rebuilt",
			&comicinfo.Pages{Page: []comicinfo.PageInfo{{Image: 0, Type: "Story"}}},
			fresh},
		{"same count keeps hand-set Type/Bookmark/Key, without a FrontCover default",
			&comicinfo.Pages{Page: []comicinfo.PageInfo{
				{Image: 0, Type: "InnerCover", ImageWidth: 1}, // stale size: recomputed
				{Image: 1, Type: "FrontCover", Bookmark: "Start"},
				{Image: 2, Key: "k"},
			}},
			[]comicinfo.PageInfo{
				{Image: 0, Type: "InnerCover", ImageSize: 100, ImageWidth: 800, ImageHeight: 1200},
				{Image: 1, Type: "FrontCover", Bookmark: "Start", DoublePage: true, ImageSize: 200, ImageWidth: 1600, ImageHeight: 1200},
				{Image: 2, Key: "k", ImageSize: 300},
			}},
		{"same count with no types still gets the FrontCover default",
			&comicinfo.Pages{Page: []comicinfo.PageInfo{{Image: 0}, {Image: 1, Bookmark: "b"}, {Image: 2}}},
			[]comicinfo.PageInfo{fresh[0], {Image: 1, Bookmark: "b", DoublePage: true, ImageSize: 200, ImageWidth: 1600, ImageHeight: 1200}, fresh[2]}},
	}
	for _, c := range cases {
		got := ComicPages(pages, c.existing)
		if got == nil || !reflect.DeepEqual(got.Page, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	if ComicPages(nil, nil) != nil {
		t.Error("no pages should give nil <Pages>")
	}
}

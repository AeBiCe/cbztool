package archive

import "codeberg.org/Nikode/cbztool/internal/comicinfo"

// ComicPages builds the ComicInfo <Pages> list for pages (in reading order,
// as returned by Archive.Pages). Image index, size and dimensions come from
// the archive; DoublePage is set for a page wider than it is tall, and the
// first page is marked FrontCover.
//
// Hand-set values survive a rewrite: when existing describes the same number
// of pages, each page keeps its Type, Bookmark and Key from existing (and if
// existing set any Type at all, no FrontCover default is added, so a cover
// someone moved to page 2 stays there). With a different page count the old
// list no longer lines up with the images, so it is rebuilt from scratch.
// It returns nil when there are no pages.
func ComicPages(pages []Page, existing *comicinfo.Pages) *comicinfo.Pages {
	if len(pages) == 0 {
		return nil
	}
	var old []comicinfo.PageInfo
	if existing != nil && len(existing.Page) == len(pages) {
		old = existing.Page
	}
	typed := false
	for _, p := range old {
		typed = typed || p.Type != ""
	}

	out := make([]comicinfo.PageInfo, len(pages))
	for i, p := range pages {
		pi := comicinfo.PageInfo{Image: i, ImageSize: p.Size}
		if p.Err == nil {
			pi.ImageWidth, pi.ImageHeight = p.Width, p.Height
			pi.DoublePage = p.Width > p.Height
		}
		if old != nil {
			pi.Type, pi.Bookmark, pi.Key = old[i].Type, old[i].Bookmark, old[i].Key
		}
		if i == 0 && !typed {
			pi.Type = "FrontCover"
		}
		out[i] = pi
	}
	return &comicinfo.Pages{Page: out}
}

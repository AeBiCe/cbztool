package archive

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"
)

// Fingerprint identifies an archive's page content without decompressing
// anything: a SHA-256 over the sorted (CRC-32, size) pairs of its page
// entries, taken from the ZIP directory. Archives whose pages are
// byte-identical share a fingerprint whatever their file names, entry names,
// entry order, compression, metadata or junk. It is "" when there are no
// pages.
func (a *Archive) Fingerprint() string {
	files := a.pageFiles()
	if len(files) == 0 {
		return ""
	}
	type page struct {
		crc  uint32
		size uint64
	}
	pages := make([]page, len(files))
	for i, f := range files {
		pages[i] = page{f.CRC32, f.UncompressedSize64}
	}
	slices.SortFunc(pages, func(x, y page) int {
		return cmp.Or(cmp.Compare(x.crc, y.crc), cmp.Compare(x.size, y.size))
	})
	h := sha256.New()
	var buf [12]byte
	for _, p := range pages {
		binary.BigEndian.PutUint32(buf[:4], p.crc)
		binary.BigEndian.PutUint64(buf[4:], p.size)
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HasComicInfo reports whether the archive has a ComicInfo.xml entry, from
// the ZIP directory alone (the entry isn't read or parsed).
func (a *Archive) HasComicInfo() bool {
	for _, f := range a.Files() {
		if strings.EqualFold(f.Name, ComicInfoName) {
			return true
		}
	}
	return false
}

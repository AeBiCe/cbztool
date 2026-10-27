// Package archive opens, validates, reads, and rewrites .cbz (ZIP) comic
// archives. It enforces cbz's security invariants — zip-slip rejection and
// decompression-bomb caps — and performs all edits atomically (temp file +
// rename) so an interrupted run never corrupts an archive.
package archive

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/imagemeta"
)

// ComicInfoName is the canonical metadata filename inside a .cbz archive.
const ComicInfoName = "ComicInfo.xml"

// Guards against malformed or hostile archives (zip bombs). Header sizes can
// lie, so these are upper bounds enforced both at open time (declared sizes)
// and again with a limited reader when an entry is actually read. They are
// exported so the pack path can apply the same caps to loose input files.
const (
	MaxEntries   = 10_000  // refuse archives with absurd entry counts
	MaxEntrySize = 1 << 30 // 1 GiB per entry
	MaxTotalSize = 4 << 30 // 4 GiB declared total

	// maxComicInfoSize caps the one entry cbz actually decompresses and parses.
	// A metadata XML file is a few KiB even with per-page data, so 16 MiB is a
	// generous ceiling that still bounds the memory a hostile archive can force.
	maxComicInfoSize = 16 << 20
)

// imageExts lists the page-image extensions cbz counts as pages.
var imageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
	".avif": true,
}

// IsImage reports whether name has a known page-image extension.
func IsImage(name string) bool {
	return imageExts[strings.ToLower(path.Ext(name))]
}

// junkNames are operating-system metadata files that end up in archives
// zipped on macOS or browsed on Windows (compared case-insensitively).
var junkNames = map[string]bool{".ds_store": true, "thumbs.db": true, "desktop.ini": true}

// IsJunk reports whether name is operating-system cruft rather than content:
// anything under a __MACOSX/ folder, AppleDouble "._" files, and the files in
// junkNames. Junk is never a page, even with an image extension ("._001.jpg").
func IsJunk(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.EqualFold(part, "__MACOSX") {
			return true
		}
	}
	base := path.Base(name)
	return strings.HasPrefix(base, "._") || junkNames[strings.ToLower(base)]
}

// Kind classifies an archive entry by name.
type Kind int

const (
	KindPage      Kind = iota // a page image
	KindComicInfo             // ComicInfo.xml at the archive root
	KindJunk                  // operating-system cruft (IsJunk)
	KindDir                   // a directory entry
	KindExtra                 // anything else: .nfo, .txt, scanner credits, …
)

// Classify reports what kind of entry name is.
func Classify(name string) Kind {
	switch {
	case IsJunk(name):
		return KindJunk
	case strings.HasSuffix(name, "/"):
		return KindDir
	case strings.EqualFold(name, ComicInfoName):
		return KindComicInfo
	case IsImage(name):
		return KindPage
	}
	return KindExtra
}

// safeName rejects zip entry names that are absolute, contain Windows
// separators, or escape the archive root via "..". This is the zip-slip guard;
// every entry is validated before its name is trusted for any filesystem use.
func safeName(name string) error {
	if name == "" {
		return fmt.Errorf("empty entry name")
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("backslash in entry name: %q", name)
	}
	if path.IsAbs(name) {
		return fmt.Errorf("absolute entry name: %q", name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("entry escapes archive root: %q", name)
	}
	return nil
}

// Archive is an opened, validated .cbz archive. Close must be called.
type Archive struct {
	Path string
	zr   *zip.ReadCloser
}

// Open opens path as a ZIP archive and validates every entry against the
// zip-slip and size guards before returning.
func Open(path string) (*Archive, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	if len(zr.File) > MaxEntries {
		zr.Close()
		return nil, fmt.Errorf("archive has too many entries (%d > %d)", len(zr.File), MaxEntries)
	}
	var total uint64
	for _, f := range zr.File {
		if err := safeName(f.Name); err != nil {
			zr.Close()
			return nil, err
		}
		if f.UncompressedSize64 > MaxEntrySize {
			zr.Close()
			return nil, fmt.Errorf("entry %q declares %d bytes (> %d)", f.Name, f.UncompressedSize64, MaxEntrySize)
		}
		total += f.UncompressedSize64
		if total > MaxTotalSize {
			zr.Close()
			return nil, fmt.Errorf("archive declares > %d bytes uncompressed", MaxTotalSize)
		}
	}
	return &Archive{Path: path, zr: zr}, nil
}

// Close releases the underlying archive reader.
func (a *Archive) Close() error { return a.zr.Close() }

// Files returns the archive's entries.
func (a *Archive) Files() []*zip.File { return a.zr.File }

// pageFiles returns the page image entries (Classify == KindPage), sorted by
// name.
func (a *Archive) pageFiles() []*zip.File {
	var pages []*zip.File
	for _, f := range a.Files() {
		if !f.FileInfo().IsDir() && Classify(f.Name) == KindPage {
			pages = append(pages, f)
		}
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Name < pages[j].Name })
	return pages
}

// ImageEntries returns the names of image (page) entries, sorted by name.
// OS junk with an image extension ("__MACOSX/._001.jpg") is not a page.
func (a *Archive) ImageEntries() []string {
	var names []string
	for _, f := range a.pageFiles() {
		names = append(names, f.Name)
	}
	return names
}

// Page is one page image entry and what its header says.
type Page struct {
	Name string
	Size int64 // uncompressed bytes
	imagemeta.Info
	Err error // the entry is unreadable or not a recognised image
}

// Pages reads the header of every page image, in ImageEntries order. With full
// set, each entry is also read to its end so archive/zip verifies the CRC-32
// (a mismatch surfaces as zip.ErrChecksum in Err). Reads stay within the
// per-entry size cap.
func (a *Archive) Pages(full bool) []Page {
	files := a.pageFiles()
	pages := make([]Page, len(files))
	for i, f := range files {
		pages[i] = Page{Name: f.Name, Size: int64(f.UncompressedSize64)}
		pages[i].Info, pages[i].Err = readPage(f, full)
	}
	return pages
}

// readPage decodes f's image header and, when full, drains the rest of it.
func readPage(f *zip.File, full bool) (imagemeta.Info, error) {
	rc, err := f.Open()
	if err != nil {
		return imagemeta.Info{}, err
	}
	defer rc.Close()
	br := bufio.NewReader(io.LimitReader(rc, MaxEntrySize))
	info, err := imagemeta.DecodeReader(br)
	if err != nil {
		return imagemeta.Info{}, err
	}
	if full {
		if _, err := io.Copy(io.Discard, br); err != nil {
			return imagemeta.Info{}, err
		}
	}
	return info, nil
}

// PageCount is the number of image entries, used to fill ComicInfo PageCount.
func (a *Archive) PageCount() int { return len(a.ImageEntries()) }

// ReadComicInfo locates and parses ComicInfo.xml in the archive. found reports
// whether the entry exists; data holds its raw bytes when it does.
func (a *Archive) ReadComicInfo() (ci *comicinfo.ComicInfo, data []byte, found bool, err error) {
	for _, f := range a.Files() {
		if !strings.EqualFold(f.Name, ComicInfoName) {
			continue
		}
		rc, oerr := f.Open()
		if oerr != nil {
			return nil, nil, true, oerr
		}
		data, err = io.ReadAll(io.LimitReader(rc, maxComicInfoSize))
		rc.Close()
		if err != nil {
			return nil, nil, true, err
		}
		ci, err = comicinfo.Parse(data)
		return ci, data, true, err
	}
	return nil, nil, false, nil
}

// LastNumber returns the final run of digits in s as both an int (leading zeros
// stripped) and its raw text. ok is false when s contains no digits.
//
// Note: this grabs the *last* numeric run, so cruft-laden filenames such as
// "Batman #5 (2016)" resolve to 2016. For messy names prefer filename.Parse;
// this is the simple page-numbering helper used on already-clean entry names.
func LastNumber(s string) (n int, raw string, ok bool) {
	end := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] >= '0' && s[i] <= '9' {
			end = i
			break
		}
	}
	if end == -1 {
		return 0, "", false
	}
	start := end
	for start > 0 && s[start-1] >= '0' && s[start-1] <= '9' {
		start--
	}
	raw = s[start : end+1]
	n, _ = strconv.Atoi(raw)
	return n, raw, true
}

// RewriteArchive rebuilds the .cbz at path: every existing entry for which keep
// returns true is copied verbatim, then each entry in add (name -> content) is
// written, replacing any kept entry of the same name. The result is written to a
// temp file in the same directory and atomically renamed over the original, so a
// crash or error never leaves the original half-written. add may be nil.
//
// This is the shared write path for strip (keep filters out ComicInfo.xml) and
// tag (add carries a new ComicInfo.xml). A symlink is followed: the file it
// points to is rewritten and the link itself is left in place (see realTarget).
func RewriteArchive(path string, keep func(name string) bool, add map[string][]byte) error {
	path, err := realTarget(path)
	if err != nil {
		return err
	}
	r, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()

	// Defensive: never propagate an unsafe entry name into the rewritten file.
	for _, f := range r.File {
		if err := safeName(f.Name); err != nil {
			return err
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".cbz-*.cbz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	zw := zip.NewWriter(tmp)
	for _, f := range r.File {
		if _, replacing := add[f.Name]; replacing {
			continue // a replacement is supplied in add; skip the old entry
		}
		if !keep(f.Name) {
			continue
		}
		if err := copyRaw(zw, f); err != nil {
			return err
		}
	}
	for _, name := range sortedKeysBytes(add) {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(add[name]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// Preserve the original file's permissions on the replacement.
	if info, serr := os.Stat(path); serr == nil {
		_ = os.Chmod(tmpName, info.Mode())
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// BuildArchive creates a new .cbz at outPath containing entries (name ->
// content), written in lexical name order so page order is deterministic. It
// refuses to overwrite an existing file unless force is set, applies the same
// zip-slip and size guards as Open, stores entries without recompression (comic
// pages are already-compressed images), and writes atomically (temp + rename).
func BuildArchive(outPath string, entries map[string][]byte, force bool) error {
	if !force {
		if _, err := os.Stat(outPath); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if _, err := os.Lstat(outPath); err == nil {
		// Overwriting (force): replace what a symlink points to, not the link.
		if outPath, err = realTarget(outPath); err != nil {
			return err
		}
	}
	if len(entries) == 0 {
		return fmt.Errorf("no entries to pack")
	}
	if len(entries) > MaxEntries {
		return fmt.Errorf("too many entries (%d > %d)", len(entries), MaxEntries)
	}
	var total uint64
	for name, data := range entries {
		if err := safeName(name); err != nil {
			return err
		}
		if uint64(len(data)) > MaxEntrySize {
			return fmt.Errorf("entry %q is %d bytes (> %d)", name, len(data), MaxEntrySize)
		}
		total += uint64(len(data))
		if total > MaxTotalSize {
			return fmt.Errorf("entries exceed %d bytes total", MaxTotalSize)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".cbz-*.cbz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	zw := zip.NewWriter(tmp)
	for _, name := range sortedKeysBytes(entries) {
		// Store (no Deflate): comic pages are already-compressed images, so
		// recompressing only burns CPU for no size win.
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := w.Write(entries[name]); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// CreateTemp makes the file 0600; a new archive should be readable like any
	// other file the user creates (e.g. by a comic server running as another
	// user), and an overwritten one keeps its existing permissions.
	mode := os.FileMode(0o644)
	if info, serr := os.Stat(outPath); serr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return err
	}
	committed = true
	return nil
}

// realTarget resolves the file an in-place edit must replace. The atomic write
// renames a temp file over its target, and renaming over a symlink would
// replace the link itself with a regular file — silently leaving the real
// archive unedited and the library's link layout broken. So a symlink is
// resolved (through any chain of links) to its target, which must be a regular
// file; the temp file is then created beside the target so the rename stays on
// one filesystem.
func realTarget(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	return real, nil
}

// copyRaw copies a zip entry into zw without recompressing, preserving the
// stored/compressed bytes and metadata exactly.
func copyRaw(zw *zip.Writer, f *zip.File) error {
	hdr := f.FileHeader
	w, err := zw.CreateRaw(&hdr)
	if err != nil {
		return err
	}
	rc, err := f.OpenRaw()
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rc)
	return err
}

// sortedKeysBytes returns the keys of m in ascending order for deterministic output.
func sortedKeysBytes(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

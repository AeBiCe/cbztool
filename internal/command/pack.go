package command

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/term"
)

// Pack implements `cbz pack [--force] [--tag] [--manual] <dir>`: build a new .cbz
// from the image files in a directory. The archive is named after the directory
// and written as a sibling. Pack is pure creation — it does not prompt — but
// --tag (-t) chains a `tag` afterward, and --manual (-m) makes that chained tag
// prompt instead of fetching online. The source directory is never modified.
func Pack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing output .cbz")
	tagAfter := fs.Bool("tag", false, "tag the new archive after packing")
	fs.BoolVar(tagAfter, "t", false, "shorthand for --tag")
	manual := fs.Bool("manual", false, "with --tag, prompt for fields instead of fetching online")
	fs.BoolVar(manual, "m", false, "shorthand for --manual")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cbz pack [--force] [--tag] [--manual] <dir>")
	}
	dir := fs.Arg(0)

	if *manual && !*tagAfter {
		return fmt.Errorf("pack: --manual only applies with --tag")
	}

	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("pack requires a directory of images, not a file: %s", dir)
	}

	entries, images, err := collectImages(dir)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return fmt.Errorf("no page images found in %s", dir)
	}

	out := filepath.Clean(dir) + ".cbz"
	if err := archive.BuildArchive(out, entries, *force); err != nil {
		return err
	}
	fmt.Printf("packed %d images -> %s\n", len(images), Sanitize(out))

	if warn := pageOrderWarning(images); warn != "" {
		term.Warnf("%s", warn)
	}

	// Advisory page-sequence check: report problems but never fail the pack.
	if _, verr := validateOne(out, false); verr != nil {
		term.Errorf("%s: %s", Sanitize(out), Sanitize(verr.Error()))
	}

	if !*tagAfter {
		return nil
	}
	if *manual {
		return tagManualSingle(out, true)
	}
	src, err := openProviders("", false, false)
	if err != nil {
		return err
	}
	return tagFileOnline(src, nil, out, "", false, true, false, false)
}

// collectImages reads the (non-recursive) image files in dir into an entry map
// keyed by base filename, plus the sorted list of image names. An existing
// ComicInfo.xml is carried over verbatim; dotfiles and non-images are ignored.
func collectImages(dir string) (entries map[string][]byte, images []string, err error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	entries = make(map[string][]byte)
	var total uint64
	// readEntry stats a file and enforces the archive caps *before* reading it
	// into memory, so a hostile directory of huge or too-many files can't make
	// pack exhaust RAM (BuildArchive re-checks the same caps as a backstop).
	readEntry := func(e os.DirEntry) ([]byte, error) {
		fi, ierr := e.Info()
		if ierr != nil {
			return nil, ierr
		}
		if fi.Size() > archive.MaxEntrySize {
			return nil, fmt.Errorf("%s is %d bytes (> %d)", Sanitize(e.Name()), fi.Size(), int64(archive.MaxEntrySize))
		}
		total += uint64(fi.Size())
		if total > archive.MaxTotalSize {
			return nil, fmt.Errorf("input files exceed %d bytes total", int64(archive.MaxTotalSize))
		}
		if len(entries) >= archive.MaxEntries {
			return nil, fmt.Errorf("too many files to pack (> %d)", archive.MaxEntries)
		}
		return os.ReadFile(filepath.Join(dir, e.Name()))
	}
	for _, e := range dirents {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		// Only pack regular files. Skip symlinks (and devices/sockets/pipes):
		// following a symlink would slurp the *target's* contents — e.g. a
		// "cover.jpg" -> /etc/passwd link — into the archive.
		if !e.Type().IsRegular() {
			term.Warnf("skipping non-regular file %s", Sanitize(name))
			continue
		}
		switch {
		case archive.IsImage(name):
			data, rerr := readEntry(e)
			if rerr != nil {
				return nil, nil, rerr
			}
			entries[name] = data
			images = append(images, name)
		case strings.EqualFold(name, archive.ComicInfoName):
			data, rerr := readEntry(e)
			if rerr != nil {
				return nil, nil, rerr
			}
			entries[archive.ComicInfoName] = data
		}
	}
	sort.Strings(images)
	return entries, images, nil
}

// pageOrderWarning returns a non-empty message when the images' lexical order
// (how they are stored, and how most readers display them) differs from their
// natural numeric order — the classic "2.jpg sorts after 10.jpg" trap from
// names that aren't zero-padded.
func pageOrderWarning(images []string) string {
	lexical := append([]string(nil), images...)
	sort.Strings(lexical)

	numeric := append([]string(nil), images...)
	sort.SliceStable(numeric, func(i, j int) bool {
		ni, _, oki := archive.LastNumber(numeric[i])
		nj, _, okj := archive.LastNumber(numeric[j])
		if oki && okj && ni != nj {
			return ni < nj
		}
		return numeric[i] < numeric[j]
	})

	for i := range lexical {
		if lexical[i] != numeric[i] {
			return "image filenames are not zero-padded; reading order may be wrong " +
				"(e.g. \"10\" before \"2\") — consider renaming to 001, 002, …"
		}
	}
	return ""
}

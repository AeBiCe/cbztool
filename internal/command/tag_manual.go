package command

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"codeberg.org/Nikode/cbztool/internal/archive"
	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/filename"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// TagManual implements `cbz tag -m`: write ComicInfo.xml from local input only,
// never contacting a provider. With neither -f nor -s it walks through prompts;
// -f applies a JSON metadata file and -s sets individual fields (applied last),
// both layered onto any existing ComicInfo.xml. -t writes a JSON template for -f
// instead of touching any archive.
func TagManual(args []string) error {
	fs := flag.NewFlagSet("tag -m", flag.ExitOnError)
	file := fs.String("file", "", "apply metadata from a JSON file (create one with -t)")
	fs.StringVar(file, "f", "", "shorthand for --file")
	var sets setFlags
	fs.Var(&sets, "set", "set one field as `Field=Value` (repeatable; applied after -f)")
	fs.Var(&sets, "s", "shorthand for --set")
	template := fs.String("template", "", "write a JSON template for -f to this `file` (prefilled from <path> when given)")
	fs.StringVar(template, "t", "", "shorthand for --template")
	dryRun := fs.Bool("dry-run", false, "with -f/-s: print the result without writing")
	force := fs.Bool("force", false, "prompts: overwrite an existing ComicInfo.xml; -t: overwrite an existing template")
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintln(out, "usage: cbz tag -m [--force] <path>                        prompt for fields")
		fmt.Fprintln(out, "       cbz tag -m [-f meta.json] [-s Field=Value]... [--dry-run] <path>")
		fmt.Fprintln(out, "       cbz tag -m -t meta.json [--force] [<path>]         write a template")
		fs.PrintDefaults()
		fmt.Fprintln(out, "\nfields (for -s and the JSON file; case-insensitive, empty values are skipped):")
		fmt.Fprintln(out, wrapList(comicinfo.Fields(), 76, "  "))
	}
	_ = fs.Parse(args)

	if *template != "" {
		if *file != "" || len(sets) > 0 || *dryRun {
			return fmt.Errorf("tag -m: -t only writes a template; it takes just --force and an optional <path>")
		}
		if fs.NArg() > 1 {
			fs.Usage()
			return fmt.Errorf("tag -m: -t takes at most one <path>")
		}
		return writeTemplate(*template, fs.Arg(0), *force)
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("tag -m: expected exactly one <path>")
	}
	target := fs.Arg(0)

	if *file != "" || len(sets) > 0 {
		if *force {
			return fmt.Errorf("tag -m: -f/-s layer onto an existing ComicInfo.xml; --force isn't needed")
		}
		return tagApply(*file, sets, target, *dryRun)
	}
	if *dryRun {
		return fmt.Errorf("tag -m: --dry-run applies to -f/-s")
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return tagManualSeries(target, *force)
	}
	return tagManualSingle(target, *force)
}

// setFlags collects repeated -s Field=Value arguments.
type setFlags []string

func (s *setFlags) String() string     { return strings.Join(*s, ", ") }
func (s *setFlags) Set(v string) error { *s = append(*s, v); return nil }

// parseSets turns -s Field=Value pairs into a ComicInfo, validating every field
// before any archive is touched. A later -s for the same field wins.
func parseSets(sets []string) (*comicinfo.ComicInfo, error) {
	ci := &comicinfo.ComicInfo{}
	for _, s := range sets {
		name, value, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("-s %q: expected Field=Value", s)
		}
		if err := ci.Set(name, value); err != nil {
			return nil, fmt.Errorf("-s: %w", err)
		}
	}
	return ci, nil
}

// wrapList joins items with ", ", wrapping lines at width with the given indent.
func wrapList(items []string, width int, indent string) string {
	var b strings.Builder
	line := indent
	for i, it := range items {
		if i < len(items)-1 {
			it += ","
		}
		if len(line)+len(it)+1 > width && line != indent {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			line = indent
		}
		line += it + " "
	}
	b.WriteString(strings.TrimRight(line, " "))
	return b.String()
}

// --- JSON metadata file (-f) ---

// maxMetaFileSize caps a -f metadata file; it is hand-written JSON, so anything
// larger is a mistake (or not the file the user meant).
const maxMetaFileSize = 4 << 20

// metaFile is a decoded `cbz tag -m -f` document: series-level fields applied to
// every archive, plus per-issue fields keyed by issue number ("1", "001", "1.5")
// or exact filename ("Saga 001.cbz").
type metaFile struct {
	Series  *comicinfo.ComicInfo
	Issues  map[string]*comicinfo.ComicInfo // canonical key (see metaKey)
	origKey map[string]string               // canonical key -> key as written
}

// rawMetaFile is the on-disk JSON shape. Field values are decoded loosely
// (string or number) and parsed by ComicInfo.Set, so a template's "" works for
// numeric fields too.
type rawMetaFile struct {
	Series map[string]any            `json:"series"`
	Issues map[string]map[string]any `json:"issues"`
}

// loadMetaFile reads and decodes a -f JSON file, size-capped.
func loadMetaFile(path string) (*metaFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxMetaFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxMetaFileSize {
		return nil, fmt.Errorf("%s is too large (over %d bytes)", path, maxMetaFileSize)
	}
	return parseMetaFile(path, data)
}

// parseMetaFile strictly decodes a metadata file: unknown top-level keys and
// field names are rejected (catching typos like "Sumary"), values are validated
// by ComicInfo.Set, and empty values are skipped.
func parseMetaFile(name string, data []byte) (*metaFile, error) {
	var raw rawMetaFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("parse %s: unexpected data after the JSON object", name)
	}
	if raw.Series == nil && raw.Issues == nil {
		return nil, fmt.Errorf("%s has no \"series\" or \"issues\" block", name)
	}

	m := &metaFile{Issues: map[string]*comicinfo.ComicInfo{}, origKey: map[string]string{}}
	var err error
	if m.Series, err = decodeBlock(name+": series", raw.Series); err != nil {
		return nil, err
	}
	for key, block := range raw.Issues {
		ci, err := decodeBlock(fmt.Sprintf("%s: issues[%q]", name, key), block)
		if err != nil {
			return nil, err
		}
		k := metaKey(key)
		if k == "" {
			return nil, fmt.Errorf("%s: issues has an empty key", name)
		}
		if prev, dup := m.origKey[k]; dup {
			return nil, fmt.Errorf("%s: issues[%q] and issues[%q] refer to the same issue", name, prev, key)
		}
		m.Issues[k] = ci
		m.origKey[k] = key
	}
	return m, nil
}

// decodeBlock sets each field of one JSON object onto a fresh ComicInfo.
func decodeBlock(label string, block map[string]any) (*comicinfo.ComicInfo, error) {
	ci := &comicinfo.ComicInfo{}
	names := make([]string, 0, len(block))
	for k := range block {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic error reporting
	seen := map[string]string{}
	for _, k := range names {
		canon, ok := comicinfo.CanonicalField(k)
		if !ok {
			return nil, fmt.Errorf("%s: unknown field %q", label, k)
		}
		if prev, dup := seen[canon]; dup {
			return nil, fmt.Errorf("%s: %q and %q are the same field", label, prev, k)
		}
		seen[canon] = k
		var v string
		switch x := block[k].(type) {
		case nil:
			continue
		case string:
			v = x
		case json.Number:
			v = x.String()
		default:
			return nil, fmt.Errorf("%s: %s must be a string or number", label, canon)
		}
		if err := ci.Set(canon, v); err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	}
	return ci, nil
}

// metaKey canonicalizes an issues key: a .cbz filename is kept verbatim (exact
// match), anything else is treated as an issue number.
func metaKey(key string) string {
	key = strings.TrimSpace(key)
	if strings.EqualFold(filepath.Ext(key), ".cbz") {
		return key
	}
	return canonNumber(key)
}

// issueFor returns the issue entry for an archive (exact filename first, then
// its parsed issue number) and the canonical key it matched.
func (m *metaFile) issueFor(path string, parsed filename.ParsedName) (*comicinfo.ComicInfo, string) {
	if ci, ok := m.Issues[filepath.Base(path)]; ok {
		return ci, filepath.Base(path)
	}
	if k := canonNumber(parsed.Number); k != "" {
		if ci, ok := m.Issues[k]; ok {
			return ci, k
		}
	}
	return nil, ""
}

// buildManual layers the metadata for one archive: its existing ComicInfo (if
// any), then each layer in order (series block, issue entry, -s fields). Series
// and Number fall back to the filename when still unset; PageCount always comes
// from the archive.
func buildManual(existing *comicinfo.ComicInfo, parsed filename.ParsedName, pageCount int, layers ...*comicinfo.ComicInfo) *comicinfo.ComicInfo {
	ci := &comicinfo.ComicInfo{}
	ci.Overlay(existing)
	for _, l := range layers {
		ci.Overlay(l)
	}
	if ci.Series == "" {
		ci.Series = parsed.Series
	}
	if ci.Number == "" {
		ci.Number = parsed.Number
	}
	ci.PageCount = pageCount
	return ci
}

// tagApply applies a -f metadata file and/or -s fields to one .cbz or every .cbz
// in a directory. Everything is validated before the first archive is touched.
func tagApply(metaPath string, sets []string, target string, dryRun bool) error {
	m := &metaFile{}
	if metaPath != "" {
		var err error
		if m, err = loadMetaFile(metaPath); err != nil {
			return err
		}
	}
	setCI, err := parseSets(sets)
	if err != nil {
		return err
	}
	files, err := cbzFiles(target)
	if err != nil {
		return err
	}

	used := map[string]bool{}
	var b *batch // numbered progress + summary, only for more than one file
	if len(files) > 1 {
		b = newBatch(len(files), dryRun)
	}
	for _, f := range files {
		o := tagApplyOne(f, m, setCI, used, dryRun)
		if b != nil {
			b.record(f, o)
		} else if err := reportOne(f, o, dryRun); err != nil {
			return err
		}
	}

	var unused []string
	for k, orig := range m.origKey {
		if !used[k] {
			unused = append(unused, orig)
		}
	}
	sort.Strings(unused)
	for _, k := range unused {
		term.Warnf("issues[%q] in %s matched no archive", Sanitize(k), Sanitize(metaPath))
	}
	if b != nil {
		return b.done()
	}
	return nil
}

func tagApplyOne(path string, m *metaFile, setCI *comicinfo.ComicInfo, used map[string]bool, dryRun bool) fileOutcome {
	a, err := archive.Open(path)
	if err != nil {
		return fileOutcome{err: err}
	}
	existing, _, found, rerr := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if rerr != nil {
		return fileOutcome{err: rerr}
	}
	if !found {
		existing = nil
	}

	parsed := filename.Parse(path)
	issue, key := m.issueFor(path, parsed)
	if key != "" {
		used[key] = true
	}
	ci := buildManual(existing, parsed, pageCount, m.Series, issue, setCI)
	if dryRun {
		err = printDryRun(path, "manual input", ci)
	} else {
		err = saveComicInfo(path, ci)
	}
	if err != nil {
		return fileOutcome{err: err}
	}
	return fileOutcome{written: true}
}

// --- interactive prompts ---

// tagManualSingle prompts for every field and writes ComicInfo.xml into one
// archive. Series and Number are pre-filled from the filename so the common
// case is mostly Enter-presses.
func tagManualSingle(path string, force bool) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if found && !force {
		return fmt.Errorf("%s already has a ComicInfo.xml (use --force to overwrite)", path)
	}

	parsed := filename.Parse(path)
	p := newPrompter()

	ci := &comicinfo.ComicInfo{PageCount: pageCount}
	var perr error
	if ci.Title, perr = p.ask("Title"); perr != nil {
		return perr
	}
	if ci.Series, perr = p.askDefault("Series", parsed.Series); perr != nil {
		return perr
	}
	if ci.Number, perr = p.askDefault("Number", parsed.Number); perr != nil {
		return perr
	}
	for _, f := range []struct {
		label string
		dst   *string
	}{
		{"Writer", &ci.Writer},
		{"Genre", &ci.Genre},
		{"Tags", &ci.Tags},
		{"Characters", &ci.Characters},
	} {
		v, err := p.ask(f.label)
		if err != nil {
			return err
		}
		*f.dst = comicinfo.CleanCSV(v)
	}

	ar, err := p.askAgeRating()
	if err != nil {
		return err
	}
	ci.AgeRating = ar

	return writeComicInfo(path, ci)
}

// tagManualSeries prompts the series-level fields once and writes a
// ComicInfo.xml into every .cbz in the directory, parsing Number from each
// filename.
func tagManualSeries(dir string, force bool) error {
	files, err := cbzFiles(dir)
	if err != nil {
		return err
	}
	p := newPrompter()

	series, err := p.ask("Series (applies to all)")
	if err != nil {
		return err
	}
	writer, err := p.ask("Writer (applies to all)")
	if err != nil {
		return err
	}
	genre, err := p.ask("Genre (applies to all)")
	if err != nil {
		return err
	}
	ageRating, err := p.askAgeRating()
	if err != nil {
		return err
	}
	writer, genre = comicinfo.CleanCSV(writer), comicinfo.CleanCSV(genre)

	b := newBatch(len(files), false)
	for _, f := range files {
		b.record(f, tagSeriesOne(f, series, writer, genre, ageRating, force))
	}
	return b.done()
}

func tagSeriesOne(path, series, writer, genre, ageRating string, force bool) fileOutcome {
	a, err := archive.Open(path)
	if err != nil {
		return fileOutcome{err: err}
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if found && !force {
		return fileOutcome{skip: skipAlreadyTagged}
	}

	// Number = trailing digit run of the filename stem, leading zeros stripped.
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	number := ""
	var notes []note
	if n, _, ok := archive.LastNumber(stem); ok {
		number = strconv.Itoa(n)
	} else {
		notes = append(notes, note{true, "no number in filename; Number left empty"})
	}

	ci := &comicinfo.ComicInfo{
		Series:    series,
		Number:    number,
		Writer:    writer,
		Genre:     genre,
		AgeRating: ageRating,
		PageCount: pageCount,
		// Title omitted (per-issue); Tags/Characters omitted (fill later).
	}
	if err := saveComicInfo(path, ci); err != nil {
		return fileOutcome{err: err}
	}
	return fileOutcome{written: true, notes: notes}
}

package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cb/internal/archive"
	"cb/internal/comicinfo"
	"cb/internal/filename"
	"cb/internal/provider"
)

// Tag implements `cb tag [flags] <path>`: populate ComicInfo.xml, either from an
// online provider (default) or by prompting the user (--manual). A single .cbz
// is one comic; a directory is processed as a batch (online auto-match per file)
// or, with --manual, as one series (series-level fields prompted once).
func Tag(args []string) error {
	fs := flag.NewFlagSet("tag", flag.ExitOnError)
	id := fs.String("id", "", "tag a specific issue by provider issue ID (online)")
	dryRun := fs.Bool("dry-run", false, "print the resolved ComicInfo without writing (online)")
	force := fs.Bool("force", false, "overwrite an existing ComicInfo.xml")
	pick := fs.Bool("pick", false, "always show the series picker (online; skip auto-match)")
	manual := fs.Bool("manual", false, "prompt for fields instead of fetching online")
	fs.BoolVar(manual, "m", false, "shorthand for --manual")
	refresh := fs.Bool("refresh", false, "ignore cached API responses and refetch (online)")
	verbose := fs.Bool("verbose", false, "show parsed fields and every series candidate the provider returned")
	fs.BoolVar(verbose, "v", false, "shorthand for --verbose")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: cb tag [--manual] [--id <issueID>] [--dry-run] [--force] [--pick] [--refresh] [-v] <path>")
	}
	target := fs.Arg(0)

	if *manual {
		switch {
		case *id != "":
			return fmt.Errorf("tag: --manual and --id are mutually exclusive")
		case *pick:
			return fmt.Errorf("tag: --pick applies only to online tagging")
		case *dryRun:
			return fmt.Errorf("tag: --dry-run applies only to online tagging")
		case *refresh:
			return fmt.Errorf("tag: --refresh applies only to online tagging")
		}
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}

	if *manual {
		if info.IsDir() {
			return tagManualSeries(target, *force)
		}
		return tagManualSingle(target, *force)
	}

	prov, err := openProvider(*refresh)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return tagDirOnline(prov, target, *dryRun, *force, *id, *verbose)
	}
	return tagFileOnline(prov, target, *id, *dryRun, *force, *pick, *verbose)
}

// openProvider loads config and builds the configured online provider. When
// refresh is set and the provider keeps a persistent cache, cached reads are
// bypassed for this run (the cache is still repopulated with fresh data).
func openProvider(refresh bool) (provider.Provider, error) {
	cfg, err := provider.LoadConfig()
	if err != nil {
		return nil, err
	}
	prov, err := provider.New(cfg)
	if err != nil {
		return nil, err
	}
	if refresh {
		if rc, ok := prov.(provider.CacheControl); ok {
			rc.SetRefresh(true)
		}
	}
	return prov, nil
}

// --- online tagging (formerly `cb fetch`) ---

// tagFileOnline resolves and writes (or dry-run prints) metadata for one archive.
func tagFileOnline(prov provider.Provider, path, id string, dryRun, force, pick, verbose bool) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if found && !force && !dryRun {
		return fmt.Errorf("%s already has a ComicInfo.xml (use --force to overwrite)", path)
	}

	// Generous budget: a single resolution is up to 3 calls, and a throttled
	// call may back off and retry, so allow room beyond the happy path.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var ci *comicinfo.ComicInfo
	if id != "" {
		ci, err = prov.IssueByID(ctx, id)
	} else {
		ci, err = resolveFromFilename(ctx, prov, path, pick, verbose)
	}
	if err != nil {
		return err
	}
	ci.PageCount = pageCount // archive is authoritative for PageCount

	if dryRun {
		return printDryRun(path, prov.Name(), ci)
	}
	return writeComicInfo(path, ci)
}

// tagDirOnline processes every .cbz in a directory non-interactively: it
// auto-matches each file and skips (without prompting) anything it can't
// confidently resolve, so one ambiguous file never blocks the batch.
func tagDirOnline(prov provider.Provider, dir string, dryRun, force bool, id string, verbose bool) error {
	if id != "" {
		return fmt.Errorf("tag: --id cannot be combined with a directory")
	}
	files, err := cbzFiles(dir)
	if err != nil {
		return err
	}

	var resolved, skipped, failed int
	for _, f := range files {
		a, oerr := archive.Open(f)
		if oerr != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, oerr)
			failed++
			continue
		}
		_, _, found, _ := a.ReadComicInfo()
		pageCount := a.PageCount()
		a.Close()
		if found && !force && !dryRun {
			fmt.Printf("%s: already has ComicInfo.xml (skipped; use --force)\n", f)
			skipped++
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		ci, matched, rerr := autoResolve(ctx, prov, f, verbose)
		cancel()
		if rerr != nil {
			// A clean "no match" is a skip, not a failure — one missing issue
			// shouldn't count against the batch like a real (transport) error.
			// Throttling is handled inside the provider (it waits and retries),
			// so by the time an error reaches here we just record and move on.
			if errors.Is(rerr, provider.ErrNotFound) {
				fmt.Printf("%s: no matching issue found; skipped\n", f)
				skipped++
			} else {
				fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, rerr)
				failed++
			}
			continue
		}
		if !matched {
			fmt.Printf("%s: no confident auto-match; skipped (run 'cb tag' on it directly)\n", f)
			skipped++
			continue
		}
		ci.PageCount = pageCount

		if dryRun {
			if perr := printDryRun(f, prov.Name(), ci); perr != nil {
				fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, perr)
				failed++
				continue
			}
		} else if werr := writeComicInfo(f, ci); werr != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, werr)
			failed++
			continue
		}
		resolved++
	}
	fmt.Printf("done: %d resolved, %d skipped, %d failed\n", resolved, skipped, failed)
	return nil
}

// autoResolve does a no-prompt resolution for batch mode: it requires the
// filename to yield a series and number, and the series to auto-match uniquely.
// matched=false (with a nil error) means "couldn't confidently resolve" — the
// caller should skip rather than abort.
func autoResolve(ctx context.Context, prov provider.Provider, path string, verbose bool) (ci *comicinfo.ComicInfo, matched bool, err error) {
	parsed := filename.Parse(path)
	if parsed.Series == "" || parsed.Number == "" {
		if verbose {
			fmt.Printf("[verbose] %s\n[verbose]   parsed: series=%q number=%q — incomplete, cannot auto-match\n", path, parsed.Series, parsed.Number)
		}
		return nil, false, nil
	}
	results, err := prov.SearchSeries(ctx, parsed.Series)
	if err != nil {
		return nil, false, err
	}
	results = preferEdition(results, parsed.Series)
	s, ok := bestSeriesMatch(results, parsed.Series, parsed.Year)
	if verbose {
		var picked *provider.SeriesResult
		if ok {
			picked = &s
		}
		printSearchDiagnostics(path, parsed, results, picked)
	}
	if !ok {
		return nil, false, nil
	}
	ci, err = prov.IssueByNumber(ctx, s.ID, parsed.Number)
	if err != nil {
		return nil, false, err
	}
	return ci, true, nil
}

// printDryRun prints the resolved ComicInfo without writing it.
func printDryRun(path, providerName string, ci *comicinfo.ComicInfo) error {
	data, err := ci.Marshal()
	if err != nil {
		return err
	}
	fmt.Printf("%s: resolved via %s (dry-run, not written):\n", path, providerName)
	os.Stdout.Write(data)
	return nil
}

// resolveFromFilename parses the filename, searches the provider for the
// series, and resolves the issue by number. When the parsed series (and year,
// as a tiebreaker) uniquely identify a result, it is auto-selected with no
// prompt; if the issue number was also parsed, the whole tag is hands-off.
// Otherwise the user picks from the candidates. force (--pick) always prompts.
func resolveFromFilename(ctx context.Context, prov provider.Provider, path string, force, verbose bool) (*comicinfo.ComicInfo, error) {
	parsed := filename.Parse(path)
	p := newPrompter()

	name := parsed.Series
	if name == "" {
		q, err := p.ask("Series to search")
		if err != nil {
			return nil, err
		}
		name = q
	} else {
		fmt.Printf("Parsed from filename: series=%q number=%q year=%d\n", parsed.Series, parsed.Number, parsed.Year)
	}

	results, err := prov.SearchSeries(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no series found for %q", name)
	}
	results = preferEdition(results, name)

	if verbose {
		printSearchDiagnostics(path, parsed, results, nil)
	}

	if !force {
		if s, ok := bestSeriesMatch(results, name, parsed.Year); ok {
			fmt.Printf("Auto-matched series: %s id=%s\n", seriesLabel(s), s.ID)
			return resolveIssue(ctx, prov, p, s, parsed.Number)
		}
	}

	// Fall back to a picker, narrowed to exact-name matches when there are any.
	candidates := exactNameMatches(results, name)
	if len(candidates) == 0 {
		candidates = results
	}
	fmt.Println("Select a series:")
	for i, s := range candidates {
		fmt.Printf("  %2d) %s [%d issues] id=%s\n", i, seriesLabel(s), s.IssueCount, s.ID)
	}
	idx, err := p.askIndex("  choice [0]", len(candidates))
	if err != nil {
		return nil, err
	}
	return resolveIssue(ctx, prov, p, candidates[idx], parsed.Number)
}

// resolveIssue fetches the issue by number, prompting for the number only when
// it could not be parsed from the filename.
func resolveIssue(ctx context.Context, prov provider.Provider, p *prompter, series provider.SeriesResult, number string) (*comicinfo.ComicInfo, error) {
	if number == "" {
		ans, err := p.ask("Issue number")
		if err != nil {
			return nil, err
		}
		number = ans
	}
	if number == "" {
		return nil, fmt.Errorf("no issue number given")
	}
	return prov.IssueByNumber(ctx, series.ID, number)
}

// printSearchDiagnostics dumps, under -v/--verbose, what a filename parsed to
// and every series candidate the provider returned, marking the auto-matched
// one (if any) with "=>". It lets the user see why an auto-match did or did not
// happen — e.g. an ambiguous name, a year mismatch, or no exact-name match.
func printSearchDiagnostics(path string, parsed filename.ParsedName, results []provider.SeriesResult, picked *provider.SeriesResult) {
	fmt.Printf("[verbose] %s\n", path)
	fmt.Printf("[verbose]   parsed: series=%q number=%q year=%d vol=%d\n", parsed.Series, parsed.Number, parsed.Year, parsed.Volume)
	fmt.Printf("[verbose]   %d series candidate(s) returned for %q:\n", len(results), parsed.Series)
	for _, s := range results {
		mark := "  "
		if picked != nil && s.ID == picked.ID {
			mark = "=>"
		}
		fmt.Printf("[verbose]   %s %s [%d issues] id=%s\n", mark, seriesLabel(s), s.IssueCount, s.ID)
	}
	if picked == nil {
		fmt.Printf("[verbose]   no confident auto-match (need exactly one exact-name candidate%s)\n",
			yearHint(parsed.Year))
	}
}

// yearHint notes that the parsed year is used as a tiebreaker, for the verbose
// "no confident auto-match" explanation.
func yearHint(year int) string {
	if year > 0 {
		return fmt.Sprintf(", or one matching year %d", year)
	}
	return ""
}

// exactNameMatches returns the results whose series name (ignoring a trailing
// "(year)" in the display string) matches name once separator punctuation and
// case are normalized away (see matchKey).
func exactNameMatches(results []provider.SeriesResult, name string) []provider.SeriesResult {
	key := matchKey(name)
	var out []provider.SeriesResult
	for _, s := range results {
		if matchKey(stripYearSuffix(s.Name)) == key {
			out = append(out, s)
		}
	}
	return out
}

// matchKey reduces a series name to a comparison key: it lowercases and
// collapses every run of non-alphanumeric characters (spaces, "/", "-", ":",
// "&", etc.) into a single space, trimming the ends. This lets a filename's
// bare "Aliens Vampirella" match a provider's "Aliens / Vampirella", or
// "Spider-Man" match "Spider Man", without resorting to fuzzy scoring. It is
// Unicode-aware so accented and non-Latin titles compare correctly.
func matchKey(s string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(unicode.ToLower(r))
		} else {
			pendingSpace = true
		}
	}
	return b.String()
}

// bestSeriesMatch auto-selects a single series when the name — and, as a
// tiebreaker, the year — identify exactly one result.
func bestSeriesMatch(results []provider.SeriesResult, name string, year int) (provider.SeriesResult, bool) {
	exact := exactNameMatches(results, name)
	if len(exact) == 1 {
		return exact[0], true
	}
	if year > 0 {
		var byYear []provider.SeriesResult
		for _, s := range exact {
			if s.StartYear == year {
				byYear = append(byYear, s)
			}
		}
		if len(byYear) == 1 {
			return byYear[0], true
		}
	}
	return provider.SeriesResult{}, false
}

// seriesLabel renders a series candidate for display: "Name (Year) — Publisher",
// omitting the year when unknown and the publisher when the provider didn't
// supply one. Untrusted fields are sanitized for terminal output.
func seriesLabel(s provider.SeriesResult) string {
	label := sanitize(s.Name)
	if s.StartYear > 0 {
		label += fmt.Sprintf(" (%d)", s.StartYear)
	}
	if s.Publisher != "" {
		label += " — " + sanitize(s.Publisher)
	}
	return label
}

var yearSuffixRe = regexp.MustCompile(`\s*\((?:19|20)\d{2}\)\s*$`)

// stripYearSuffix removes a trailing " (YYYY)" that Metron includes in series
// display names, leaving the bare series name.
func stripYearSuffix(s string) string {
	return yearSuffixRe.ReplaceAllString(s, "")
}

// collectedEditionMarkers denote a collected/edition variant of a series
// (trade paperback, hardcover, graphic novel, etc.) rather than the singles.
var collectedEditionMarkers = []string{"TPB", "HC", "OGN", "GN", "OMNIBUS", "DELUXE", "COMPENDIUM"}

// editionMarker returns the first collected-edition marker present as a word in
// name (case-insensitive), or "" if none.
func editionMarker(name string) string {
	for word := range strings.FieldsSeq(strings.ToUpper(name)) {
		word = strings.Trim(word, ".:-()")
		for _, m := range collectedEditionMarkers {
			if word == m {
				return m
			}
		}
	}
	return ""
}

// preferEdition filters search results so their edition intent matches want's:
// if want names an edition (e.g. "... TPB"), keep only candidates with the same
// marker; if want names none, drop candidates carrying any collected-edition
// marker. If that would remove everything, the unfiltered results are returned
// so a match is still possible.
func preferEdition(results []provider.SeriesResult, want string) []provider.SeriesResult {
	wantMarker := editionMarker(want)
	var out []provider.SeriesResult
	for _, s := range results {
		m := editionMarker(stripYearSuffix(s.Name))
		if wantMarker == "" {
			if m == "" {
				out = append(out, s)
			}
		} else if m == wantMarker {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return results
	}
	return out
}

// --- manual tagging (formerly `cb init`) ---

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
		*f.dst = cleanCSV(v)
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
	writer, genre = cleanCSV(writer), cleanCSV(genre)

	for _, f := range files {
		if err := tagSeriesOne(f, series, writer, genre, ageRating, force); err != nil {
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, err)
		}
	}
	return nil
}

func tagSeriesOne(path, series, writer, genre, ageRating string, force bool) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if found && !force {
		fmt.Printf("%s: already has ComicInfo.xml (skipped; use --force)\n", path)
		return nil
	}

	// Number = trailing digit run of the filename stem, leading zeros stripped.
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	number := ""
	if n, _, ok := archive.LastNumber(stem); ok {
		number = strconv.Itoa(n)
	} else {
		fmt.Printf("%s: warning: no number in filename; Number left empty\n", path)
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
	return writeComicInfo(path, ci)
}

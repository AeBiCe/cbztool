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
	seriesID := fs.String("series-id", "", "tag a file or directory against a known provider series ID; match each file by its issue number (online)")
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
		return fmt.Errorf("usage: cb tag [--manual] [--id <issueID>] [--series-id <seriesID>] [--dry-run] [--force] [--pick] [--refresh] [-v] <path>")
	}
	target := fs.Arg(0)

	if *manual {
		switch {
		case *id != "":
			return fmt.Errorf("tag: --manual and --id are mutually exclusive")
		case *seriesID != "":
			return fmt.Errorf("tag: --series-id applies only to online tagging")
		case *pick:
			return fmt.Errorf("tag: --pick applies only to online tagging")
		case *dryRun:
			return fmt.Errorf("tag: --dry-run applies only to online tagging")
		case *refresh:
			return fmt.Errorf("tag: --refresh applies only to online tagging")
		}
	}

	if *seriesID != "" {
		switch {
		case *id != "":
			return fmt.Errorf("tag: --id and --series-id are mutually exclusive")
		case *pick:
			return fmt.Errorf("tag: --pick and --series-id are mutually exclusive")
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
	if *seriesID != "" {
		return tagBySeriesID(prov, target, info.IsDir(), *seriesID, *dryRun, *force, *verbose)
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

// tagDirOnline tags every .cbz in a directory by name search. Files are grouped
// by their parsed series name, each series is resolved once (and the whole group
// is skipped with a single message if it can't be — no per-issue churn on a
// miss), the local issue count is verified against the provider's, and each file
// is then matched by its number. A mixed directory (several series) is handled
// by resolving each group independently, so one bad group never blocks another.
func tagDirOnline(prov provider.Provider, dir string, dryRun, force bool, id string, verbose bool) error {
	if id != "" {
		return fmt.Errorf("tag: --id cannot be combined with a directory")
	}
	files, err := cbzFiles(dir)
	if err != nil {
		return err
	}
	groups, ungroupable := groupBySeries(files)
	if verbose {
		printGroupingTree(groups, ungroupable)
	}

	var resolved, skipped, failed int
	for _, f := range ungroupable {
		fmt.Printf("%s: no series parsed from filename; skipped\n", f)
		skipped++
	}
	for _, g := range groups {
		r, s, fl := tagSeriesGroup(prov, g, dryRun, force, verbose)
		resolved += r
		skipped += s
		failed += fl
	}
	fmt.Printf("done: %d resolved, %d skipped, %d failed\n", resolved, skipped, failed)
	return nil
}

// tagSeriesGroup resolves one series (by name, once) and tags the group's files,
// returning the group's resolved/skipped/failed tallies. A series that can't be
// confidently matched, or whose local issue count exceeds the provider's record,
// is skipped wholesale with one message rather than probed file-by-file.
func tagSeriesGroup(prov provider.Provider, g *seriesGroup, dryRun, force, verbose bool) (resolved, skipped, failed int) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	results, err := prov.SearchSeries(ctx, g.display)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cb: series %q: %v\n", g.display, err)
		return 0, 0, len(g.files)
	}
	results = preferEdition(results, g.display)
	series, ok := bestSeriesMatch(results, g.display, g.repYear())
	if verbose {
		var picked *provider.SeriesResult
		if ok {
			picked = &series
		}
		printGroupDiagnostics(g, results, picked)
	}
	if !ok {
		fmt.Printf("series %q: no confident match; skipping %d file(s) (tag individually or use --series-id)\n", g.display, len(g.files))
		return 0, len(g.files), 0
	}
	label := fmt.Sprintf("series %q", seriesLabel(series))

	// Verify how much of the run we hold before doing any per-issue work: more
	// local issues than the series has on record is a strong "wrong series /
	// oddly-split files" signal, so refuse the whole group rather than mis-tag.
	local := distinctNumbers(g.parsed)
	switch verdict := classifyIssueCount(local, series.IssueCount); verdict {
	case countOver:
		fmt.Fprintf(os.Stderr, "cb: %s; skipping %d file(s)\n", issueCountNote(label, verdict, local, series.IssueCount), len(g.files))
		return 0, len(g.files), 0
	case countIncomplete:
		fmt.Printf("%s\n", issueCountNote(label, verdict, local, series.IssueCount))
	case countComplete:
		if verbose {
			fmt.Printf("%s\n", issueCountNote(label, verdict, local, series.IssueCount))
		}
	}

	resolve := func(ctx context.Context, number string) (*comicinfo.ComicInfo, error) {
		return prov.IssueByNumber(ctx, series.ID, number)
	}
	for _, f := range g.files {
		written, ferr := tagFileWithResolver(prov, f, resolve, dryRun, force)
		switch {
		case ferr != nil:
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, ferr)
			failed++
		case written:
			resolved++
		default:
			skipped++
		}
	}
	return resolved, skipped, failed
}

// seriesGroup collects the files in a directory that parsed to the same series
// name, so the series is resolved (and verified) once for the whole group.
type seriesGroup struct {
	display string                // representative parsed series name (search + messages)
	files   []string              // archive paths in the group
	parsed  []filename.ParsedName // parsed names, index-aligned with files
}

// repYear returns the earliest non-zero cover year in the group, the best proxy
// for the series' start year (used only as a same-name tiebreaker). Later issues
// of a run carry later cover years, so the minimum is closest to when it began.
func (g *seriesGroup) repYear() int {
	min := 0
	for _, p := range g.parsed {
		if p.Year > 0 && (min == 0 || p.Year < min) {
			min = p.Year
		}
	}
	return min
}

// groupBySeries buckets files by parsed series name, normalized via matchKey so
// "Aliens Vampirella" and "Aliens / Vampirella" co-group. Group order is stable
// (first-seen). Files with no parsable series come back as ungroupable.
func groupBySeries(files []string) (groups []*seriesGroup, ungroupable []string) {
	byKey := make(map[string]*seriesGroup)
	for _, f := range files {
		p := filename.Parse(f)
		if p.Series == "" {
			ungroupable = append(ungroupable, f)
			continue
		}
		key := matchKey(p.Series)
		g := byKey[key]
		if g == nil {
			g = &seriesGroup{display: p.Series}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.files = append(g.files, f)
		g.parsed = append(g.parsed, p)
	}
	return groups, ungroupable
}

// printGroupingTree shows, under -v, how the directory's files bucketed into
// series groups before any network call — each group with its issues, plus any
// ungroupable files. This is the "how did cb group my files?" view; the actual
// provider match for each group is printed later by printGroupDiagnostics.
func printGroupingTree(groups []*seriesGroup, ungroupable []string) {
	fmt.Printf("[verbose] grouped local files into %d series:\n", len(groups))
	for _, g := range groups {
		year := "year ?"
		if y := g.repYear(); y > 0 {
			year = fmt.Sprintf("~%d", y)
		}
		fmt.Printf("[verbose]   %s (%s) — %d issue(s)\n", g.display, year, distinctNumbers(g.parsed))
		for i, p := range g.parsed {
			num := p.Number
			if num == "" {
				num = "?"
			}
			fmt.Printf("[verbose]       #%-5s %s\n", num, filepath.Base(g.files[i]))
		}
	}
	if len(ungroupable) > 0 {
		fmt.Printf("[verbose]   (no series parsed — ungroupable)\n")
		for _, f := range ungroupable {
			fmt.Printf("[verbose]       %s\n", filepath.Base(f))
		}
	}
}

// printSeriesIDTree shows, under -v, the local files being tagged against an
// explicit --series-id, each with its parsed number and whether that number is
// present in the series. It's the single-series analogue of the grouping tree,
// so both batch paths give the same "here's what I'm about to do" view.
func printSeriesIDTree(seriesID string, available int, files []string, parsed []filename.ParsedName, byNum map[string]string) {
	fmt.Printf("[verbose] series %s — %d issue(s) available; %d local file(s):\n", seriesID, available, len(files))
	for i, f := range files {
		num, status := parsed[i].Number, "in series"
		switch num {
		case "":
			num, status = "?", "no number parsed"
		default:
			if _, ok := byNum[canonNumber(num)]; !ok {
				status = "not in series"
			}
		}
		fmt.Printf("[verbose]       #%-5s %s  (%s)\n", num, filepath.Base(f), status)
	}
}

// printGroupDiagnostics dumps, under -v, the series candidates a group's name
// search returned, marking the resolved one with "=>".
func printGroupDiagnostics(g *seriesGroup, results []provider.SeriesResult, picked *provider.SeriesResult) {
	fmt.Printf("[verbose] resolving %q:\n", g.display)
	for _, s := range results {
		mark := "  "
		if picked != nil && s.ID == picked.ID {
			mark = "=>"
		}
		fmt.Printf("[verbose]   %s %s [%d issues] id=%s\n", mark, seriesLabel(s), s.IssueCount, s.ID)
	}
}

// tagBySeriesID tags one file or a whole directory against a known provider
// series ID. It enumerates the series' issues once (SeriesIssueLister), builds a
// number -> issueID map, then matches each file by its parsed issue number and
// writes that issue's full metadata. This skips name-search entirely — there is
// no fuzzy matching — and costs one list call plus one detail per matched file.
// For a directory it applies the same completeness verification as name-search
// batch tagging, refusing to tag when more local issues exist than the series.
func tagBySeriesID(prov provider.Provider, target string, isDir bool, seriesID string, dryRun, force, verbose bool) error {
	lister, ok := prov.(provider.SeriesIssueLister)
	if !ok {
		return fmt.Errorf("tag: the %s provider does not support --series-id", prov.Name())
	}

	listCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	stubs, err := lister.ListIssues(listCtx, seriesID)
	cancel()
	if err != nil {
		return err
	}
	// number -> issueID, keyed by a canonical number so "001" matches "1".
	byNum := make(map[string]string, len(stubs))
	for _, s := range stubs {
		byNum[canonNumber(s.Number)] = s.ID
	}
	// A number not in the listed series is a clean per-file skip (ErrNotFound).
	resolve := func(ctx context.Context, number string) (*comicinfo.ComicInfo, error) {
		id, ok := byNum[canonNumber(number)]
		if !ok {
			return nil, fmt.Errorf("issue %q not in series %s: %w", number, seriesID, provider.ErrNotFound)
		}
		return prov.IssueByID(ctx, id)
	}

	// Collect the target file(s) and parse them up front so -v can show the same
	// file -> issue tree the name-search path does.
	var files []string
	if isDir {
		files, err = cbzFiles(target)
		if err != nil {
			return err
		}
	} else {
		files = []string{target}
	}
	parsed := make([]filename.ParsedName, len(files))
	for i, f := range files {
		parsed[i] = filename.Parse(f)
	}
	if verbose {
		printSeriesIDTree(seriesID, len(stubs), files, parsed, byNum)
	}

	if !isDir {
		_, err := tagFileWithResolver(prov, target, resolve, dryRun, force)
		return err
	}

	// Same completeness check as the name-search path (consistency): the issue
	// list we just fetched is the authoritative count.
	local := distinctNumbers(parsed)
	label := fmt.Sprintf("series %s", seriesID)
	switch verdict := classifyIssueCount(local, len(stubs)); verdict {
	case countOver:
		return fmt.Errorf("%s; refusing to tag (verify the series id, the files, or split the directory)", issueCountNote(label, verdict, local, len(stubs)))
	case countIncomplete:
		fmt.Printf("%s\n", issueCountNote(label, verdict, local, len(stubs)))
	case countComplete:
		if verbose {
			fmt.Printf("%s\n", issueCountNote(label, verdict, local, len(stubs)))
		}
	}

	var resolved, skipped, failed int
	for _, f := range files {
		written, ferr := tagFileWithResolver(prov, f, resolve, dryRun, force)
		switch {
		case ferr != nil:
			fmt.Fprintf(os.Stderr, "cb: %s: %v\n", f, ferr)
			failed++
		case written:
			resolved++
		default:
			skipped++
		}
	}
	fmt.Printf("done: %d resolved, %d skipped, %d failed\n", resolved, skipped, failed)
	return nil
}

// issueResolver turns a parsed issue number into resolved metadata for an
// already-determined series. The two batch paths differ only here: name-search
// grouping looks the number up in the series (IssueByNumber); --series-id looks
// it up in the pre-listed issue map (IssueByID). A number the series doesn't
// contain must be reported as provider.ErrNotFound so callers treat it as a skip.
type issueResolver func(ctx context.Context, number string) (*comicinfo.ComicInfo, error)

// tagFileWithResolver applies a resolved-series issueResolver to one archive: it
// honors an existing ComicInfo (unless force/dry-run), requires a parsed issue
// number, fetches metadata, takes PageCount from the archive, and writes (or
// dry-run prints). A number the series lacks (ErrNotFound) is a skip, not an
// error, so one stray file never aborts a batch. written is true only when
// metadata was produced.
func tagFileWithResolver(prov provider.Provider, path string, resolve issueResolver, dryRun, force bool) (written bool, err error) {
	a, err := archive.Open(path)
	if err != nil {
		return false, err
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if found && !force && !dryRun {
		fmt.Printf("%s: already has ComicInfo.xml (skipped; use --force)\n", path)
		return false, nil
	}

	number := filename.Parse(path).Number
	if number == "" {
		fmt.Printf("%s: no issue number parsed from filename; skipped\n", path)
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	ci, err := resolve(ctx, number)
	cancel()
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			fmt.Printf("%s: issue #%s not in the series; skipped\n", path, number)
			return false, nil
		}
		return false, err
	}
	ci.PageCount = pageCount // archive is authoritative for PageCount

	if dryRun {
		if perr := printDryRun(path, prov.Name(), ci); perr != nil {
			return false, perr
		}
		return true, nil
	}
	if werr := writeComicInfo(path, ci); werr != nil {
		return false, werr
	}
	return true, nil
}

// seriesCountVerdict classifies how the count of distinct issues held locally
// compares to the count the provider reports for the series. It is the shared
// verification behind both batch paths, so they behave identically.
type seriesCountVerdict int

const (
	countUnknown    seriesCountVerdict = iota // provider reported no count; can't verify
	countComplete                             // local == reported
	countIncomplete                           // local < reported (only part of the run)
	countOver                                 // local > reported (likely the wrong series)
)

// classifyIssueCount compares distinct local issue numbers to the provider's
// reported issue count (reported <= 0 means "unknown", so checks are skipped).
func classifyIssueCount(localDistinct, reported int) seriesCountVerdict {
	switch {
	case reported <= 0:
		return countUnknown
	case localDistinct > reported:
		return countOver
	case localDistinct < reported:
		return countIncomplete
	default:
		return countComplete
	}
}

// issueCountNote renders the one-line note for a count verdict (empty for an
// unknown count). label is e.g. `series "Saga (2014)"`.
func issueCountNote(label string, v seriesCountVerdict, localDistinct, reported int) string {
	switch v {
	case countOver:
		return fmt.Sprintf("%s: %d local issue(s) exceed the %d on record — likely the wrong series or oddly-split files", label, localDistinct, reported)
	case countIncomplete:
		return fmt.Sprintf("%s: have %d of %d issue(s); series is incomplete locally", label, localDistinct, reported)
	case countComplete:
		return fmt.Sprintf("%s: %d of %d issue(s); series complete", label, localDistinct, reported)
	default:
		return ""
	}
}

// distinctNumbers counts the distinct canonical issue numbers among parsed
// filenames. Files without a parsable number don't count toward the total, and
// variants/duplicates of the same number count once — so the figure reflects
// distinct issues, not file count.
func distinctNumbers(parsed []filename.ParsedName) int {
	seen := make(map[string]struct{}, len(parsed))
	for _, p := range parsed {
		if p.Number != "" {
			seen[canonNumber(p.Number)] = struct{}{}
		}
	}
	return len(seen)
}

// canonNumber canonicalizes an issue number for matching a filename's number
// against a provider's: leading zeros are stripped from plain integers ("001" ->
// "1"), decimals and alphanumeric suffixes are kept but lowercased ("1.MU" ->
// "1.mu"). It mirrors filename.Parse's number normalization.
func canonNumber(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.ContainsRune(s, '.') {
		if n, err := strconv.Atoi(s); err == nil {
			return strconv.Itoa(n)
		}
	}
	return strings.ToLower(s)
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

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

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/comicinfo"
	"github.com/AeBiCe/cbztool/internal/config"
	"github.com/AeBiCe/cbztool/internal/filename"
	"github.com/AeBiCe/cbztool/internal/provider"
)

// Tag implements `cbz tag [flags] <path>`: populate ComicInfo.xml from an online
// provider. A single .cbz is one comic; a directory is processed as a batch
// (online auto-match per file). `-m`/`--manual` anywhere before the path
// switches to the manual mode (see TagManual), which has its own flag set.
func Tag(args []string) error {
	if rest, ok := stripManualFlag(args); ok {
		return TagManual(rest)
	}

	fs := flag.NewFlagSet("tag", flag.ExitOnError)
	id := fs.String("id", "", "tag one file by issue id: a Metron number, or a ComicVine 4000-… id")
	seriesID := fs.String("series-id", "", "tag a file or directory against a known series: a Metron number, or a ComicVine 4050-… volume id; files are matched by issue number")
	dryRun := fs.Bool("dry-run", false, "print the resolved ComicInfo without writing")
	force := fs.Bool("force", false, "overwrite an existing ComicInfo.xml")
	pick := fs.Bool("pick", false, "always show the series picker (skip auto-match)")
	refresh := fs.Bool("refresh", false, "ignore cached API responses and refetch")
	providerName := fs.String("provider", "", "use only this provider: metron or comicvine (aliases mt, cv); default is every configured provider, combined")
	fs.StringVar(providerName, "p", "", "shorthand for --provider")
	verbose := fs.Bool("verbose", false, "show parsed fields, series candidates, and how each source was matched")
	fs.BoolVar(verbose, "v", false, "shorthand for --verbose")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz tag [-p <provider>] [--id <issueID>] [--series-id <seriesID>] [--dry-run] [--force] [--pick] [--refresh] [-v] <path>")
		fmt.Fprintln(fs.Output(), "       cbz tag -m ...   manual mode (prompts, JSON file, or -s Field=Value); see cbz tag -m -h")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("tag: expected exactly one <path>")
	}
	target := fs.Arg(0)

	if *seriesID != "" {
		switch {
		case *id != "":
			return fmt.Errorf("tag: --id and --series-id are mutually exclusive")
		case *pick:
			return fmt.Errorf("tag: --pick and --series-id are mutually exclusive")
		}
	}

	// An id names its provider (ComicVine ids carry a 4000-/4050- prefix), so
	// work that out before opening providers.
	var idProv, idValue string
	var err error
	switch {
	case *id != "":
		idProv, idValue, err = idProvider(*id, cvIssuePrefix, *providerName, "--id")
	case *seriesID != "":
		idProv, idValue, err = idProvider(*seriesID, cvVolumePrefix, *providerName, "--series-id")
	}
	if err != nil {
		return err
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}

	src, err := openProviders(*providerName, *refresh, *verbose)
	if err != nil {
		return err
	}
	if idProv == "" {
		if info.IsDir() {
			return tagDirOnline(src, target, *dryRun, *force, *verbose)
		}
		return tagFileOnline(src, nil, target, "", *dryRun, *force, *pick, *verbose)
	}
	prov, err := providerNamed(src.chain, idProv)
	if err != nil {
		return err
	}
	if *seriesID != "" {
		return tagBySeriesID(src, prov, target, info.IsDir(), idValue, *dryRun, *force, *verbose)
	}
	if info.IsDir() {
		return fmt.Errorf("tag: --id cannot be combined with a directory")
	}
	return tagFileOnline(src, prov, target, idValue, *dryRun, *force, *pick, *verbose)
}

// stripManualFlag reports whether args selects the manual mode (-m/--manual
// before any "--" terminator) and returns args with that flag removed, so the
// manual flag set never sees it.
func stripManualFlag(args []string) ([]string, bool) {
	for i, a := range args {
		switch a {
		case "--":
			return args, false
		case "-m", "--m", "-manual", "--manual":
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			return rest, true
		}
	}
	return args, false
}

// buildProvider constructs the named provider (aliases accepted) from its
// credentials in cfg, enabling cache bypass when refresh is set.
func buildProvider(cfg *config.Config, name string, refresh bool) (provider.Provider, error) {
	name = provider.CanonicalProvider(name)
	cr := cfg.Credentials[name]
	prov, err := provider.New(name, provider.ProviderConfig{
		APIKey:   cr.APIKey,
		CacheTTL: cfg.Providers.CacheTTL.Duration,
	})
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

// tagFileOnline resolves and writes (or dry-run prints) metadata for one
// archive: directly by id on idProv when given, otherwise from the filename
// (trying each provider in turn while earlier ones have no match). The result
// is then completed from the other providers (combine).
func tagFileOnline(src sources, idProv provider.Provider, path, id string, dryRun, force, pick, verbose bool) error {
	a, err := archive.Open(path)
	if err != nil {
		return err
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	fmt.Println(Sanitize(filepath.Base(path)))
	if found && !force && !dryRun {
		fmt.Println("  skipped: " + skipAlreadyTagged)
		return nil
	}

	// Generous budget: a resolution is up to 3 calls plus the other sources'
	// lookups, and a throttled call may back off and retry.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	var iss *provider.Issue
	via := idProv // the provider that resolved the issue
	if idProv != nil {
		iss, err = idProv.IssueByID(ctx, id)
	} else {
		iss, via, err = resolveFromFilename(ctx, src.chain, path, pick, verbose)
	}
	if err != nil {
		return err
	}
	ci, names, notes := combine(ctx, src, via, iss, verbose)
	ci.PageCount = pageCount // archive is authoritative for PageCount

	o := fileOutcome{written: true, notes: notes, detail: strings.Join(names, " + ")}
	if dryRun {
		if o.dump, err = ci.Marshal(); err != nil {
			return err
		}
	} else if err := saveComicInfo(path, ci); err != nil {
		return err
	}
	fmt.Printf("  %s as %s\n", o.summary(dryRun), Sanitize(issueLabel(ci)))
	printFileNotes("  ", o.notes)
	os.Stdout.Write(o.dump)
	return nil
}

// issueLabel names a resolved issue for the single-file report:
// `Angor #1 "Runaway"`.
func issueLabel(ci *comicinfo.ComicInfo) string {
	s := ci.Series
	if ci.Number != "" {
		s += " #" + ci.Number
	}
	if ci.Title != "" {
		s += fmt.Sprintf(" %q", ci.Title)
	}
	return s
}

// seriesRef renders a provider series id the way --series-id accepts it
// ("comicvine 4050-98801", "metron 5"), so it can be copied into a re-run.
func seriesRef(providerName, id string) string {
	if providerName == "comicvine" && !strings.HasPrefix(id, cvVolumePrefix+"-") {
		id = cvVolumePrefix + "-" + id
	}
	return providerName + " " + id
}

// tagDirOnline tags every .cbz in a directory by name search. Files are grouped
// by their parsed series name, each series is resolved once, the local issue
// count is verified against the provider's, and each file is then matched by
// its number. Each series gets a header line saying what it matched, with its
// files listed under it. A mixed directory (several series) is handled by
// resolving each group independently, so one bad group never blocks another.
func tagDirOnline(src sources, dir string, dryRun, force, verbose bool) error {
	files, err := cbzFiles(dir)
	if err != nil {
		return err
	}
	groups, ungroupable := groupBySeries(files)

	b := newBatch(len(files), dryRun)
	var numbers []string
	for _, g := range groups {
		for _, p := range g.parsed {
			numbers = append(numbers, p.Number)
		}
	}
	b.numWidth = numColumn(numbers)

	if len(ungroupable) > 0 {
		b.header("no series name in filename", true, false)
		for _, f := range ungroupable {
			b.record(f, fileOutcome{skip: "no series name in filename"})
		}
	}
	for _, g := range groups {
		tagSeriesGroup(src, g, b, dryRun, force, verbose)
	}
	return b.done()
}

// tagSeriesGroup resolves one series (by name, once, across the provider chain)
// and tags the group's files, recording each outcome in b. A series that can't
// be confidently matched on any provider, or whose local issue count exceeds
// the matched provider's record, has every file skipped rather than probed.
func tagSeriesGroup(src sources, g *seriesGroup, b *batch, dryRun, force, verbose bool) {
	m, err := resolveGroupSeries(src.chain, g)
	diag := func() {
		if verbose {
			for _, l := range m.diag {
				fmt.Println("  " + l)
			}
		}
	}
	files := counted(len(g.files), "file", "files")
	switch {
	case err != nil:
		b.header(fmt.Sprintf("%q · %s — series search failed: %s", g.display, files, err), false, true)
		for i, f := range g.files {
			b.record(f, fileOutcome{number: g.parsed[i].Number, err: errors.New("series search failed")})
		}
		return
	case m.prov == nil:
		b.header(fmt.Sprintf("%q · %s — %s; tag files one by one, or use --series-id", g.display, files, m.miss), true, false)
		diag()
		for i, f := range g.files {
			b.record(f, fileOutcome{number: g.parsed[i].Number, skip: "series not matched"})
		}
		return
	}

	head := fmt.Sprintf("%s · %s · %s", seriesLabel(m.series), seriesRef(m.prov.Name(), m.series.ID), files)
	if m.series.IssueCount > 0 {
		head += fmt.Sprintf(", %s on record", counted(m.series.IssueCount, "issue", "issues"))
	}
	if m.prov != src.chain[0] {
		head += fmt.Sprintf(" (no %s match)", src.chain[0].Name())
	}

	// More distinct local issues than the series has on record is a strong
	// "wrong series / oddly-split files" signal: refuse the group, don't mis-tag.
	if classifyIssueCount(distinctNumbers(g.parsed), m.series.IssueCount) == countOver {
		b.header(head+" — more issues here than on record; likely the wrong series, use --series-id", true, false)
		diag()
		for i, f := range g.files {
			b.record(f, fileOutcome{number: g.parsed[i].Number, skip: "more issues here than in the matched series"})
		}
		return
	}
	b.header(head, false, false)
	diag()

	resolve := func(ctx context.Context, number string) (*provider.Issue, error) {
		return m.prov.IssueByNumber(ctx, m.series.ID, number)
	}
	for _, f := range g.files {
		b.record(f, tagFileWithResolver(src, m.prov, f, resolve, dryRun, force, verbose))
	}
}

// groupMatch is the outcome of resolving a series group: the provider and
// series it matched (prov nil when none did), why it didn't match, and the
// candidate lists for -v.
type groupMatch struct {
	prov   provider.Provider
	series provider.SeriesResult
	miss   string
	diag   []string
}

// resolveGroupSeries searches each provider in the chain for a confident series
// match, returning the first that matches. It only advances to the next
// provider when the current one has no exact-name candidate at all (a genuine
// "not here"); an ambiguous-but-present match stops the search rather than
// letting a different source guess. A transport/auth error is returned
// immediately — trying another source never masks a real failure.
func resolveGroupSeries(chain []provider.Provider, g *seriesGroup) (groupMatch, error) {
	var m groupMatch
	var tried []string
	for _, prov := range chain {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		results, err := prov.SearchSeries(ctx, g.display)
		cancel()
		if err != nil {
			return m, fmt.Errorf("%s: %w", prov.Name(), err)
		}
		results = preferEdition(results, g.display)
		series, ok := bestSeriesMatch(results, g.display, g.repYear())
		var picked *provider.SeriesResult
		if ok {
			picked = &series
		}
		m.diag = append(m.diag, candidateLines(prov.Name(), g.display, results, picked)...)
		if ok {
			m.prov, m.series = prov, series
			return m, nil
		}
		tried = append(tried, prov.Name())
		if exact := len(exactNameMatches(results, g.display)); exact > 0 {
			m.miss = fmt.Sprintf("%d series with that name on %s, none clearly the one", exact, prov.Name())
			return m, nil // present but ambiguous: don't let another source override it
		}
	}
	m.miss = "not found on " + strings.Join(tried, " or ")
	return m, nil
}

// candidateLines lists, for -v, the series candidates a search returned,
// marking the chosen one with "=>".
func candidateLines(providerName, query string, results []provider.SeriesResult, picked *provider.SeriesResult) []string {
	lines := []string{fmt.Sprintf("%s: %s for %q", providerName, counted(len(results), "candidate", "candidates"), query)}
	for _, s := range results {
		mark := "  "
		if picked != nil && s.ID == picked.ID {
			mark = "=>"
		}
		lines = append(lines, fmt.Sprintf("  %s %s · %s · %s", mark, seriesLabel(s), counted(s.IssueCount, "issue", "issues"), seriesRef(providerName, s.ID)))
	}
	return lines
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
	parsed := make([]filename.ParsedName, len(files))
	for i, f := range files {
		parsed[i] = filename.Parse(f)
	}
	return groupParsed(files, parsed)
}

// groupParsed is groupBySeries over already-identified files (parsed is
// index-aligned with files), for callers whose series/number come from
// somewhere other than the filename — e.g. an archive's ComicInfo.
func groupParsed(files []string, parsed []filename.ParsedName) (groups []*seriesGroup, ungroupable []string) {
	byKey := make(map[string]*seriesGroup)
	for i, f := range files {
		p := parsed[i]
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

// tagBySeriesID tags one file or a whole directory against a known provider
// series ID. It enumerates the series' issues once (SeriesIssueLister), builds a
// number -> issueID map, then matches each file by its parsed issue number and
// writes that issue's full metadata. This skips name-search entirely — there is
// no fuzzy matching — and costs one list call plus one detail per matched file.
// For a directory it applies the same completeness verification as name-search
// batch tagging, refusing to tag when more local issues exist than the series.
func tagBySeriesID(src sources, prov provider.Provider, target string, isDir bool, seriesID string, dryRun, force, verbose bool) error {
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
	resolve := func(ctx context.Context, number string) (*provider.Issue, error) {
		id, ok := byNum[canonNumber(number)]
		if !ok {
			return nil, fmt.Errorf("issue %q not in series %s: %w", number, seriesID, provider.ErrNotFound)
		}
		return prov.IssueByID(ctx, id)
	}

	if !isDir {
		fmt.Println(Sanitize(filepath.Base(target)))
		o := tagFileWithResolver(src, prov, target, resolve, dryRun, force, verbose)
		if o.err != nil {
			return o.err
		}
		fmt.Printf("  %s\n", Sanitize(o.summary(dryRun)))
		printFileNotes("  ", o.notes)
		os.Stdout.Write(o.dump)
		return nil
	}

	files, err := cbzFiles(target)
	if err != nil {
		return err
	}
	parsed := make([]filename.ParsedName, len(files))
	numbers := make([]string, len(files))
	for i, f := range files {
		parsed[i] = filename.Parse(f)
		numbers[i] = parsed[i].Number
	}

	head := fmt.Sprintf("%s · %s, %s on record", seriesRef(prov.Name(), seriesID),
		counted(len(files), "file", "files"), counted(len(stubs), "issue", "issues"))
	if classifyIssueCount(distinctNumbers(parsed), len(stubs)) == countOver {
		return fmt.Errorf("%s — more issues here than on record; refusing to tag (check the series id, or split the directory)", head)
	}

	b := newBatch(len(files), dryRun)
	b.numWidth = numColumn(numbers)
	b.header(head, false, false)
	for _, f := range files {
		b.record(f, tagFileWithResolver(src, prov, f, resolve, dryRun, force, verbose))
	}
	return b.done()
}

// issueResolver turns a parsed issue number into resolved metadata for an
// already-determined series. The two batch paths differ only here: name-search
// grouping looks the number up in the series (IssueByNumber); --series-id looks
// it up in the pre-listed issue map (IssueByID). A number the series doesn't
// contain must be reported as provider.ErrNotFound so callers treat it as a skip.
type issueResolver func(ctx context.Context, number string) (*provider.Issue, error)

// tagFileWithResolver applies a resolved-series issueResolver to one archive: it
// honors an existing ComicInfo (unless force/dry-run), requires a parsed issue
// number, fetches metadata (completed from the other providers in chain),
// takes PageCount from the archive, and writes (or, under dry-run, keeps the
// XML for the report). It prints nothing; the caller reports the returned
// outcome. A number the series lacks (ErrNotFound) is a skip, not an error, so
// one stray file never aborts a batch.
func tagFileWithResolver(src sources, prov provider.Provider, path string, resolve issueResolver, dryRun, force, verbose bool) fileOutcome {
	number := filename.Parse(path).Number
	a, err := archive.Open(path)
	if err != nil {
		return fileOutcome{number: number, err: err}
	}
	_, _, found, _ := a.ReadComicInfo()
	pageCount := a.PageCount()
	a.Close()
	if found && !force && !dryRun {
		return fileOutcome{number: number, skip: skipAlreadyTagged}
	}
	if number == "" {
		return fileOutcome{skip: "no issue number in filename"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	iss, err := resolve(ctx, number)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			return fileOutcome{number: number, skip: fmt.Sprintf("#%s isn't in this series", number)}
		}
		return fileOutcome{number: number, err: err}
	}
	ci, names, notes := combine(ctx, src, prov, iss, verbose)
	ci.PageCount = pageCount // archive is authoritative for PageCount
	o := fileOutcome{number: number, written: true, detail: strings.Join(names, " + "), notes: notes}

	if dryRun {
		if o.dump, err = ci.Marshal(); err != nil {
			return fileOutcome{number: number, err: err}
		}
	} else if err := saveComicInfo(path, ci); err != nil {
		return fileOutcome{number: number, err: err}
	}
	return o
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
	fmt.Printf("%s: resolved via %s (dry-run, not written):\n", Sanitize(path), providerName)
	os.Stdout.Write(data)
	return nil
}

// resolveFromFilename parses the filename, searches the provider for the
// series, and resolves the issue by number. When the parsed series (and year,
// as a tiebreaker) uniquely identify a result, it is auto-selected with no
// prompt; if the issue number was also parsed, the whole tag is hands-off.
// Otherwise the user picks from the candidates. force (--pick) always prompts.
// Its messages are indented under the file name tagFileOnline prints.
func resolveFromFilename(ctx context.Context, chain []provider.Provider, path string, force, verbose bool) (*provider.Issue, provider.Provider, error) {
	parsed := filename.Parse(path)
	p := newPrompter()

	name := parsed.Series
	if name == "" {
		q, err := p.ask("  Series to search")
		if err != nil {
			return nil, nil, err
		}
		name = q
	}
	if verbose {
		fmt.Printf("  parsed: series %q, number %q, year %d, volume %d\n", parsed.Series, parsed.Number, parsed.Year, parsed.Volume)
	}

	// Auto-match across the providers (skipped under --pick). It advances to the
	// next provider only when the current one has no exact-name candidate at
	// all, never on a transport/auth error, and never to override an
	// ambiguous-but-present match.
	if !force {
		for _, prov := range chain {
			results, err := prov.SearchSeries(ctx, name)
			if err != nil {
				return nil, nil, err
			}
			results = preferEdition(results, name)
			s, ok := bestSeriesMatch(results, name, parsed.Year)
			if verbose {
				var picked *provider.SeriesResult
				if ok {
					picked = &s
				}
				for _, l := range candidateLines(prov.Name(), name, results, picked) {
					fmt.Println("  " + l)
				}
				if !ok {
					fmt.Printf("  no confident match (need exactly one exact-name candidate%s)\n", yearHint(parsed.Year))
				}
			}
			if ok {
				line := fmt.Sprintf("  series: %s · %s", seriesLabel(s), seriesRef(prov.Name(), s.ID))
				if prov != chain[0] {
					line += fmt.Sprintf(" (no %s match)", chain[0].Name())
				}
				fmt.Println(line)
				iss, err := resolveIssue(ctx, prov, p, s, parsed.Number)
				return iss, prov, err
			}
			if len(exactNameMatches(results, name)) > 0 {
				break // present but ambiguous: don't let another source guess
			}
		}
	}

	// Interactive picker on the primary provider (single-file only).
	prov := chain[0]
	results, err := prov.SearchSeries(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if len(results) == 0 {
		return nil, nil, fmt.Errorf("no series found for %q", name)
	}
	results = preferEdition(results, name)
	candidates := exactNameMatches(results, name)
	if len(candidates) == 0 {
		candidates = results
	}
	fmt.Printf("  pick the series for %q on %s:\n", name, prov.Name())
	for i, s := range candidates {
		fmt.Printf("  %3d) %s · %s · %s\n", i, seriesLabel(s), counted(s.IssueCount, "issue", "issues"), seriesRef(prov.Name(), s.ID))
	}
	idx, err := p.askIndex("  choice [0]", len(candidates))
	if err != nil {
		return nil, nil, err
	}
	iss, err := resolveIssue(ctx, prov, p, candidates[idx], parsed.Number)
	return iss, prov, err
}

// resolveIssue fetches the issue by number, prompting for the number only when
// it could not be parsed from the filename.
func resolveIssue(ctx context.Context, prov provider.Provider, p *prompter, series provider.SeriesResult, number string) (*provider.Issue, error) {
	if number == "" {
		ans, err := p.ask("  Issue number")
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

// yearHint notes that the parsed year is used as a tiebreaker, for the verbose
// "no confident match" explanation.
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
	label := Sanitize(s.Name)
	if s.StartYear > 0 {
		label += fmt.Sprintf(" (%d)", s.StartYear)
	}
	if s.Publisher != "" {
		label += " — " + Sanitize(s.Publisher)
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

package command

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AeBiCe/cbztool/internal/archive"
	"github.com/AeBiCe/cbztool/internal/filename"
	"github.com/AeBiCe/cbztool/internal/provider"
	"github.com/AeBiCe/cbztool/internal/term"
)

// Missing implements `cbz missing [flags] <dir>`: for each series in a
// directory, list the issues you don't have. Series are resolved exactly as
// `cbz tag` resolves them (or pinned with --series-id), and the provider's full
// issue list is compared with the local issue numbers. --offline needs no
// provider and reports only gaps in the local numbering. Missing issues are a
// report, not a failure; only a series that couldn't be checked (network,
// auth) makes the exit status non-zero.
func Missing(args []string) error {
	fs := flag.NewFlagSet("missing", flag.ExitOnError)
	recursive := fs.Bool("r", false, "include .cbz files in subdirectories")
	offline := fs.Bool("offline", false, "don't contact a provider; report only gaps in the local numbering")
	seriesID := fs.String("series-id", "", "treat every file as this series: a Metron number, or a ComicVine 4050-… volume id")
	providerName := fs.String("provider", "", "use only this provider: metron or comicvine (aliases mt, cv)")
	fs.StringVar(providerName, "p", "", "shorthand for --provider")
	refresh := fs.Bool("refresh", false, "ignore cached API responses and refetch")
	verbose := fs.Bool("verbose", false, "show series candidates and how each series was matched")
	fs.BoolVar(verbose, "v", false, "shorthand for --verbose")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: cbz missing [-r] [-p <provider>] [--series-id <id>] [--offline] [--refresh] [-v] <dir>")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("missing: expected exactly one <dir>")
	}
	files, err := libraryFiles(fs.Arg(0), *recursive)
	if err != nil {
		return err
	}
	ids := make([]filename.ParsedName, len(files))
	for i, f := range files {
		ids[i] = localIdentity(f)
	}

	if *offline {
		if *seriesID != "" || *providerName != "" {
			return fmt.Errorf("missing: --offline can't be combined with --series-id or -p")
		}
		missingOffline(files, ids)
		return nil
	}

	var idProv, idValue string
	if *seriesID != "" {
		if idProv, idValue, err = idProvider(*seriesID, cvVolumePrefix, *providerName, "--series-id"); err != nil {
			return err
		}
	}
	src, err := openProviders(*providerName, *refresh, *verbose)
	if err != nil {
		return err
	}
	if idProv != "" {
		prov, err := providerNamed(src.chain, idProv)
		if err != nil {
			return err
		}
		label := fmt.Sprintf("series %s", Sanitize(*seriesID))
		return reportMissing(prov, idValue, label, ids)
	}
	return missingOnline(src.chain, files, ids, *verbose)
}

// localIdentity is what cbz knows about a local issue: the archive's ComicInfo
// Series/Number/Year when it has them, otherwise what the filename says.
func localIdentity(path string) filename.ParsedName {
	p := filename.Parse(path)
	a, err := archive.Open(path)
	if err != nil {
		return p
	}
	ci, _, found, cerr := a.ReadComicInfo()
	a.Close()
	if found && cerr == nil {
		if ci.Series != "" {
			p.Series = ci.Series
		}
		if ci.Number != "" {
			p.Number = ci.Number
		}
		if ci.Year != 0 {
			p.Year = ci.Year
		}
	}
	return p
}

// missingOnline resolves each series group on the providers and reports it.
func missingOnline(chain []provider.Provider, files []string, ids []filename.ParsedName, verbose bool) error {
	groups, ungroupable := groupParsed(files, ids)
	for _, f := range ungroupable {
		fmt.Printf("%s %s — skipped: no series in its ComicInfo.xml or filename\n", term.Warn(os.Stdout), Sanitize(filepath.Base(f)))
	}
	failed := 0
	for _, g := range groups {
		m, err := resolveGroupSeries(chain, g)
		if verbose {
			for _, l := range m.diag {
				fmt.Println("  " + l)
			}
		}
		switch {
		case err != nil:
			term.Errorf("%q — series search failed: %s", Sanitize(g.display), Sanitize(err.Error()))
			failed++
			continue
		case m.prov == nil:
			fmt.Printf("%s %q — skipped: %s; pin it with --series-id\n", term.Warn(os.Stdout), Sanitize(g.display), m.miss)
			continue
		}
		label := fmt.Sprintf("%s · %s", seriesLabel(m.series), seriesRef(m.prov.Name(), m.series.ID))
		if err := reportMissing(m.prov, m.series.ID, label, g.parsed); err != nil {
			term.Errorf("%s: %s", label, Sanitize(err.Error()))
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d series could not be checked", failed, len(groups))
	}
	return nil
}

// reportMissing lists a series' issues on prov and prints how the local
// issues compare.
func reportMissing(prov provider.Provider, seriesID, label string, local []filename.ParsedName) error {
	lister, ok := prov.(provider.SeriesIssueLister)
	if !ok {
		return fmt.Errorf("the %s provider can't list a series' issues", prov.Name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	stubs, err := lister.ListIssues(ctx, seriesID)
	if err != nil {
		return err
	}
	gap := compareIssues(stubs, local)
	switch {
	case gap.total == 0:
		fmt.Printf("%s — the provider lists no issues\n", label)
	case len(gap.missing) == 0:
		fmt.Printf("%s — have %d of %s, complete\n", label, gap.have, counted(gap.total, "issue", "issues"))
	default:
		fmt.Printf("%s — have %d of %s, missing %d: %s\n", label, gap.have, counted(gap.total, "issue", "issues"), len(gap.missing), formatNumbers(gap.missing))
	}
	if len(gap.extra) > 0 {
		fmt.Printf("  not listed by %s: %s\n", prov.Name(), formatNumbers(gap.extra))
	}
	return nil
}

// seriesGap compares the issues a provider lists for a series with the local
// ones, by canonical number.
type seriesGap struct {
	have, total int
	missing     []string // listed but not held (provider's spelling), sorted
	extra       []string // held but not listed (local spelling), sorted
}

func compareIssues(stubs []provider.IssueStub, local []filename.ParsedName) seriesGap {
	held := map[string]string{} // canonical -> local spelling
	for _, p := range local {
		if p.Number != "" {
			held[canonNumber(p.Number)] = p.Number
		}
	}
	listed := map[string]bool{}
	var g seriesGap
	for _, s := range stubs {
		k := canonNumber(s.Number)
		if k == "" || listed[k] {
			continue // variants share a number; count it once
		}
		listed[k] = true
		g.total++
		if _, ok := held[k]; ok {
			g.have++
		} else {
			g.missing = append(g.missing, s.Number)
		}
	}
	for k, n := range held {
		if !listed[k] {
			g.extra = append(g.extra, n)
		}
	}
	sortNumbers(g.missing)
	sortNumbers(g.extra)
	return g
}

// missingOffline reports, per series, the gaps in the local integer numbering
// from #1 to the highest issue held. Without a provider, issues after the
// highest one can't be known.
func missingOffline(files []string, ids []filename.ParsedName) {
	groups, ungroupable := groupParsed(files, ids)
	for _, f := range ungroupable {
		fmt.Printf("%s: skipped — no series in its ComicInfo.xml or filename\n", Sanitize(f))
	}
	for _, g := range groups {
		gaps, highest := localGaps(g.parsed)
		label := fmt.Sprintf("series %q", Sanitize(g.display))
		switch {
		case highest == 0:
			fmt.Printf("%s: no numbered issues\n", label)
		case len(gaps) == 0:
			fmt.Printf("%s: no gaps in #1–#%d\n", label, highest)
		default:
			fmt.Printf("%s: missing %d in #1–#%d: %s\n", label, len(gaps), highest, formatNumbers(gaps))
		}
	}
}

// localGaps returns the whole numbers from 1 to the highest whole issue
// number held that aren't held. Decimal or suffixed numbers ("1.5", "1.MU")
// count as held but never extend the range.
func localGaps(local []filename.ParsedName) (gaps []string, highest int) {
	held := map[int]bool{}
	for _, p := range local {
		if n, err := strconv.Atoi(canonNumber(p.Number)); err == nil && n >= 0 {
			held[n] = true
			highest = max(highest, n)
		}
	}
	for n := 1; n <= highest; n++ {
		if !held[n] {
			gaps = append(gaps, strconv.Itoa(n))
		}
	}
	return gaps, highest
}

// sortNumbers orders issue numbers numerically where they parse as numbers
// ("2" before "10", "1.5" between them), then the rest alphabetically.
func sortNumbers(nums []string) {
	slices.SortStableFunc(nums, func(a, b string) int {
		fa, ea := strconv.ParseFloat(a, 64)
		fb, eb := strconv.ParseFloat(b, 64)
		switch {
		case ea == nil && eb == nil:
			return cmp.Compare(fa, fb)
		case ea == nil:
			return -1
		case eb == nil:
			return 1
		}
		return strings.Compare(a, b)
	})
}

// formatNumbers joins sorted issue numbers, collapsing runs of three or more
// consecutive whole numbers into a range: "1, 2, 4–7, 9.5".
func formatNumbers(nums []string) string {
	var out []string
	for i := 0; i < len(nums); {
		start, err := strconv.Atoi(nums[i])
		j := i + 1
		if err == nil {
			for j < len(nums) {
				if n, err := strconv.Atoi(nums[j]); err != nil || n != start+(j-i) {
					break
				}
				j++
			}
		}
		if j-i >= 3 {
			out = append(out, fmt.Sprintf("%s–%s", nums[i], nums[j-1]))
		} else {
			j = i + 1
			out = append(out, nums[i])
		}
		i = j
	}
	return Sanitize(strings.Join(out, ", "))
}

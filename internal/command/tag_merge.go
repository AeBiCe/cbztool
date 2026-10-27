package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/provider"
	"codeberg.org/Nikode/cbztool/internal/term"
)

// sources is the set of providers an online run draws on and how their data is
// merged.
type sources struct {
	chain  []provider.Provider // precedence order: the first wins single values
	prefer map[string]string   // ComicInfo field -> provider whose value wins it
}

// openProviders builds the providers for an online run, in the configured
// precedence order (providers.order, then any unlisted ones). With pinned (-p)
// set, only that provider is used. Otherwise every configured provider is
// used; a provider without credentials is left out, so a single-provider setup
// keeps working. It is an error only if none is usable.
func openProviders(pinned string, refresh, verbose bool) (sources, error) {
	cfg, err := loadConfig()
	if err != nil {
		return sources{}, err
	}
	if err := cfg.ResolveSecrets(); err != nil {
		return sources{}, err
	}
	if pinned != "" {
		p, err := buildProvider(cfg, pinned, refresh)
		if err != nil {
			return sources{}, err
		}
		return sources{chain: []provider.Provider{p}}, nil
	}

	order := slices.Clone(cfg.Providers.Order)
	for _, n := range provider.Names() {
		if !slices.Contains(order, n) {
			order = append(order, n)
		}
	}
	var chain []provider.Provider
	var firstErr error
	for _, n := range order {
		p, err := buildProvider(cfg, n, refresh)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if verbose {
				fmt.Printf("%s %s not used: %s\n", term.Warn(os.Stdout), n, Sanitize(err.Error()))
			}
			continue
		}
		chain = append(chain, p)
	}
	if len(chain) == 0 {
		return sources{}, firstErr
	}
	return sources{chain: chain, prefer: cfg.Providers.Prefer}, nil
}

// ComicVine prefixes ids with a resource-type code: 4000 for issues, 4050 for
// volumes (series), as seen in its URLs (".../4050-160423/").
const (
	cvIssuePrefix  = "4000"
	cvVolumePrefix = "4050"
)

// idProvider decides which provider an --id / --series-id value belongs to, so
// both kinds of id work without -p: a ComicVine id carries its resource prefix
// (want: cvIssuePrefix or cvVolumePrefix), a bare number is Metron's. With
// pinned set, a bare number belongs to the pinned provider (so `-p cv --id
// 12345` keeps working). It returns the provider name and the bare numeric id.
func idProvider(raw, want, pinned, flagName string) (name, id string, err error) {
	raw = strings.TrimSpace(raw)
	pinned = provider.CanonicalProvider(pinned)
	prefix, rest, prefixed := strings.Cut(raw, "-")
	if !prefixed {
		if !isNumericID(raw) {
			return "", "", fmt.Errorf("tag: %s %q is not a valid id (a Metron number, or a ComicVine %s-… id)", flagName, raw, want)
		}
		if pinned == "" {
			pinned = "metron"
		}
		return pinned, raw, nil
	}

	if !isNumericID(rest) {
		return "", "", fmt.Errorf("tag: %s %q is not a valid id", flagName, raw)
	}
	switch prefix {
	case want:
	case cvIssuePrefix:
		return "", "", fmt.Errorf("tag: %s %q is a ComicVine issue id; %s takes a volume id (%s-…)", flagName, raw, flagName, cvVolumePrefix)
	case cvVolumePrefix:
		return "", "", fmt.Errorf("tag: %s %q is a ComicVine volume id; %s takes an issue id (%s-…)", flagName, raw, flagName, cvIssuePrefix)
	default:
		return "", "", fmt.Errorf("tag: %s %q has an unknown prefix (ComicVine uses %s-…)", flagName, raw, want)
	}
	if pinned != "" && pinned != "comicvine" {
		return "", "", fmt.Errorf("tag: %s %q is a ComicVine id, but -p selects %s", flagName, raw, pinned)
	}
	return "comicvine", rest, nil
}

func isNumericID(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// providerNamed returns the provider in chain with the given name.
func providerNamed(chain []provider.Provider, name string) (provider.Provider, error) {
	for _, p := range chain {
		if p.Name() == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("tag: the id belongs to %s, which isn't configured (see config.json)", name)
}

// combine completes an issue resolved by via with the same issue from every
// other provider in src, then merges them in chain order: the first
// provider's values win, the rest fill gaps and add list entries (see
// comicinfo.Merge), and src.prefer then overrides individual fields
// (applyPrefer). The other providers are matched by exact id, never by a
// fuzzy search; a lookup that fails or disagrees on the issue number is
// reported and skipped, leaving the data that did resolve. Notes records which
// issue ids the result came from. It returns the merged metadata, the names of
// the providers that contributed, and notes for the file's report (warnings,
// plus how each source matched when verbose).
func combine(ctx context.Context, src sources, via provider.Provider, iss *provider.Issue, verbose bool) (*comicinfo.ComicInfo, []string, []note) {
	found := map[string]*provider.Issue{via.Name(): iss}
	var log []note
	for _, o := range src.chain {
		if o == via {
			continue
		}
		other, how, err := sameIssue(ctx, o, via, iss)
		switch {
		case err != nil && !errors.Is(err, provider.ErrNotFound):
			log = append(log, note{true, fmt.Sprintf("%s lookup failed (%s); used the other source(s) only", o.Name(), err)})
			continue
		case other == nil:
			if verbose {
				log = append(log, note{false, fmt.Sprintf("%s: no match (%s)", o.Name(), how)})
			}
			continue
		case canonNumber(other.Number) != canonNumber(iss.Number):
			log = append(log, note{true, fmt.Sprintf("%s issue %s is #%s, not #%s; ignored its data", o.Name(), other.ID, other.Number, iss.Number)})
			continue
		}
		if verbose {
			log = append(log, note{false, fmt.Sprintf("%s: issue %s (%s)", o.Name(), other.ID, how)})
		}
		found[o.Name()] = other
	}

	var ci *comicinfo.ComicInfo
	var names, notes []string
	for _, p := range src.chain {
		if r := found[p.Name()]; r != nil {
			ci = comicinfo.Merge(ci, r.ComicInfo)
			names = append(names, p.Name())
			notes = append(notes, fmt.Sprintf("%s issue %s", p.Name(), r.ID))
		}
	}
	applyPrefer(ci, found, src.prefer)
	if ci.Notes == "" {
		ci.Notes = "Tagged by cbztool: " + strings.Join(notes, ", ")
	}
	return ci, names, log
}

// applyPrefer overrides fields of the merged ci with the value from the
// provider configured for them (providers.prefer), when that provider
// contributed and has a value. Year stands for the whole date, which is never
// mixed across providers.
func applyPrefer(ci *comicinfo.ComicInfo, found map[string]*provider.Issue, prefer map[string]string) {
	for field, name := range prefer {
		r := found[name]
		if r == nil {
			continue
		}
		if field == "Year" {
			if r.Year != 0 {
				ci.Year, ci.Month, ci.Day = r.Year, r.Month, r.Day
			}
			continue
		}
		if v := r.Get(field); v != "" {
			_ = ci.Set(field, v) // v came from Get, so it always parses
		}
	}
}

// sameIssue finds, on provider o, the issue that via resolved: first by via's
// cross-reference to o, then by o's reverse lookup of via's id, and finally by
// an exact, unambiguous series-name match plus the issue number. A clean miss
// returns (nil, reason, nil or ErrNotFound); how describes the route taken.
func sameIssue(ctx context.Context, o, via provider.Provider, iss *provider.Issue) (*provider.Issue, string, error) {
	if id := iss.XRef[o.Name()]; id != "" {
		other, err := o.IssueByID(ctx, id)
		return other, via.Name() + " cross-reference", err
	}
	if xl, ok := o.(provider.XRefLookup); ok && iss.ID != "" {
		other, err := xl.IssueByXRef(ctx, via.Name(), iss.ID)
		if !errors.Is(err, provider.ErrNotFound) {
			return other, "lookup by " + via.Name() + " id", err
		}
	}
	if iss.Series == "" || iss.Number == "" {
		return nil, "no series/number to search by", nil
	}
	results, err := o.SearchSeries(ctx, iss.Series)
	if err != nil {
		return nil, "series search", err
	}
	exact := exactNameMatches(preferEdition(results, iss.Series), iss.Series)
	if len(exact) != 1 {
		return nil, fmt.Sprintf("no cross-reference, and %d exact series-name matches", len(exact)), nil
	}
	other, err := o.IssueByNumber(ctx, exact[0].ID, iss.Number)
	if errors.Is(err, provider.ErrNotFound) {
		return nil, "series matched by name, but not the issue number", nil
	}
	return other, "series matched by name", err
}

// Package provider defines the online comic-metadata source abstraction and its
// backends (Metron and ComicVine; GCD later). Backends self-register and are
// selected by configuration; matching is deliberately ID-based rather than
// fuzzy to avoid the mismatch problems of tools like ComicTagger.
package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/AeBiCe/cbztool/internal/comicinfo"
)

// ErrNotFound is returned (wrapped) by a provider when a lookup resolves to no
// result — e.g. a series has no issue with the requested number. Callers use
// errors.Is to distinguish a clean "no match" (skippable) from a transport or
// auth failure (a real error). Backends must wrap it: fmt.Errorf("…: %w", ErrNotFound).
var ErrNotFound = errors.New("not found")

// SeriesResult is one candidate series/volume returned by a provider search.
// The user (or a heuristic) picks among these to disambiguate before an issue
// is fetched.
//
// Contract every backend must honor (the shared matcher in package command
// relies on it):
//   - Name is the *bare* series name, e.g. "Saga" — not "Saga (2014)". The year
//     belongs in StartYear. Edition markers (TPB/HC/GN/…) stay in Name so
//     edition-aware filtering works.
//   - StartYear is the first publication year (0 if unknown).
//   - Publisher is optional; set it when the search exposes it so the picker can
//     disambiguate same-named series across publishers.
type SeriesResult struct {
	ID         string // provider-specific series/volume identifier
	Name       string
	StartYear  int
	Publisher  string
	IssueCount int
}

// Issue is one resolved issue: its metadata plus the identifiers needed to find
// the same issue on other providers, so the command layer can combine sources
// by exact ID rather than by a second name search. ComicInfo is embedded, so an
// Issue reads like the metadata it carries.
type Issue struct {
	*comicinfo.ComicInfo
	ID   string            // this provider's issue ID
	XRef map[string]string // other provider name -> its issue ID (e.g. "comicvine": "675976")
}

// Provider is an online comic-metadata source (Metron, ComicVine, GCD, ...).
// Implementations map their native schema onto cbz's ComicInfo. Every call takes
// a context for timeout and cancellation.
//
// The accuracy strategy is deliberately ID-based: resolve a series once
// (SearchSeries), then fetch the exact issue (IssueByNumber). IssueByID supports
// a direct `cbz tag --id` lookup with no matching at all.
type Provider interface {
	// Name is the short provider identifier, e.g. "metron".
	Name() string
	// SearchSeries returns series/volumes whose name matches query.
	SearchSeries(ctx context.Context, query string) ([]SeriesResult, error)
	// IssueByNumber fetches one issue of a series, mapped to ComicInfo.
	IssueByNumber(ctx context.Context, seriesID, number string) (*Issue, error)
	// IssueByID fetches one issue directly by its provider issue ID.
	IssueByID(ctx context.Context, issueID string) (*Issue, error)
}

// XRefLookup is an optional Provider capability: find this provider's issue from
// another provider's issue ID (e.g. Metron's issue with a given ComicVine id).
// It lets the command layer combine sources in the direction the other
// provider's data doesn't cross-reference. A miss must wrap ErrNotFound.
type XRefLookup interface {
	IssueByXRef(ctx context.Context, otherProvider, otherID string) (*Issue, error)
}

// IssueStub is a lightweight issue identifier from a series listing: just
// enough to match a filename's issue number to a provider issue ID, without the
// cost (or detail) of a full issue fetch. Resolve the full metadata by passing
// ID to IssueByID.
type IssueStub struct {
	ID     string // provider issue ID
	Number string // issue number as the provider records it (e.g. "1", "1.MU")
}

// SeriesIssueLister is an optional Provider capability: enumerate every issue of
// a series in one (internally paginated) pass. The command layer uses it for
// `cbz tag --series-id`, which pins a directory to a known series and matches
// each file by its parsed issue number — one list call plus one detail per
// matched file, with no name-search or fuzzy matching at all. Backends that
// cannot enumerate a series simply don't implement it.
type SeriesIssueLister interface {
	ListIssues(ctx context.Context, seriesID string) ([]IssueStub, error)
}

// CacheControl is an optional capability of a Provider that keeps a persistent
// on-disk response cache. The command layer type-asserts to it to honor
// `cbz tag --refresh`, which bypasses cached reads for one run (and repopulates
// the cache with fresh data). Backends without a cache simply don't implement it.
type CacheControl interface {
	SetRefresh(refresh bool)
}

// ProviderConfig holds one backend's settings, resolved by the command layer
// from cbz's configuration (package config). Credentials are separate fields so
// each backend takes only what it needs (ComicVine uses APIKey; Metron/GCD use
// HTTP Basic user/pass). CacheTTL is the on-disk response freshness; zero means
// the default (defaultCacheTTL).
type ProviderConfig struct {
	APIKey   string
	Username string
	Password string
	CacheTTL time.Duration
}

// providerFactory builds a configured Provider from its settings.
type providerFactory func(cfg ProviderConfig) (Provider, error)

// providers is the registry of known backends, keyed by canonical Name().
// Backends register themselves via registerProvider; selection is driven by
// config.
var providers = map[string]providerFactory{}

// registerProvider adds a backend factory to the registry.
func registerProvider(name string, f providerFactory) { providers[name] = f }

// Names lists the registered backends' canonical names, sorted.
func Names() []string {
	return slices.Sorted(maps.Keys(providers))
}

// providerAliases maps short aliases to canonical provider names, so a user can
// write `-p cv` / `-p mt` (or the same in config.json / CBZ_PROVIDER).
var providerAliases = map[string]string{
	"mt": "metron",
	"cv": "comicvine",
}

// CanonicalProvider resolves a provider name or alias to its canonical form
// (trimmed and lowercased). An unrecognized name is returned as-is so New can
// report it as unknown.
func CanonicalProvider(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if canon, ok := providerAliases[name]; ok {
		return canon
	}
	return name
}

// New builds the named Provider (aliases accepted) from its settings.
func New(name string, cfg ProviderConfig) (Provider, error) {
	factory, ok := providers[CanonicalProvider(name)]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	return factory(cfg)
}

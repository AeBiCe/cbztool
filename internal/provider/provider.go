// Package provider defines the online comic-metadata source abstraction and its
// backends (Metron, and later ComicVine/GCD). Backends self-register and are
// selected by configuration; matching is deliberately ID-based rather than
// fuzzy to avoid the mismatch problems of tools like ComicTagger.
package provider

import (
	"context"
	"errors"
	"fmt"

	"cb/internal/comicinfo"
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

// Provider is an online comic-metadata source (Metron, ComicVine, GCD, ...).
// Implementations map their native schema onto cb's ComicInfo. Every call takes
// a context for timeout and cancellation.
//
// The accuracy strategy is deliberately ID-based: resolve a series once
// (SearchSeries), then fetch the exact issue (IssueByNumber). IssueByID supports
// a direct `cb tag --id` lookup with no matching at all.
type Provider interface {
	// Name is the short provider identifier, e.g. "metron".
	Name() string
	// SearchSeries returns series/volumes whose name matches query.
	SearchSeries(ctx context.Context, query string) ([]SeriesResult, error)
	// IssueByNumber fetches one issue of a series, mapped to ComicInfo.
	IssueByNumber(ctx context.Context, seriesID, number string) (*comicinfo.ComicInfo, error)
	// IssueByID fetches one issue directly by its provider issue ID.
	IssueByID(ctx context.Context, issueID string) (*comicinfo.ComicInfo, error)
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
// `cb tag --series-id`, which pins a directory to a known series and matches
// each file by its parsed issue number — one list call plus one detail per
// matched file, with no name-search or fuzzy matching at all. Backends that
// cannot enumerate a series simply don't implement it.
type SeriesIssueLister interface {
	ListIssues(ctx context.Context, seriesID string) ([]IssueStub, error)
}

// CacheControl is an optional capability of a Provider that keeps a persistent
// on-disk response cache. The command layer type-asserts to it to honor
// `cb tag --refresh`, which bypasses cached reads for one run (and repopulates
// the cache with fresh data). Backends without a cache simply don't implement it.
type CacheControl interface {
	SetRefresh(refresh bool)
}

// ProviderConfig holds the per-provider settings loaded from the config file or
// environment. Credentials are separate fields so each backend takes only what
// it needs (ComicVine uses APIKey; Metron/GCD use HTTP Basic user/pass).
type ProviderConfig struct {
	APIKey   string `json:"api_key,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// providerFactory builds a configured Provider from its settings.
type providerFactory func(cfg ProviderConfig) (Provider, error)

// providers is the registry of known backends, keyed by Name(). Backends
// register themselves via registerProvider; selection is driven by config.
var providers = map[string]providerFactory{}

// registerProvider adds a backend factory to the registry.
func registerProvider(name string, f providerFactory) { providers[name] = f }

// New builds the Provider named by cfg.Provider using its credentials block.
func New(cfg *Config) (Provider, error) {
	factory, ok := providers[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (configure one in config.json or CB_PROVIDER)", cfg.Provider)
	}
	return factory(cfg.providerConfig(cfg.Provider))
}

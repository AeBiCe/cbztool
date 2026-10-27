package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"codeberg.org/Nikode/cbztool/internal/comicinfo"
	"codeberg.org/Nikode/cbztool/internal/version"
)

const metronBaseURL = "https://metron.cloud/api"

// maxResponseBytes caps how much of an API response we read before decoding, so
// a hostile or malfunctioning endpoint can't stream an unbounded body into
// memory. Metron issue/series JSON is a few KiB; 8 MiB is a generous ceiling.
const maxResponseBytes = 8 << 20

// userAgent identifies cbztool to the APIs, with its version and project URL.
// A descriptive, stable User-Agent (rather than a generic or empty one) keeps
// us in good standing with Metron's bot / rate-limit protection — a missing or
// generic UA is a common cause of 403s. If you fork cbztool, point the URL at
// your fork.
var userAgent = "cbztool/" + version.String() + " (+https://codeberg.org/Nikode/cbztool)"

// Retry policy for throttling (429/403) and transient server/network errors.
const (
	maxAttempts = 4               // total tries per request, including the first
	backoffBase = 2 * time.Second // first backoff; doubles each attempt
	backoffMax  = 30 * time.Second
)

func init() {
	registerProvider("metron", func(cfg ProviderConfig) (Provider, error) {
		if cfg.Username == "" || cfg.Password == "" {
			return nil, fmt.Errorf("metron requires a username and password (credentials.metron in the config, or CBZ_METRON_USERNAME/CBZ_METRON_PASSWORD)")
		}
		return newMetron(cfg.Username, cfg.Password, cfg.CacheTTL), nil
	})
}

// metron is a Provider backed by the Metron API (metron.cloud). It uses HTTP
// Basic auth, self-throttles to stay within Metron's rate limit, backs off and
// retries on throttling, and caches identical GETs both for the life of the
// process (in-memory) and across runs (on disk, TTL-bounded).
type metron struct {
	user, pass string
	baseURL    string
	http       *http.Client
	limiter    *rateLimiter
	disk       *diskCache
	refresh    bool // when true, ignore disk-cache reads for this run

	mu    sync.Mutex
	cache map[string][]byte // url -> raw 200 response body, for the current run
}

func newMetron(user, pass string, cacheTTL time.Duration) *metron {
	return &metron{
		user:    user,
		pass:    pass,
		baseURL: metronBaseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
		limiter: newRateLimiter(3 * time.Second), // ~20/min
		disk:    newDiskCache("metron", cacheTTL),
		cache:   make(map[string][]byte),
	}
}

// SetRefresh implements CacheControl: when set, this run ignores cached
// responses on disk (and refetches), repopulating the cache with fresh data.
func (m *metron) SetRefresh(v bool) { m.refresh = v }

func (m *metron) Name() string { return "metron" }

// get performs a rate-limited, authenticated GET and decodes JSON into out.
// Identical GETs are served from cache so a batch run (e.g. a whole-series
// directory) doesn't re-ask Metron the same question once per file. Two layers
// back this: an in-process map (always authoritative within a run, since it
// only holds bodies fetched fresh this run) and a TTL-bounded on-disk cache
// (shared across runs; skipped on --refresh).
func (m *metron) get(ctx context.Context, path string, query url.Values, out any) error {
	u := m.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	if body, ok := m.cacheGet(u); ok {
		return json.Unmarshal(body, out)
	}
	if !m.refresh {
		if body, ok := m.disk.get(u); ok {
			m.cachePut(u, body)
			return json.Unmarshal(body, out)
		}
	}
	body, err := m.fetch(ctx, u, path)
	if err != nil {
		return err
	}
	m.cachePut(u, body)
	m.disk.put(u, body)
	return json.Unmarshal(body, out)
}

func (m *metron) cacheGet(u string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.cache[u]
	return b, ok
}

func (m *metron) cachePut(u string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[u] = body
}

// fetch issues an authenticated GET with rate-limiting and returns the raw 200
// body. The retry/backoff machinery is shared with other REST backends via
// apiGet; metron only contributes its HTTP Basic credentials.
func (m *metron) fetch(ctx context.Context, u, path string) ([]byte, error) {
	return apiGet(ctx, "metron", m.http, m.limiter, u, path, func(req *http.Request) {
		req.SetBasicAuth(m.user, m.pass)
	})
}

// apiGet performs a rate-limited GET of u for backend `name` and returns the raw
// 200 body. It retries on throttling (429/403), 5xx, and transient network
// errors, honoring a Retry-After header (or a DRF throttle body's "available in
// N seconds") and otherwise backing off exponentially with jitter; it stops at
// the context deadline or after maxAttempts. Accept and User-Agent are always
// set; decorate adds per-backend headers (e.g. Basic auth) and may be nil for
// backends that authenticate via a query parameter. label names the endpoint in
// errors. apiGet neither caches nor decodes — callers layer that on top.
func apiGet(ctx context.Context, name string, client *http.Client, limiter *rateLimiter, u, label string, decorate func(*http.Request)) ([]byte, error) {
	var lastErr error
	throttled := false
	for attempt := range maxAttempts {
		if err := limiter.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if decorate != nil {
			decorate(req)
		}

		resp, err := client.Do(req)
		if err != nil {
			// Transient network error: back off and retry.
			lastErr = redactTransportError(name, label, err)
			if serr := sleep(ctx, backoffDelay(attempt, 0)); serr != nil {
				return nil, serr
			}
			continue
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
			resp.Body.Close()
			return body, rerr

		case resp.StatusCode == http.StatusUnauthorized:
			resp.Body.Close()
			return nil, fmt.Errorf("%s: unauthorized (check credentials)", name)

		case resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode >= 500:
			throttled = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden
			wait := retryAfter(resp.Header)
			if wait == 0 {
				peek, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
				wait = throttleBodyWait(peek)
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: GET %s: %s", name, label, resp.Status)
			if attempt == maxAttempts-1 {
				break
			}
			if serr := sleep(ctx, backoffDelay(attempt, wait)); serr != nil {
				return nil, serr
			}
			continue

		default:
			resp.Body.Close()
			return nil, fmt.Errorf("%s: GET %s: %s", name, label, resp.Status)
		}
	}
	if throttled {
		return nil, fmt.Errorf("%s: still throttled after %d attempts: %v — try again later or lower your request rate", name, maxAttempts, lastErr)
	}
	return nil, fmt.Errorf("%s: giving up after %d attempts: %w", name, maxAttempts, lastErr)
}

// redactTransportError rewrites a transport-layer error for safe reporting: a
// *url.Error stringifies with the full request URL, which for ComicVine carries
// the api_key query parameter, so the message is rebuilt from the endpoint
// label instead. Errors that don't embed a URL pass through unchanged.
func redactTransportError(name, label string, err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %s %s: %w", name, ue.Op, label, ue.Err)
	}
	return err
}

// retryAfter parses a Retry-After header, which may be either a number of
// seconds or an HTTP date. It returns 0 when absent or unparseable.
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

var throttleSecsRe = regexp.MustCompile(`(\d+)\s*second`)

// throttleBodyWait extracts the wait hint from a DRF throttle response body,
// e.g. {"detail":"Request was throttled. Expected available in 23 seconds."}.
func throttleBodyWait(body []byte) time.Duration {
	var d struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &d) != nil {
		return 0
	}
	if mt := throttleSecsRe.FindStringSubmatch(d.Detail); mt != nil {
		if n, err := strconv.Atoi(mt[1]); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

// backoffDelay returns how long to wait before the next attempt. A server hint
// (Retry-After / throttle body) wins, capped to backoffMax; otherwise it is an
// exponential backoff (2s, 4s, 8s, …) with ±25% jitter to avoid lockstep.
func backoffDelay(attempt int, suggested time.Duration) time.Duration {
	if suggested > 0 {
		if suggested > backoffMax {
			return backoffMax
		}
		return suggested
	}
	d := min(backoffBase<<attempt, backoffMax)
	jitter := time.Duration(rand.Int64N(int64(d/2)+1)) - d/4
	return d + jitter
}

// sleep waits for d or until ctx is done, returning ctx's error if it fires.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// --- JSON shapes (subset of the Metron issue/series schema) ---

type metronGenericItem struct {
	Name string `json:"name"`
}

type metronIssue struct {
	ID          int                 `json:"id"`
	Number      string              `json:"number"`
	AltNumber   string              `json:"alt_number"`
	CollTitle   string              `json:"title"` // collection title
	StoryNames  []string            `json:"name"`  // story titles
	CoverDate   string              `json:"cover_date"`
	StoreDate   string              `json:"store_date"`
	Page        int                 `json:"page"` // page count
	Desc        string              `json:"desc"`
	ISBN        string              `json:"isbn"`
	UPC         string              `json:"upc"`
	CVID        int                 `json:"cv_id"` // ComicVine cross-ref
	ResourceURL string              `json:"resource_url"`
	Publisher   metronGenericItem   `json:"publisher"`
	Imprint     metronGenericItem   `json:"imprint"` // null when none
	Series      metronIssueSeries   `json:"series"`
	Rating      metronGenericItem   `json:"rating"`
	Credits     []metronCredit      `json:"credits"`
	Characters  []metronGenericItem `json:"characters"`
	Teams       []metronGenericItem `json:"teams"`
	Arcs        []metronGenericItem `json:"arcs"`
}

type metronIssueSeries struct {
	Name       string              `json:"name"`
	Volume     int                 `json:"volume"`
	YearBegan  int                 `json:"year_began"`
	Genres     []metronGenericItem `json:"genres"`
	SeriesType metronGenericItem   `json:"series_type"`
}

type metronCredit struct {
	Creator string              `json:"creator"`
	Role    []metronGenericItem `json:"role"`
}

type metronSeriesList struct {
	Results []metronSeriesResult `json:"results"`
}

type metronSeriesResult struct {
	ID         int    `json:"id"`
	Series     string `json:"series"` // display name
	YearBegan  int    `json:"year_began"`
	IssueCount int    `json:"issue_count"`
}

// --- Provider implementation ---

func (m *metron) SearchSeries(ctx context.Context, query string) ([]SeriesResult, error) {
	q := url.Values{}
	q.Set("name", query)
	var list metronSeriesList
	if err := m.get(ctx, "/series/", q, &list); err != nil {
		return nil, err
	}
	out := make([]SeriesResult, 0, len(list.Results))
	for _, r := range list.Results {
		out = append(out, SeriesResult{
			ID: strconv.Itoa(r.ID),
			// Metron's display name embeds the year ("Saga (2014)"); strip it so
			// Name is bare per the SeriesResult contract (year is in StartYear).
			Name:       stripSeriesYear(r.Series),
			StartYear:  r.YearBegan,
			IssueCount: r.IssueCount,
		})
	}
	return out, nil
}

var seriesYearRe = regexp.MustCompile(`\s*\((?:19|20)\d{2}\)\s*$`)

// stripSeriesYear removes a trailing " (YYYY)" from a Metron series display
// name, leaving the bare name (edition markers like "TPB" are preserved).
func stripSeriesYear(s string) string {
	return seriesYearRe.ReplaceAllString(s, "")
}

func (m *metron) IssueByNumber(ctx context.Context, seriesID, number string) (*Issue, error) {
	q := url.Values{}
	q.Set("series_id", seriesID)
	q.Set("number", number)
	var list struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := m.get(ctx, "/issue/", q, &list); err != nil {
		return nil, err
	}
	if len(list.Results) == 0 {
		return nil, fmt.Errorf("metron: no issue %q in series %s: %w", number, seriesID, ErrNotFound)
	}
	return m.IssueByID(ctx, strconv.Itoa(list.Results[0].ID))
}

// maxIssueListPages caps how many list pages ListIssues will walk, as a safety
// valve so a malformed/non-clearing "next" can never loop forever. Metron pages
// ~28 issues each, so this still covers series of several thousand issues.
const maxIssueListPages = 200

// ListIssues implements SeriesIssueLister: it returns every issue in a series as
// a (number, id) stub. It pages by constructing its own ?page=N requests rather
// than following the server-supplied "next" URL — we never fetch an arbitrary
// URL handed back in a response body; every request is built from our validated
// base URL, which keeps the auth header scoped to metron.cloud and removes any
// open-redirect/SSRF surface. Each page is cached like any other GET.
func (m *metron) ListIssues(ctx context.Context, seriesID string) ([]IssueStub, error) {
	// seriesID flows into a query parameter; constrain it to a positive integer
	// (Metron IDs are integers) so a typo or hostile value can't be smuggled in.
	if !isPositiveIntID(seriesID) {
		return nil, fmt.Errorf("metron: invalid series id %q: %w", seriesID, ErrNotFound)
	}
	var out []IssueStub
	for page := 1; page <= maxIssueListPages; page++ {
		q := url.Values{}
		q.Set("series_id", seriesID)
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		var list struct {
			Next    string `json:"next"`
			Results []struct {
				ID     int    `json:"id"`
				Number string `json:"number"`
			} `json:"results"`
		}
		if err := m.get(ctx, "/issue/", q, &list); err != nil {
			return nil, err
		}
		for _, r := range list.Results {
			out = append(out, IssueStub{ID: strconv.Itoa(r.ID), Number: r.Number})
		}
		if list.Next == "" {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("metron: series %s has no issues: %w", seriesID, ErrNotFound)
	}
	return out, nil
}

// isPositiveIntID reports whether s is a base-10 positive integer, the form of a
// Metron series/issue ID. Used to validate IDs before they reach a query.
func isPositiveIntID(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

func (m *metron) IssueByID(ctx context.Context, issueID string) (*Issue, error) {
	iss, err := m.issueDetail(ctx, issueID)
	if err != nil {
		return nil, err
	}
	return metronIssueToIssue(iss), nil
}

func (m *metron) issueDetail(ctx context.Context, issueID string) (*metronIssue, error) {
	var iss metronIssue
	if err := m.get(ctx, fmt.Sprintf("/issue/%s/", url.PathEscape(issueID)), nil, &iss); err != nil {
		return nil, err
	}
	return &iss, nil
}

// IssueByXRef implements XRefLookup for ComicVine ids via Metron's cv_id issue
// filter. The detail's own cv_id is checked against the requested id, so a
// filter the server ignored (returning unrelated issues) can never be mistaken
// for a match.
func (m *metron) IssueByXRef(ctx context.Context, otherProvider, otherID string) (*Issue, error) {
	if otherProvider != "comicvine" || !isPositiveIntID(otherID) {
		return nil, fmt.Errorf("metron: no cross-reference for %s id %q: %w", otherProvider, otherID, ErrNotFound)
	}
	q := url.Values{}
	q.Set("cv_id", otherID)
	var list struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := m.get(ctx, "/issue/", q, &list); err != nil {
		return nil, err
	}
	if len(list.Results) != 1 {
		return nil, fmt.Errorf("metron: no single issue with comicvine id %s: %w", otherID, ErrNotFound)
	}
	iss, err := m.issueDetail(ctx, strconv.Itoa(list.Results[0].ID))
	if err != nil {
		return nil, err
	}
	if strconv.Itoa(iss.CVID) != otherID {
		return nil, fmt.Errorf("metron: issue %d does not cross-reference comicvine id %s: %w", iss.ID, otherID, ErrNotFound)
	}
	return metronIssueToIssue(iss), nil
}

// metronIssueToIssue wraps the mapped metadata with Metron's id and its
// ComicVine cross-reference (when Metron records one).
func metronIssueToIssue(iss *metronIssue) *Issue {
	out := &Issue{ComicInfo: metronIssueToComicInfo(iss), ID: strconv.Itoa(iss.ID)}
	if iss.CVID > 0 {
		out.XRef = map[string]string{"comicvine": strconv.Itoa(iss.CVID)}
	}
	return out
}

// metronIssueToComicInfo maps a Metron issue onto cbz's ComicInfo, filling as
// many v2.1 fields as the API exposes. PageCount is taken from Metron here but
// is overwritten by the actual archive page count in the tag command before
// writing. This function is pure for offline testing.
func metronIssueToComicInfo(iss *metronIssue) *comicinfo.ComicInfo {
	ci := &comicinfo.ComicInfo{
		Series:          iss.Series.Name,
		Number:          iss.Number,
		AlternateNumber: strings.TrimSpace(iss.AltNumber),
		Volume:          iss.Series.Volume,
		Title:           firstNonEmpty(strings.Join(iss.StoryNames, "; "), iss.CollTitle),
		Summary:         cleanSummary(iss.Desc),
		Publisher:       iss.Publisher.Name,
		Imprint:         strings.TrimSpace(iss.Imprint.Name),
		Genre:           metronNames(iss.Series.Genres),
		Web:             iss.ResourceURL,
		PageCount:       iss.Page,
		Format:          iss.Series.SeriesType.Name,
		Characters:      metronNames(iss.Characters),
		Teams:           metronNames(iss.Teams),
		StoryArc:        metronNames(iss.Arcs),
		AgeRating:       mapMetronRating(iss.Rating.Name),
		GTIN:            firstNonEmpty(iss.UPC, iss.ISBN),
	}
	// Store (on-sale) date first: cover dates are usually a nominal 1st-of-month.
	if y, mo, d, ok := parseISODate(firstNonEmpty(iss.StoreDate, iss.CoverDate)); ok {
		ci.Year, ci.Month, ci.Day = y, mo, d
	}
	assignCredits(ci, iss.Credits)
	return ci
}

// assignCredits maps Metron credit roles onto the ComicInfo creator fields,
// accumulating multiple names per role as a comma-separated list.
func assignCredits(ci *comicinfo.ComicInfo, credits []metronCredit) {
	for _, c := range credits {
		for _, r := range c.Role {
			applyCredit(ci, r.Name, c.Creator)
		}
	}
}

// applyCredit routes one (role, creator) pair onto the matching ComicInfo
// creator field, appending to any names already there. It is shared by the
// Metron and ComicVine backends so role mapping stays identical across sources;
// unknown roles and blank creators are ignored.
func applyCredit(ci *comicinfo.ComicInfo, role, creator string) {
	creator = strings.TrimSpace(creator)
	if creator == "" {
		return
	}
	add := func(dst *string) {
		if *dst == "" {
			*dst = creator
		} else {
			*dst += ", " + creator
		}
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "writer", "script", "story":
		add(&ci.Writer)
	case "penciller", "penciler", "pencils", "artist":
		add(&ci.Penciller)
	case "inker", "inks":
		add(&ci.Inker)
	case "colorist", "colourist", "colors", "colours":
		add(&ci.Colorist)
	case "letterer", "letters":
		add(&ci.Letterer)
	case "cover", "cover artist":
		add(&ci.CoverArtist)
	case "editor":
		add(&ci.Editor)
	case "translator":
		add(&ci.Translator)
	}
}

// parseISODate parses a "YYYY-MM-DD" date into its parts.
func parseISODate(s string) (year, month, day int, ok bool) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, 0, 0, false
	}
	return t.Year(), int(t.Month()), t.Day(), true
}

// cleanSummary normalizes a description's line endings: Metron returns literal
// CRLF, which round-trips into ComicInfo.xml as "&#xD;&#xA;". We drop the CRs
// (keeping LF so paragraphs survive) and trim surrounding whitespace.
func cleanSummary(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

// firstNonEmpty returns the first non-empty string in vals.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// metronNames joins the non-empty names of generic items with ", ".
func metronNames(items []metronGenericItem) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		if it.Name != "" {
			names = append(names, it.Name)
		}
	}
	return strings.Join(names, ", ")
}

// mapMetronRating maps Metron's rating onto the ComicInfo AgeRating enum,
// best-effort. Unknown/unmapped values yield "" (the element is then omitted).
func mapMetronRating(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "everyone":
		return "Everyone"
	case "teen":
		return "Teen"
	case "teen plus":
		return "Mature 17+"
	case "mature":
		return "Mature 17+"
	case "explicit", "adult":
		return "Adults Only 18+"
	default:
		return ""
	}
}

// rateLimiter enforces a minimum interval between calls.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
}

func newRateLimiter(d time.Duration) *rateLimiter { return &rateLimiter{interval: d} }

func (r *rateLimiter) wait(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.last.IsZero() {
		if d := time.Until(r.last.Add(r.interval)); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	r.last = time.Now()
	return nil
}

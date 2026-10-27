package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"codeberg.org/Nikode/cbztool/internal/comicinfo"
)

const comicvineBaseURL = "https://comicvine.gamespot.com/api"

// ComicVine velocity throttle: the API blocks clients that burst, recommending
// roughly one request per second (separate from the 200/resource/hour budget).
// A 1s minimum interval keeps us comfortably under the velocity limit.
const comicvineInterval = 1 * time.Second

func init() {
	registerProvider("comicvine", func(cfg ProviderConfig) (Provider, error) {
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("comicvine requires an API key (credentials.comicvine.api_key_file in the config, or CBZ_COMICVINE_API_KEY)")
		}
		return newComicVine(cfg.APIKey, cfg.CacheTTL), nil
	})
}

// comicvine is a Provider backed by the ComicVine API
// (comicvine.gamespot.com/api). It authenticates with an api_key query
// parameter, self-throttles to respect ComicVine's velocity limit, reuses the
// shared retry/backoff (apiGet) and the same two-layer (in-process + on-disk)
// response cache as the Metron backend.
//
// ComicVine maps cleanly onto cbz's model: a "volume" is our series, and an
// "issue" is our issue. One ComicVine quirk drives the design here — errors are
// reported in the JSON envelope's status_code with an HTTP 200, so get inspects
// status_code (1 = OK, 101 = not found, 100 = bad key) rather than trusting the
// HTTP status alone, and never caches a non-OK body.
type comicvine struct {
	apiKey  string
	baseURL string
	http    *http.Client
	limiter *rateLimiter
	disk    *diskCache
	refresh bool // when true, ignore disk-cache reads for this run

	mu    sync.Mutex
	cache map[string][]byte // url -> raw OK response body, for the current run
}

func newComicVine(apiKey string, cacheTTL time.Duration) *comicvine {
	return &comicvine{
		apiKey:  apiKey,
		baseURL: comicvineBaseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
		limiter: newRateLimiter(comicvineInterval),
		disk:    newDiskCache("comicvine", cacheTTL),
		cache:   make(map[string][]byte),
	}
}

// SetRefresh implements CacheControl.
func (c *comicvine) SetRefresh(v bool) { c.refresh = v }

func (c *comicvine) Name() string { return "comicvine" }

// get performs a cached, throttled, retrying GET and decodes the result. It
// adds the api_key and format=json, validates ComicVine's envelope status_code
// before decoding, and caches only confirmed-OK bodies (so a transient
// not-found or an invalid key is never persisted for the cache TTL). The api_key
// rides in the URL but is hashed away in the disk-cache filename and is kept out
// of every error message (which name only the path).
func (c *comicvine) get(ctx context.Context, path string, query url.Values, out any) error {
	q := url.Values{}
	maps.Copy(q, query)
	q.Set("api_key", c.apiKey)
	q.Set("format", "json")
	u := c.baseURL + path + "?" + q.Encode()

	body, cached := c.cacheGet(u)
	if !cached && !c.refresh {
		if b, ok := c.disk.get(u); ok {
			c.cachePut(u, b)
			body, cached = b, true
		}
	}
	fresh := false
	if !cached {
		b, err := apiGet(ctx, "comicvine", c.http, c.limiter, u, path, nil)
		if err != nil {
			return err
		}
		body, fresh = b, true
	}

	// ComicVine returns errors as a status_code in the envelope (with HTTP 200),
	// so validate it before decoding results.
	var env struct {
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("comicvine: GET %s: invalid response: %w", path, err)
	}
	switch env.StatusCode {
	case 1: // OK
	case 101: // object not found
		return fmt.Errorf("comicvine: %s: not found: %w", path, ErrNotFound)
	case 100: // invalid api key
		return fmt.Errorf("comicvine: invalid API key (check comicvine.api_key)")
	default:
		msg := env.Error
		if msg == "" {
			msg = "request failed"
		}
		return fmt.Errorf("comicvine: %s: %s (status %d)", path, msg, env.StatusCode)
	}

	if fresh {
		c.cachePut(u, body)
		c.disk.put(u, body)
	}
	return json.Unmarshal(body, out)
}

func (c *comicvine) cacheGet(u string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.cache[u]
	return b, ok
}

func (c *comicvine) cachePut(u string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[u] = body
}

// --- JSON shapes (subset of the ComicVine volume/issue schema) ---

type cvNamed struct {
	Name string `json:"name"`
}

type cvVolumeResult struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	StartYear     string `json:"start_year"` // ComicVine returns the year as a string
	CountOfIssues int    `json:"count_of_issues"`
	Publisher     struct {
		Name string `json:"name"`
	} `json:"publisher"`
}

type cvIssue struct {
	ID          int    `json:"id"`
	IssueNumber string `json:"issue_number"`
	Name        string `json:"name"`
	CoverDate   string `json:"cover_date"`
	StoreDate   string `json:"store_date"`
	Deck        string `json:"deck"`
	Description string `json:"description"` // HTML
	SiteDetail  string `json:"site_detail_url"`
	Volume      struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"volume"`
	PersonCredits []struct {
		Name string `json:"name"`
		Role string `json:"role"` // comma-separated, e.g. "writer, cover"
	} `json:"person_credits"`
	CharacterCredits []cvNamed `json:"character_credits"`
	TeamCredits      []cvNamed `json:"team_credits"`
	StoryArcCredits  []cvNamed `json:"story_arc_credits"`
	LocationCredits  []cvNamed `json:"location_credits"`
}

// --- Provider implementation ---

// SearchSeries searches ComicVine for volumes matching query. It uses the
// /search endpoint (fuzzy, relevance-ranked) for discovery rather than
// /volumes/?filter=name: — ComicVine's name filter is near-literal and misses a
// volume whose punctuation differs from the filename (e.g. a filename's " - "
// vs. the volume's ": "). Recall comes from the fuzzy search; precision is still
// the command layer's exact, normalized name match — so this never loosens
// matching, it only ensures the right candidate is in the set to be matched.
func (c *comicvine) SearchSeries(ctx context.Context, query string) ([]SeriesResult, error) {
	q := url.Values{}
	q.Set("resources", "volume")
	q.Set("query", query)
	q.Set("field_list", "id,name,start_year,count_of_issues,publisher")
	q.Set("limit", "10") // ComicVine caps /search at 10
	var resp struct {
		Results []cvVolumeResult `json:"results"`
	}
	if err := c.get(ctx, "/search/", q, &resp); err != nil {
		return nil, err
	}
	out := make([]SeriesResult, 0, len(resp.Results))
	for _, v := range resp.Results {
		year, _ := strconv.Atoi(strings.TrimSpace(v.StartYear))
		out = append(out, SeriesResult{
			ID:         strconv.Itoa(v.ID),
			Name:       strings.TrimSpace(v.Name),
			StartYear:  year,
			Publisher:  strings.TrimSpace(v.Publisher.Name),
			IssueCount: v.CountOfIssues,
		})
	}
	return out, nil
}

// IssueByNumber finds the one issue of a volume with the given number, then
// fetches its full metadata.
func (c *comicvine) IssueByNumber(ctx context.Context, seriesID, number string) (*Issue, error) {
	if !isPositiveIntID(seriesID) {
		return nil, fmt.Errorf("comicvine: invalid volume id %q: %w", seriesID, ErrNotFound)
	}
	q := url.Values{}
	q.Set("filter", "volume:"+seriesID+",issue_number:"+number)
	q.Set("field_list", "id")
	var resp struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/issues/", q, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, fmt.Errorf("comicvine: no issue %q in volume %s: %w", number, seriesID, ErrNotFound)
	}
	return c.IssueByID(ctx, strconv.Itoa(resp.Results[0].ID))
}

// IssueByID fetches one issue's full metadata. ComicVine detail URLs prefix the
// id with the resource-type code (4000 for issues); a bare id or a full
// "4000-<id>" are both accepted from the command line.
func (c *comicvine) IssueByID(ctx context.Context, issueID string) (*Issue, error) {
	id := normalizeCVID(issueID)
	if !isPositiveIntID(id) {
		return nil, fmt.Errorf("comicvine: invalid issue id %q: %w", issueID, ErrNotFound)
	}
	var resp struct {
		Results cvIssue `json:"results"`
	}
	if err := c.get(ctx, "/issue/4000-"+id+"/", nil, &resp); err != nil {
		return nil, err
	}
	ci := cvIssueToComicInfo(&resp.Results)
	if vol := resp.Results.Volume.ID; vol > 0 {
		ci.Publisher = c.volumePublisher(ctx, vol)
	}
	return &Issue{ComicInfo: ci, ID: strconv.Itoa(resp.Results.ID)}, nil
}

// volumePublisher returns a volume's publisher name, which ComicVine keeps on
// the volume rather than the issue. It is one small request per volume (cached
// like every other GET, so a whole series costs one call) and best-effort:
// Publisher is optional, so a failure here never fails the issue.
func (c *comicvine) volumePublisher(ctx context.Context, volumeID int) string {
	q := url.Values{}
	q.Set("field_list", "publisher")
	var resp struct {
		Results struct {
			Publisher struct {
				Name string `json:"name"`
			} `json:"publisher"`
		} `json:"results"`
	}
	if err := c.get(ctx, fmt.Sprintf("/volume/4050-%d/", volumeID), q, &resp); err != nil {
		return ""
	}
	return strings.TrimSpace(resp.Results.Publisher.Name)
}

// normalizeCVID strips an optional ComicVine resource-type prefix ("4000-")
// from an id, leaving the bare numeric id.
func normalizeCVID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.LastIndex(id, "-"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// cvMaxIssuePages caps how many list pages ListIssues will walk (a safety valve
// against a non-terminating pagination loop). At 100 issues/page this covers
// volumes of several thousand issues.
const cvMaxIssuePages = 200

// cvPageLimit is ComicVine's maximum page size for list endpoints.
const cvPageLimit = 100

// ListIssues implements SeriesIssueLister: every issue of a volume as a
// (number, id) stub. It pages with its own offset/limit requests rather than
// following any URL handed back in a response, so the api_key stays scoped to
// comicvine.gamespot.com and there is no open-redirect/SSRF surface.
func (c *comicvine) ListIssues(ctx context.Context, seriesID string) ([]IssueStub, error) {
	if !isPositiveIntID(seriesID) {
		return nil, fmt.Errorf("comicvine: invalid volume id %q: %w", seriesID, ErrNotFound)
	}
	var out []IssueStub
	for page := range cvMaxIssuePages {
		q := url.Values{}
		q.Set("filter", "volume:"+seriesID)
		q.Set("field_list", "id,issue_number")
		q.Set("limit", strconv.Itoa(cvPageLimit))
		q.Set("offset", strconv.Itoa(page*cvPageLimit))
		var resp struct {
			NumberOfTotalResults int `json:"number_of_total_results"`
			Offset               int `json:"offset"`
			Results              []struct {
				ID          int    `json:"id"`
				IssueNumber string `json:"issue_number"`
			} `json:"results"`
		}
		if err := c.get(ctx, "/issues/", q, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			out = append(out, IssueStub{ID: strconv.Itoa(r.ID), Number: r.IssueNumber})
		}
		if len(resp.Results) == 0 || resp.Offset+len(resp.Results) >= resp.NumberOfTotalResults {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("comicvine: volume %s has no issues: %w", seriesID, ErrNotFound)
	}
	return out, nil
}

// cvIssueToComicInfo maps a ComicVine issue onto cbz's ComicInfo. PageCount is
// not exposed by ComicVine (and is overwritten from the archive before writing
// anyway); Publisher lives on the volume, not the issue, so IssueByID fills it
// separately (volumePublisher). This function is pure for offline testing.
func cvIssueToComicInfo(iss *cvIssue) *comicinfo.ComicInfo {
	ci := &comicinfo.ComicInfo{
		Series:     strings.TrimSpace(iss.Volume.Name),
		Number:     strings.TrimSpace(iss.IssueNumber),
		Title:      strings.TrimSpace(iss.Name),
		Summary:    firstNonEmpty(stripHTML(iss.Description), strings.TrimSpace(iss.Deck)),
		Web:        strings.TrimSpace(iss.SiteDetail),
		Characters: cvNames(iss.CharacterCredits),
		Teams:      cvNames(iss.TeamCredits),
		StoryArc:   cvNames(iss.StoryArcCredits),
		Locations:  cvNames(iss.LocationCredits),
	}
	// Store (on-sale) date first: cover dates are usually a nominal 1st-of-month.
	if y, mo, d, ok := parseISODate(firstNonEmpty(iss.StoreDate, iss.CoverDate)); ok {
		ci.Year, ci.Month, ci.Day = y, mo, d
	}
	for _, person := range iss.PersonCredits {
		for role := range strings.SplitSeq(person.Role, ",") {
			applyCredit(ci, role, person.Name)
		}
	}
	return ci
}

// cvNames joins the non-empty names of credited items with ", ".
func cvNames(items []cvNamed) string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		if n := strings.TrimSpace(it.Name); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

var (
	// htmlVoidRe strips <script>/<style> blocks including their contents, so
	// embedded code contributes no text at all.
	htmlVoidRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>`)
	// htmlParaRe matches block-level closings that should read as a paragraph
	// break (a blank line); htmlBreakRe matches softer breaks (a single newline).
	htmlParaRe  = regexp.MustCompile(`(?i)</p>|</div>|</h[1-6]>`)
	htmlBreakRe = regexp.MustCompile(`(?i)<br\s*/?>|</li>`)
	htmlTagRe   = regexp.MustCompile(`<[^>]*>`)
	htmlSpaceRe = regexp.MustCompile(`[ \t]+`)
	htmlBlankRe = regexp.MustCompile(`\n{3,}`)
)

// stripHTML reduces ComicVine's HTML description to plain text: <script>/<style>
// blocks (tags and contents) are removed, block closings become paragraph or
// line breaks, all remaining tags are dropped, entities are unescaped, and
// whitespace is tidied. Because tags are removed rather than rendered, embedded
// markup cannot survive into the written XML.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = htmlVoidRe.ReplaceAllString(s, "")
	s = htmlParaRe.ReplaceAllString(s, "\n\n")
	s = htmlBreakRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = htmlSpaceRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = htmlBlankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}

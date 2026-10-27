package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// newTestComicVine builds a comicvine pointed at srv with rate-limiting disabled
// and no disk cache, so tests run fast and never touch the real ~/.cache.
func newTestComicVine(srv *httptest.Server) *comicvine {
	return &comicvine{
		apiKey:  "secret-key",
		baseURL: srv.URL,
		http:    srv.Client(),
		limiter: newRateLimiter(0),
		cache:   make(map[string][]byte),
	}
}

// sampleCVIssue mirrors the subset of fields cbz consumes from a ComicVine
// /issue/4000-{id}/ response, so the JSON tags and mapping are verified offline.
const sampleCVIssue = `{
  "status_code": 1,
  "results": {
    "id": 1159665,
    "issue_number": "1",
    "name": "Blood and Shadow",
    "cover_date": "2024-05-01",
    "store_date": "2024-05-15",
    "deck": "A short deck.",
    "description": "<p>An <i>epic</i> tale.</p><p>Second &amp; final paragraph.</p>",
    "site_detail_url": "https://comicvine.gamespot.com/keres-blood-and-shadow-1/4000-1159665/",
    "volume": {"id": 155967, "name": "Keres: Blood and Shadow"},
    "person_credits": [
      {"name": "Jane Writer", "role": "writer, cover"},
      {"name": "Sam Artist", "role": "artist"},
      {"name": "Pat Letterer", "role": "letterer"}
    ],
    "character_credits": [{"name": "Keres"}, {"name": "Shade"}],
    "team_credits": [{"name": "The Underworld"}],
    "story_arc_credits": [{"name": "Descent"}],
    "location_credits": [{"name": "Tartarus"}, {"name": "Olympus"}]
  }
}`

// sampleCVVolume is the /volume/4050-{id}/?field_list=publisher response
// IssueByID reads the publisher from.
const sampleCVVolume = `{"status_code": 1, "results": {"publisher": {"name": "Vault Comics"}}}`

func TestCVIssueMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "secret-key" {
			t.Errorf("api_key not sent")
		}
		switch r.URL.Path {
		case "/issue/4000-1159665/":
			w.Write([]byte(sampleCVIssue))
		case "/volume/4050-155967/":
			w.Write([]byte(sampleCVVolume))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestComicVine(srv)
	// A "4000-"-prefixed id from the command line must be accepted too.
	ci, err := c.IssueByID(context.Background(), "4000-1159665")
	if err != nil {
		t.Fatalf("IssueByID: %v", err)
	}

	checks := map[string]struct{ got, want string }{
		"ID":          {ci.ID, "1159665"},
		"Publisher":   {ci.Publisher, "Vault Comics"},
		"Locations":   {ci.Locations, "Tartarus, Olympus"},
		"Series":      {ci.Series, "Keres: Blood and Shadow"},
		"Number":      {ci.Number, "1"},
		"Title":       {ci.Title, "Blood and Shadow"},
		"Summary":     {ci.Summary, "An epic tale.\n\nSecond & final paragraph."},
		"Web":         {ci.Web, "https://comicvine.gamespot.com/keres-blood-and-shadow-1/4000-1159665/"},
		"Characters":  {ci.Characters, "Keres, Shade"},
		"Teams":       {ci.Teams, "The Underworld"},
		"StoryArc":    {ci.StoryArc, "Descent"},
		"Writer":      {ci.Writer, "Jane Writer"},
		"CoverArtist": {ci.CoverArtist, "Jane Writer"}, // comma-split role
		"Penciller":   {ci.Penciller, "Sam Artist"},    // "artist" maps to Penciller
		"Letterer":    {ci.Letterer, "Pat Letterer"},
	}
	for field, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", field, c.got, c.want)
		}
	}
	if ci.Year != 2024 || ci.Month != 5 || ci.Day != 15 {
		t.Errorf("date = %d-%d-%d, want 2024-5-15 (store date)", ci.Year, ci.Month, ci.Day)
	}
}

func TestCVSearchSeries(t *testing.T) {
	const body = `{"status_code":1,"results":[
      {"id":155967,"name":"Keres: Blood and Shadow","start_year":"2024","count_of_issues":4,
       "publisher":{"name":"Dark Horse"}}
    ]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Discovery uses the fuzzy /search endpoint scoped to volumes, not the
		// near-literal /volumes/?filter=name: (which misses punctuation variants).
		if r.URL.Path != "/search/" {
			t.Errorf("path = %q, want /search/", r.URL.Path)
		}
		if got := r.URL.Query().Get("resources"); got != "volume" {
			t.Errorf("resources = %q, want volume", got)
		}
		if got := r.URL.Query().Get("query"); got != "Keres" {
			t.Errorf("query = %q, want Keres", got)
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newTestComicVine(srv)
	results, err := c.SearchSeries(context.Background(), "Keres")
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.ID != "155967" || r.Name != "Keres: Blood and Shadow" || r.StartYear != 2024 ||
		r.IssueCount != 4 || r.Publisher != "Dark Horse" {
		t.Errorf("unexpected result: %+v", r)
	}
}

func TestCVNotFoundIsErrNotFound(t *testing.T) {
	// ComicVine reports "object not found" as status_code 101 with HTTP 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status_code":101,"error":"Object Not Found","results":[]}`))
	}))
	defer srv.Close()

	c := newTestComicVine(srv)
	_, err := c.IssueByID(context.Background(), "999")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCVInvalidKeyDoesNotCache(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"status_code":100,"error":"Invalid API Key"}`))
	}))
	defer srv.Close()

	c := newTestComicVine(srv)
	for range 2 {
		if _, err := c.SearchSeries(context.Background(), "Keres"); err == nil {
			t.Fatal("expected an invalid-key error")
		}
	}
	// A non-OK envelope must never be cached, so the second call re-hits.
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (error responses must not be cached)", got)
	}
}

func TestCVListIssuesPaginates(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.URL.Query().Get("filter"); got != "volume:42" {
			t.Errorf("filter = %q, want volume:42", got)
		}
		switch r.URL.Query().Get("offset") {
		case "0":
			// total 3, this page returns 2 -> a second page (offset 100) follows.
			w.Write([]byte(`{"status_code":1,"number_of_total_results":3,"offset":0,"results":[
              {"id":1,"issue_number":"1"},{"id":2,"issue_number":"2"}]}`))
		case "100":
			w.Write([]byte(`{"status_code":1,"number_of_total_results":3,"offset":100,"results":[
              {"id":3,"issue_number":"3"}]}`))
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer srv.Close()

	c := newTestComicVine(srv)
	stubs, err := c.ListIssues(context.Background(), "42")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(stubs) != 3 || stubs[0].ID != "1" || stubs[2].Number != "3" {
		t.Fatalf("unexpected stubs: %+v", stubs)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (two offset pages)", got)
	}
}

func TestCVListIssuesRejectsInvalidID(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"status_code":1,"results":[]}`))
	}))
	defer srv.Close()

	c := newTestComicVine(srv)
	for _, bad := range []string{"", "0", "-1", "12abc", "1 OR 1=1"} {
		if _, err := c.ListIssues(context.Background(), bad); err == nil {
			t.Errorf("ListIssues(%q) = nil error, want rejection", bad)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("server hits = %d, want 0 (invalid IDs must never reach the network)", got)
	}
}

func TestStripHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>Hello</p><p>World</p>", "Hello\n\nWorld"},
		{"Line one<br>Line two", "Line one\nLine two"},
		{"Tom &amp; Jerry", "Tom & Jerry"},
		{"<b>bold</b> and <i>italic</i>", "bold and italic"},
		{"<script>alert(1)</script>", ""}, // tag dropped, no text survives
		{"plain text", "plain text"},
	}
	for _, c := range cases {
		if got := stripHTML(c.in); got != c.want {
			t.Errorf("stripHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCanonicalProvider(t *testing.T) {
	cases := map[string]string{
		"mt":        "metron",
		"MT":        "metron",
		"  cv  ":    "comicvine",
		"comicvine": "comicvine",
		"metron":    "metron",
		"unknown":   "unknown", // passes through for New to reject
	}
	for in, want := range cases {
		if got := CanonicalProvider(in); got != want {
			t.Errorf("CanonicalProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

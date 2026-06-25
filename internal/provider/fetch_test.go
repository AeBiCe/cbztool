package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// newTestMetron builds a metron pointed at srv with rate-limiting disabled (so
// the retry/cache logic runs without real spacing delays) and no disk cache (so
// tests never touch the real ~/.cache). Set m.disk explicitly to exercise it.
func newTestMetron(srv *httptest.Server) *metron {
	return &metron{
		user:    "u",
		pass:    "p",
		baseURL: srv.URL,
		http:    srv.Client(),
		limiter: newRateLimiter(0),
		cache:   make(map[string][]byte),
	}
}

// newCountingServer returns a test server that always replies with body and an
// atomic counter of how many requests it has served.
func newCountingServer(body string) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(body))
	}))
	return srv, &hits
}

func TestFetchRetriesThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1") // honored, keeps the test ~1s
			http.Error(w, `{"detail":"Request was throttled."}`, http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	m := newTestMetron(srv)
	body, err := m.fetch(context.Background(), srv.URL, "/issue/")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (one throttle + one success)", got)
	}
}

func TestFetchUnauthorizedDoesNotRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := newTestMetron(srv)
	if _, err := m.fetch(context.Background(), srv.URL, "/issue/"); err == nil {
		t.Fatal("expected an error for 401")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (401 must not retry)", got)
	}
}

func TestGetCachesIdenticalRequests(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	m := newTestMetron(srv)
	for range 3 {
		var out struct {
			Results []any `json:"results"`
		}
		if err := m.get(context.Background(), "/series/", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (identical request should be cached)", got)
	}
}

func TestListIssuesPaginates(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// The series_id filter must always be present; pagination is driven by
		// our own ?page=N, never by following the server's "next" URL.
		if got := r.URL.Query().Get("series_id"); got != "42" {
			t.Errorf("series_id = %q, want 42", got)
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			// A non-empty "next" (an absolute, even off-host URL) must NOT be
			// fetched by the client; it should page via ?page=2 instead.
			w.Write([]byte(`{"next":"https://evil.example/api/issue/?page=2","results":[{"id":1,"number":"1"},{"id":2,"number":"2"}]}`))
		case "2":
			w.Write([]byte(`{"next":"","results":[{"id":3,"number":"3"}]}`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	m := newTestMetron(srv)
	stubs, err := m.ListIssues(context.Background(), "42")
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(stubs) != 3 {
		t.Fatalf("got %d stubs, want 3: %+v", len(stubs), stubs)
	}
	if stubs[0].ID != "1" || stubs[2].Number != "3" {
		t.Errorf("unexpected stubs: %+v", stubs)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (two pages)", got)
	}
}

func TestListIssuesRejectsInvalidID(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"next":"","results":[]}`))
	}))
	defer srv.Close()

	m := newTestMetron(srv)
	for _, bad := range []string{"", "0", "-1", "12abc", "1 OR 1=1"} {
		if _, err := m.ListIssues(context.Background(), bad); err == nil {
			t.Errorf("ListIssues(%q) = nil error, want rejection", bad)
		}
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("server hits = %d, want 0 (invalid IDs must never reach the network)", got)
	}
}

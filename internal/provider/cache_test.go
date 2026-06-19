package provider

import (
	"os"
	"testing"
	"time"
)

func TestDiskCacheRoundTrip(t *testing.T) {
	c := &diskCache{dir: t.TempDir(), ttl: time.Hour}
	const u = "https://metron.cloud/api/issue/?number=1&series_id=5"

	if _, ok := c.get(u); ok {
		t.Fatal("unexpected hit on empty cache")
	}
	c.put(u, []byte(`{"ok":true}`))
	body, ok := c.get(u)
	if !ok {
		t.Fatal("expected a hit after put")
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}
}

func TestDiskCacheExpiry(t *testing.T) {
	c := &diskCache{dir: t.TempDir(), ttl: time.Hour}
	const u = "https://metron.cloud/api/series/?name=saga"
	c.put(u, []byte(`{"stale":true}`))

	// Backdate the entry past the TTL; it should be treated as a miss and pruned.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(c.entryPath(u), old, old); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.get(u); ok {
		t.Error("stale entry should be a miss")
	}
	if _, err := os.Stat(c.entryPath(u)); !os.IsNotExist(err) {
		t.Error("stale entry should have been pruned")
	}
}

func TestDiskCacheDisabled(t *testing.T) {
	// A nil cache and an empty-dir cache must both no-op without panicking.
	var nilCache *diskCache
	if _, ok := nilCache.get("x"); ok {
		t.Error("nil cache should miss")
	}
	nilCache.put("x", []byte("y")) // must not panic

	empty := &diskCache{}
	empty.put("x", []byte("y"))
	if _, ok := empty.get("x"); ok {
		t.Error("disabled cache should miss")
	}
}

// TestGetUsesDiskCacheAcrossInstances proves the on-disk layer serves a second
// process (a fresh in-process cache) without hitting the network again.
func TestGetUsesDiskCacheAcrossInstances(t *testing.T) {
	srv, hits := newCountingServer(`{"results":[]}`)
	defer srv.Close()
	dir := t.TempDir()

	first := newTestMetron(srv)
	first.disk = &diskCache{dir: dir, ttl: time.Hour}
	var out struct {
		Results []any `json:"results"`
	}
	if err := first.get(t.Context(), "/series/", nil, &out); err != nil {
		t.Fatal(err)
	}

	// A second instance shares the disk cache but starts with an empty in-process
	// map — it must still avoid a second network call.
	second := newTestMetron(srv)
	second.disk = &diskCache{dir: dir, ttl: time.Hour}
	if err := second.get(t.Context(), "/series/", nil, &out); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (disk cache should serve the second instance)", got)
	}
}

// TestGetRefreshBypassesDiskCache proves --refresh forces a network call even
// when a fresh disk entry exists, and repopulates it.
func TestGetRefreshBypassesDiskCache(t *testing.T) {
	srv, hits := newCountingServer(`{"results":[]}`)
	defer srv.Close()
	dir := t.TempDir()

	warm := newTestMetron(srv)
	warm.disk = &diskCache{dir: dir, ttl: time.Hour}
	var out struct {
		Results []any `json:"results"`
	}
	if err := warm.get(t.Context(), "/series/", nil, &out); err != nil {
		t.Fatal(err)
	}

	refreshing := newTestMetron(srv)
	refreshing.disk = &diskCache{dir: dir, ttl: time.Hour}
	refreshing.SetRefresh(true)
	if err := refreshing.get(t.Context(), "/series/", nil, &out); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("server hits = %d, want 2 (--refresh must bypass the disk cache)", got)
	}
}

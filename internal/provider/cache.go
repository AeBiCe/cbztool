package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

// defaultCacheTTL is how long a cached API response stays fresh before cbz
// refetches it, unless the config sets providers.cache_ttl.
// Comic metadata is largely immutable once published, but corrections do happen
// upstream, so a short TTL keeps repeat/cross-run requests cheap and polite
// (Metron asks clients to cache) while letting an upstream fix surface by the
// next day. Bypass the cache for a single run with `cbz tag --refresh`.
const defaultCacheTTL = 24 * time.Hour

// diskCache is a best-effort, on-disk store of raw 200 response bodies keyed by
// request URL. It lives under $XDG_CACHE_HOME/cbz (so it is safe to delete and is
// never backed up). Every operation degrades silently to a miss on any I/O
// error: the cache must never be the reason a tag fails.
type diskCache struct {
	dir string // "" disables the cache (fall back to the network)
	ttl time.Duration
}

// newDiskCache returns a cache rooted at $XDG_CACHE_HOME/cbz/<name> (falling back
// to ~/.cache/cbz/<name>) whose entries stay fresh for ttl (defaultCacheTTL when
// zero). If the directory can't be resolved or created, the returned cache is
// disabled rather than an error — caching is an optimization, not a
// requirement.
func newDiskCache(name string, ttl time.Duration) *diskCache {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	dir, err := cacheDir(name)
	if err != nil {
		return &diskCache{}
	}
	return &diskCache{dir: dir, ttl: ttl}
}

// cacheDir resolves and creates $XDG_CACHE_HOME/cbz/<name> (or the ~/.cache
// fallback).
func cacheDir(name string) (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(base, "cbz", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// entryPath maps a URL to its cache file: <dir>/<sha256(url)>.json. Hashing
// keeps the filename fixed-length and filesystem-safe regardless of the query.
func (c *diskCache) entryPath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}

// get returns the cached body for url when present and still within the TTL.
// A missing, stale, or unreadable entry is a miss; a stale entry is pruned.
func (c *diskCache) get(url string) ([]byte, bool) {
	if c == nil || c.dir == "" {
		return nil, false
	}
	path := c.entryPath(url)
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if time.Since(info.ModTime()) > c.ttl {
		os.Remove(path) // best-effort prune
		return nil, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return body, true
}

// put stores body for url atomically (temp + rename) so a crashed or concurrent
// write never leaves a torn file that later decodes as garbage. All errors are
// ignored: a failed write simply means the next lookup is a miss.
func (c *diskCache) put(url string, body []byte) {
	if c == nil || c.dir == "" {
		return
	}
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, c.entryPath(url)); err != nil {
		os.Remove(tmpName)
	}
}

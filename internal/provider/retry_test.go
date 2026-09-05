package provider

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestRedactTransportError guards against credential leakage: a transport
// error's *url.Error carries the full request URL, which for ComicVine
// includes the api_key, and must never survive into a reported error.
func TestRedactTransportError(t *testing.T) {
	inner := errors.New("connect: connection refused")
	wrapped := &url.Error{
		Op:  "Get",
		URL: "https://comicvine.gamespot.com/api/issues/?api_key=SECRET123&format=json",
		Err: inner,
	}
	err := redactTransportError("comicvine", "/issues/", wrapped)
	if s := err.Error(); strings.Contains(s, "SECRET123") || strings.Contains(s, "api_key") {
		t.Errorf("redacted error still contains the request URL: %q", s)
	}
	if !strings.Contains(err.Error(), "/issues/") {
		t.Errorf("redacted error lost the endpoint label: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Errorf("redacted error lost the underlying cause: %v", err)
	}

	// An error without an embedded URL passes through unchanged.
	plain := errors.New("no route to host")
	if got := redactTransportError("metron", "/series/", plain); got != plain {
		t.Errorf("non-URL error was rewritten: %v", got)
	}
}

func TestRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want time.Duration
	}{
		{"absent", "", 0},
		{"seconds", "23", 23 * time.Second},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"garbage", "soon", 0},
		{"past http-date", "Wed, 21 Oct 2015 07:28:00 GMT", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.val != "" {
				h.Set("Retry-After", c.val)
			}
			if got := retryAfter(h); got != c.want {
				t.Errorf("retryAfter(%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}

	// An HTTP-date in the future yields a positive, roughly-correct wait.
	future := time.Now().Add(40 * time.Second).UTC().Format(http.TimeFormat)
	h := http.Header{}
	h.Set("Retry-After", future)
	if got := retryAfter(h); got <= 0 || got > 45*time.Second {
		t.Errorf("retryAfter(future date) = %v, want ~40s", got)
	}
}

func TestThrottleBodyWait(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{"drf detail", `{"detail":"Request was throttled. Expected available in 23 seconds."}`, 23 * time.Second},
		{"singular second", `{"detail":"available in 1 second."}`, time.Second},
		{"no number", `{"detail":"Request was throttled."}`, 0},
		{"not json", `nope`, 0},
		{"empty", ``, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := throttleBodyWait([]byte(c.body)); got != c.want {
				t.Errorf("throttleBodyWait(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestBackoffDelay(t *testing.T) {
	// A server hint is honored (and capped to backoffMax).
	if got := backoffDelay(0, 5*time.Second); got != 5*time.Second {
		t.Errorf("hint = %v, want 5s", got)
	}
	if got := backoffDelay(0, time.Hour); got != backoffMax {
		t.Errorf("oversized hint = %v, want cap %v", got, backoffMax)
	}
	// Without a hint, delays grow but never exceed backoffMax (+jitter ceiling).
	for attempt := range 8 {
		d := backoffDelay(attempt, 0)
		if d < 0 {
			t.Errorf("attempt %d: negative delay %v", attempt, d)
		}
		// Upper bound: capped base is backoffMax, jitter adds at most +base/4.
		if d > backoffMax+backoffMax/4 {
			t.Errorf("attempt %d: delay %v exceeds cap+jitter", attempt, d)
		}
	}
}

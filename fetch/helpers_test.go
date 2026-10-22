package fetch

import (
	"net/url"
	"strings"
	"testing"
)

func TestCheckRedirect(t *testing.T) {
	for _, tc := range []struct {
		to   string
		hops int
		ok   bool
	}{
		{"https://example.com/next", 0, true},
		{"http://example.com/next", 4, true},
		{"https://example.com/next", 5, false},
		{"file:///etc/passwd", 0, false},
		{"ftp://example.com/x", 1, false},
	} {
		u, err := url.Parse(tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkRedirect(u, tc.hops); (err == nil) != tc.ok {
			t.Errorf("redirect to %s after %d hops: %v", tc.to, tc.hops, err)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("short", 10); got != "short" {
		t.Errorf("Clip under the limit = %q", got)
	}
	if got := Clip("héllo wörld", 5); got != "héllo\n[truncated]" {
		t.Errorf("Clip counts characters, not bytes: %q", got)
	}
}

func TestUserAgent(t *testing.T) {
	if got := (&Fetcher{}).userAgent(); got != DefaultUserAgent {
		t.Errorf("default user agent %q", got)
	}
	if got := (&Fetcher{UserAgent: "refetch/1.2.3"}).userAgent(); got != "refetch/1.2.3" {
		t.Errorf("user agent %q", got)
	}
}

func TestDialContextRefusesLoopback(t *testing.T) {
	_, err := DialContext(t.Context(), "tcp", "127.0.0.1:9")
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("loopback dial: %v", err)
	}
}

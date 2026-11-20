package fetch

import (
	"bytes"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// challengeMarkers: Cloudflare, DataDome, PerimeterX, Imperva, Akamai, Reddit.
var challengeMarkers = []string{
	"challenge-platform", "cf-browser-verification", "cf_chl_", "<title>just a moment...</title>",
	"attention required! | cloudflare", "captcha-delivery.com", "px-captcha", "_incapsula_resource",
	"/_sec/cp_challenge", "enable javascript and cookies to continue",
	"prove your humanity",
}

const maxChallengeText = 2000

// Challenge reports whether a response is a bot challenge rather than the
// page: HTTP 503, HTTP 202 (AWS WAF answers a GET with an empty 202 and a
// challenge script), or a page with little text carrying a challenge
// marker: bot managers inject their scripts into real pages too (The
// Atlantic's articles load /cdn-cgi/challenge-platform/scripts/).
func Challenge(status int, body []byte) bool {
	if status == http.StatusServiceUnavailable || status == http.StatusAccepted {
		return true
	}
	if len(body) > 256<<10 {
		return false
	}
	lower := strings.ToLower(string(body))
	if !slices.ContainsFunc(challengeMarkers, func(m string) bool { return strings.Contains(lower, m) }) {
		return false
	}
	return len([]rune(Text(body, "text/html"))) < maxChallengeText
}

var mountPoint = regexp.MustCompile(`(?i)<div[^>]+id=["']?(root|app|__next|__nuxt|svelte|main)["']?[^>]*>\s*</div>|<app-root[^>]*>\s*</app-root>`)

var jsNotices = []string{
	"enable javascript", "javascript is required", "requires javascript", "javascript is disabled",
	"javascript must be enabled", "turn on javascript",
}

// NeedsJS reports a JavaScript-only shell: under 200 characters of text
// plus an empty mount point, an enable-JavaScript notice or over 100 KB of
// markup, or under 100 characters plus a script (a proof-of-work page too).
func NeedsJS(body []byte, contentType string) bool {
	if contentType != "" && !strings.Contains(contentType, "html") {
		return false
	}
	text := len([]rune(Text(body, "text/html")))
	if text >= 200 {
		return false
	}
	if len(body) > shellBytes || mountPoint.Match(body) || (text < 100 && bytes.Contains(bytes.ToLower(body), []byte("<script"))) {
		return true
	}
	lower := strings.ToLower(string(body))
	for _, n := range jsNotices {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

const shellBytes = 100 << 10

// EmptyRendered reports a headless Chrome DOM with under 20 characters of
// text, or under 200 in over 100 KB of markup; scripts and mount points stay.
func EmptyRendered(dom []byte) bool {
	text := len([]rune(strings.TrimSpace(Text(dom, "text/html"))))
	return text < 20 || (text < minTrimmed && len(dom) > shellBytes)
}

// Blocked reports an anti-bot refusal: HTTP 403 or 429, or a challenge.
func Blocked(status int, body []byte) bool {
	return status == http.StatusForbidden || status == http.StatusTooManyRequests || Challenge(status, body)
}

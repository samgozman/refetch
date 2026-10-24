package fetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type page struct {
	status    int
	body      string
	err       error
	ct        string // "text/html" when empty
	mitigated string
}

func fake(pages map[string]page, calls *atomic.Int32) getter {
	return func(_ context.Context, url string) (reply, error) {
		if calls != nil {
			calls.Add(1)
		}
		p, ok := pages[url]
		switch {
		case !ok:
			return reply{status: http.StatusNotFound, body: []byte("not found"), contentType: "text/plain"}, nil
		case p.err != nil:
			return reply{}, p.err
		}
		ct := p.ct
		if ct == "" {
			ct = "text/html"
		}
		return reply{p.status, []byte(p.body), ct, p.mitigated}, nil
	}
}

func TestModes(t *testing.T) {
	const u = "https://news.example.com/post"
	refused := map[string]page{u: {status: 403, body: "denied"}}
	challenge := map[string]page{u: {status: 200, body: "<html><title>Just a moment...</title>challenge-platform</html>"}}
	cf403 := map[string]page{u: {status: 403, body: "<html><title>Just a moment...</title>challenge-platform</html>"}}
	cfHeader := map[string]page{u: {status: 403, body: "<html>please wait</html>", mitigated: "challenge"}}
	pdf := map[string]page{u: {status: 200, body: "%PDF-1.7\n\x00\x01binary", ct: "application/pdf"}}
	good := map[string]page{u: {status: 200, body: "<p>release notes</p>"}}
	tlsErr := map[string]page{u: {err: errors.New("tls: handshake failure")}}
	timeout := map[string]page{u: {err: context.DeadlineExceeded}}
	dnsErr := map[string]page{u: {err: errors.New("dial tcp: lookup news.example.com: no such host")}}
	cases := map[string]struct {
		mode          string
		plain, chrome map[string]page
		wantErr       string
		wantClients   []string
		wantOutcomes  []string
	}{
		"always, ok":                   {"", nil, good, "", []string{ClientChromeLike}, []string{OutcomeOK}},
		"always, refused":              {"", good, refused, "refused (HTTP 403, chrome_like client)", []string{ClientChromeLike}, []string{OutcomeRefused}},
		"always, challenge":            {"", good, challenge, "challenge page", []string{ClientChromeLike}, []string{OutcomeChallenge}},
		"always, 403 challenge page":   {"", good, cf403, "challenge page (HTTP 403", []string{ClientChromeLike}, []string{OutcomeChallenge}},
		"always, cf-mitigated":         {"", good, cfHeader, "challenge page (HTTP 403", []string{ClientChromeLike}, []string{OutcomeChallenge}},
		"always, PDF":                  {"", good, pdf, "not a web page or text (application/pdf)", []string{ClientChromeLike}, []string{OutcomeError}},
		"fallback on 403 challenge":    {ImpersonateFallback, cf403, good, "", []string{ClientPlain, ClientChromeLike}, []string{OutcomeChallenge, OutcomeOK}},
		"always, TLS error to backup":  {"", good, tlsErr, "", []string{ClientChromeLike, ClientPlainBackup}, []string{OutcomeError, OutcomeOK}},
		"always, no backup on timeout": {"", good, timeout, "deadline", []string{ClientChromeLike}, []string{OutcomeTimeout}},
		"always, no backup on DNS":     {"", good, dnsErr, "no such host", []string{ClientChromeLike}, []string{OutcomeError}},
		"always, 404":                  {"", good, map[string]page{}, "HTTP 404", []string{ClientChromeLike}, []string{OutcomeError}},
		"fallback rescues":             {ImpersonateFallback, refused, good, "", []string{ClientPlain, ClientChromeLike}, []string{OutcomeRefused, OutcomeOK}},
		"fallback on challenge":        {ImpersonateFallback, challenge, good, "", []string{ClientPlain, ClientChromeLike}, []string{OutcomeChallenge, OutcomeOK}},
		"fallback skips 404":           {ImpersonateFallback, map[string]page{}, good, "HTTP 404", []string{ClientPlain}, []string{OutcomeError}},
		"off":                          {ImpersonateOff, refused, good, "refused", []string{ClientPlain}, []string{OutcomeRefused}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var got []Attempt
			f := &Fetcher{Impersonate: tc.mode, Record: func(a Attempt) { got = append(got, a) },
				plain: fake(tc.plain, nil), chrome: fake(tc.chrome, nil)}
			p, err := f.Get(t.Context(), u)
			if tc.wantErr == "" && (err != nil || !strings.Contains(string(p.Body), "release notes")) {
				t.Errorf("body = %q, err = %v", p.Body, err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
			if len(got) != len(tc.wantClients) || len(p.Attempts) != len(got) {
				t.Fatalf("records = %+v, page attempts = %+v", got, p.Attempts)
			}
			for i, a := range got {
				if a.Client != tc.wantClients[i] || a.Outcome != tc.wantOutcomes[i] || a.Host != "news.example.com" {
					t.Errorf("attempt %d = %+v", i, a)
				}
			}
			if p.Last != got[len(got)-1] {
				t.Errorf("Last = %+v", p.Last)
			}
		})
	}
}

func TestRobots(t *testing.T) {
	var calls atomic.Int32
	pages := map[string]page{
		"https://news.example.com/robots.txt": {status: 200, body: "User-agent: *\nDisallow: /private\n"},
		"https://news.example.com/post":       {status: 200, body: "ok"},
		"https://news.example.com/private/x":  {status: 200, body: "ok"},
		"https://docs.example.com/robots.txt": {status: 500},
		"https://docs.example.com/post":       {status: 200, body: "ok"},
	}
	f := &Fetcher{RespectRobots: true, Impersonate: ImpersonateOff, plain: fake(pages, &calls)}
	if _, err := f.Get(t.Context(), "https://news.example.com/post"); err != nil {
		t.Errorf("allowed page: %v", err)
	}
	p, err := f.Get(t.Context(), "https://news.example.com/private/x")
	if err == nil || !strings.Contains(err.Error(), "robots.txt") || p.Last.Outcome != OutcomeDisallowed {
		t.Errorf("disallowed page: %v, %+v", err, p.Last)
	}
	if calls.Load() != 2 {
		t.Errorf("robots.txt must be cached per host: %d calls", calls.Load())
	}
	if _, err := f.Get(t.Context(), "https://docs.example.com/post"); err == nil {
		t.Error("a 5xx robots.txt disallows everything")
	}
	f = &Fetcher{Impersonate: ImpersonateOff, plain: fake(pages, nil)}
	if _, err := f.Get(t.Context(), "https://news.example.com/private/x"); err != nil {
		t.Errorf("robots.txt is not consulted by default: %v", err)
	}
}

func TestChallenge(t *testing.T) {
	if !Challenge(503, nil) || !Challenge(200, []byte(`<div id="px-captcha">`)) || Challenge(200, []byte("<p>pricing</p>")) || Challenge(404, nil) {
		t.Error("Challenge misclassifies")
	}
	big := strings.Repeat("a", 300<<10) + "challenge-platform"
	if Challenge(200, []byte(big)) {
		t.Error("a large real page mentioning a marker is not a challenge")
	}
	if !Blocked(403, nil) || !Blocked(429, nil) || Blocked(500, nil) {
		t.Error("Blocked misclassifies")
	}
}

func TestRejectsNonHTTP(t *testing.T) {
	f := &Fetcher{}
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "example.com", ""} {
		if _, err := f.Get(t.Context(), u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestBothClientsRefuseNonPublicAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	t.Cleanup(srv.Close)
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	t.Cleanup(tls.Close)
	gets := map[string]getter{
		"plain": func(ctx context.Context, u string) (reply, error) {
			return plainGet(ctx, u, DefaultUserAgent, 5*time.Second)
		},
		"chrome": func(ctx context.Context, u string) (reply, error) {
			return chromeGet(ctx, u, 5*time.Second)
		},
	}
	for name, get := range gets {
		for _, u := range []string{srv.URL, tls.URL} {
			if r, err := get(t.Context(), u); err == nil || !strings.Contains(err.Error(), "not a public address") {
				t.Errorf("%s client fetched %s: body %q, err %v", name, u, r.body, err)
			}
		}
	}
	for _, addr := range []string{"10.0.0.1:80", "192.168.1.5:443", "169.254.169.254:80", "100.64.0.1:80", "[::1]:80", "0.0.0.0:80"} {
		if publicOnly("tcp", addr, nil) == nil {
			t.Errorf("%s must be refused", addr)
		}
	}
	if err := publicOnly("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public address refused: %v", err)
	}
}

func TestIsPublic(t *testing.T) {
	refused := []string{
		"0.1.2.3", "10.1.1.1", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.0.0.8",
		"192.0.2.1", "192.168.1.1", "198.18.0.1", "203.0.113.5", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::127.0.0.1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe",
		"2002:7f00:1::1", "2002:c0a8:101::1", "2001:0:4136:e378::1", "64:ff9b:1::1", "fc00::1", "fd12::1",
		"fe80::1", "fec0::1", "ff02::1", "2001:db8::1", "100::1", "168.63.129.16", "2001:2::1", "3fff::1", "5f00::1",
		"fd12::1%eth0", "64:ff9b::7f00:1%eth0", "2606:4700::1111%en0",
	}
	for _, s := range refused {
		if isPublic(netip.MustParseAddr(s)) {
			t.Errorf("%s must be refused", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "1.1.1.1", "::ffff:8.8.8.8", "64:ff9b::808:808", "2002:808:808::1", "2606:4700::1111"} {
		if !isPublic(netip.MustParseAddr(s)) {
			t.Errorf("%s is public", s)
		}
	}
}

func TestPublicRefusalSkipsBackup(t *testing.T) {
	if backupWorthy(errors.New("dial tcp 127.0.0.1:443: 127.0.0.1 is not a public address")) {
		t.Error("an address refusal must not go to the plain backup")
	}
}

func TestOwnAddressRefused(t *testing.T) {
	old := interfaceAddrs
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("93.184.216.34"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("2606:4700::1111"), Mask: net.CIDRMask(64, 128)},
		}, nil
	}
	local.Lock()
	local.at = time.Time{}
	local.Unlock()
	t.Cleanup(func() {
		interfaceAddrs = old
		local.Lock()
		local.at = time.Time{}
		local.Unlock()
	})
	for _, addr := range []string{"93.184.216.34:8080", "[2606:4700::1111]:6379", "[::ffff:93.184.216.34]:80"} {
		if err := publicOnly("tcp", addr, nil); err == nil || !strings.Contains(err.Error(), "this machine's own") {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := publicOnly("tcp", "93.184.216.35:443", nil); err != nil {
		t.Errorf("a neighbour on the same network is public: %v", err)
	}
}

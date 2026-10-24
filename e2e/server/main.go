//go:build e2e

// Command server is the compatibility test's local site. Pages answer the
// Chrome-like client (a Chrome User-Agent) with the real page and every
// other client (WebFetch, context-mode's fetch) with the failure the path
// names. It writes its throwaway CA to -ca for NODE_EXTRA_CA_CERTS.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Words are the secret words the sessions must report, by path.
var words = map[string]string{"/ok": "apple", "/403": "banana", "/challenge": "damson", "/js": "elder", "/slow": "fig", "/challenge-js": "grape", "/challenge-403": "honeydew"}

const challenge = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>` +
	`<p>Verifying you are human. This may take a few seconds.</p><div id="challenge-stage"></div>` +
	`<script src="/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1"></script></body></html>`

// jsShell renders its content with JavaScript, so only the headless Chrome
// step can read it.
const jsShell = `<!doctype html><html><head><title>App</title></head><body>` +
	`<noscript>You need to enable JavaScript to run this app.</noscript><div id="root"></div>` +
	`<script>setTimeout(function(){document.getElementById("root").innerHTML=` +
	`"<article><h1>Secret word: elder</h1><p>Rendered in the browser after the page loaded.</p></article>"},300)</script></body></html>`

// jsChallenge sets a cookie and reloads, like a JavaScript bot check: the
// HTTP clients get only this page, a browser gets through.
const jsChallenge = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>` +
	`<p>Verifying you are human. This may take a few seconds.</p>` +
	`<script>document.cookie="cleared=1; path=/"; setTimeout(function(){location.reload()},300)</script></body></html>`

// real is an ordinary article page of a realistic size (a few KB).
func real(path, word string) string {
	para := `<p>These field notes describe a small orchard: the trees planted in each row, when each ` +
		`variety flowers, which rootstock it grows on and how the harvest is stored over the winter months.</p>`
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>Field notes %s</title>`+
		`<link rel="stylesheet" href="/static/site.css"></head><body><header><nav><a href="/">Home</a> `+
		`<a href="/notes">Notes</a></nav></header><main><article><h1>Secret word: %s</h1>%s</article></main>`+
		`<footer><p>Orchard notes, updated weekly.</p></footer></body></html>`, path, word, strings.Repeat(para, 12))
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	caOut := flag.String("ca", "ca.pem", "where to write the CA certificate")
	slow := flag.Duration("slow", 90*time.Second, "how long /slow stalls non-Chrome clients")
	flag.Parse()
	cert, caPEM, err := certs()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*caOut, caPEM, 0o600); err != nil {
		log.Fatal(err)
	}
	ln, err := tls.Listen("tcp", *addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ln.Addr().String()) // the runner reads the port from this line
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		word, ok := words[r.URL.Path]
		chrome := strings.Contains(r.UserAgent(), "Chrome/")
		log.Printf("%s chrome=%v ua=%q", r.URL.Path, chrome, r.UserAgent())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case !ok:
			http.NotFound(w, r)
		case r.URL.Path == "/js":
			_, _ = w.Write([]byte(jsShell))
		case r.URL.Path == "/challenge-js" || r.URL.Path == "/challenge-403":
			if c, err := r.Cookie("cleared"); err == nil && c.Value == "1" {
				_, _ = w.Write([]byte(real(r.URL.Path, word)))
				return
			}
			if r.URL.Path == "/challenge-403" { // as Cloudflare sends its managed challenge
				w.Header().Set("Cf-Mitigated", "challenge")
				w.WriteHeader(http.StatusForbidden)
			}
			_, _ = w.Write([]byte(jsChallenge))
		case chrome || r.URL.Path == "/ok":
			_, _ = w.Write([]byte(real(r.URL.Path, word)))
		case r.URL.Path == "/403":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<html><head><title>403 Forbidden</title></head><body>Access denied</body></html>"))
		case r.URL.Path == "/challenge":
			_, _ = w.Write([]byte(challenge))
		case r.URL.Path == "/slow":
			select {
			case <-time.After(*slow):
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte(real(r.URL.Path, word)))
		}
	})}
	log.Fatal(srv.Serve(ln))
}

// certs makes a throwaway CA and a 127.0.0.1 server certificate it signed.
func certs() (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "refetch e2e CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

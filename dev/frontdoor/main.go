// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command frontdoor is the local-dev HTTPS reverse proxy that gives the SPA
// a single Secure-cookie origin, matching prod topology.
//
// Two real dev gotchas made a browser session impossible before this existed
// (STATUS.md / memory margince-local-run): the session cookie is Secure, so
// the app must be served from HTTPS, and the api trusts the X-Workspace-Slug
// header only under MARGINCE_ENV=dev. This front door terminates TLS on :8080
// with an in-memory self-signed cert (no openssl, no files to manage) and
// routes the contract surface to the api while everything else — including
// Vite's HMR websocket — goes to the dev server. Open https://localhost:8080.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// apiPrefixes are the paths the api role owns; everything else is the SPA
// (served by Vite in dev). Keep this in lockstep with the server mounts in
// internal/compose — the contract surface plus the operational endpoints.
var apiPrefixes = []string{"/v1", "/oauth", "/healthz", "/readyz", "/metrics"}

func main() {
	addr := envOr("FRONTDOOR_ADDR", ":8080")
	apiTarget := envOr("FRONTDOOR_API", "http://localhost:8081")
	viteTarget := envOr("FRONTDOOR_VITE", "http://localhost:5173")

	api := mustProxy(apiTarget)
	vite := mustProxy(viteTarget)

	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasAPIPrefix(r.URL.Path) {
			api.ServeHTTP(w, r)
			return
		}
		vite.ServeHTTP(w, r)
	})

	cert, err := selfSignedCert()
	if err != nil {
		log.Fatalf("frontdoor: generating dev cert: %v", err)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("frontdoor: https://localhost%s → api %s · spa %s", addr, apiTarget, viteTarget)
	log.Printf("frontdoor: open https://localhost%s (accept the self-signed cert once)", addr)
	// ListenAndServeTLS with empty cert/key files uses TLSConfig.Certificates.
	if err := srv.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("frontdoor: %v", err)
	}
}

func hasAPIPrefix(path string) bool {
	for _, p := range apiPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// mustProxy builds a reverse proxy to target. httputil.ReverseProxy already
// forwards Connection: Upgrade (Vite's HMR websocket) since Go 1.12, so no
// special websocket handling is needed. The incoming Host is preserved so the
// SPA's window.location.origin stays https://localhost:8080 end to end.
func mustProxy(target string) *httputil.ReverseProxy {
	u, err := url.Parse(target)
	if err != nil {
		log.Fatalf("frontdoor: bad target %q: %v", target, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// A dead upstream in dev is almost always "the api/vite process
		// isn't up yet" — say so instead of a bare 502 the browser hides.
		if r.Context().Err() != nil {
			return
		}
		log.Printf("frontdoor: %s %s → %s unreachable: %v", r.Method, r.URL.Path, target, err)
		http.Error(w, "frontdoor: upstream "+target+" is not up yet — is the dev stack still starting?", http.StatusBadGateway)
	}
	return proxy
}

// selfSignedCert mints an in-memory ECDSA cert for localhost / 127.0.0.1 / ::1,
// so there is no key material on disk and nothing to gitignore. The browser
// shows a one-time "not trusted" prompt; this is a dev front door, never prod.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost", Organization: []string{"Margince dev front door"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

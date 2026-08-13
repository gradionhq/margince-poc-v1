// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// apiPrefixes are the paths that belong to the api rather than the SPA.
//
// This list mirrors frontend/vite.config.ts exactly. The dev server proxies
// these to the api role and serves everything else from the bundle; the
// desktop app must make the same split, or a route that works under `pnpm
// dev` 404s in the shipped product.
var apiPrefixes = []string{
	"/v1",
	"/readyz",
	"/healthz",
	"/metrics",
	"/mcp",
	"/oauth",
	"/.well-known",
}

// ui is the single origin the user's browser talks to: static SPA files plus
// a reverse proxy to the api.
//
// One origin is the point. The api's port is ephemeral and internal, so
// serving the SPA from it is not an option, and putting the SPA on a
// different origin would turn every API call into a cross-origin request —
// buying a CORS configuration the server has no reason to carry for a
// single-user desktop install.
type ui struct {
	layout layout
	apiURL string
	port   int
	srv    *http.Server
	errs   chan error
}

func (u *ui) baseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", u.port) }

func (u *ui) start(ctx context.Context) error {
	target, err := url.Parse(u.apiURL)
	if err != nil {
		return fmt.Errorf("parse the api address %q: %w", u.apiURL, err)
	}
	webRoot := u.layout.webRoot()
	if _, err := os.Stat(filepath.Join(webRoot, "index.html")); err != nil {
		return fmt.Errorf("the bundled frontend at %s is unusable: %w", webRoot, err)
	}

	// Unlike the internal services, this port is fixed and user-visible: the
	// browser is the only way in, so the address has to stay the same across
	// restarts for a bookmark to keep working. Refusing a taken port is
	// deliberate — silently moving would break that bookmark and leave the
	// user hunting for the new address.
	addr := fmt.Sprintf("127.0.0.1:%d", u.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf(
			"cannot listen on %s: %w\nAnother program is using port %d — quit it, or set MARGINCE_PORT in margince.env to a different port",
			addr, err, u.port,
		)
	}

	mux := http.NewServeMux()
	proxy := httputil.NewSingleHostReverseProxy(target)
	for _, prefix := range apiPrefixes {
		mux.Handle(prefix, proxy)
		mux.Handle(prefix+"/", proxy)
	}
	mux.Handle("/", spaHandler(webRoot))

	u.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	u.errs = make(chan error, 1)
	go func() {
		err := u.srv.Serve(listener)
		// A closed server is the expected outcome of quitting, not a fault.
		if errors.Is(err, http.ErrServerClosed) {
			u.errs <- nil
			return
		}
		u.errs <- err
	}()

	return waitUntil(ctx, "web ui", 15*time.Second, nil, func() error {
		return dialTCP(addr)
	})
}

// spaHandler serves the built frontend, falling back to index.html for paths
// that are client-side routes rather than files.
//
// Without the fallback, reloading the browser on any deep link — /deals, a
// record page — returns 404, because those paths only exist inside the
// router. The dev server does this implicitly; a static file server does not.
func spaHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	index := filepath.Join(root, "index.html")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Real assets keep their own content type and caching; only unknown
		// paths become the shell. Anything under the build's asset directory
		// that is missing is a genuine 404, not a route.
		clean := filepath.Clean(r.URL.Path)
		if strings.HasPrefix(clean, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		if candidate := filepath.Join(root, clean); clean != "/" && fileExists(candidate) {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// stop shuts the server down and reports whatever ListenAndServe returned, so
// a failure that happened while the app was running is not lost at quit.
func (u *ui) stop() error {
	if u.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := u.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down the web ui: %w", err)
	}
	return <-u.errs
}

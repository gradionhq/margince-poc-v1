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
	"path"
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

func (u *ui) baseURL() string { return fmt.Sprintf("http://%s:%d", loopbackHost, u.port) }

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
	addr := fmt.Sprintf("%s:%d", loopbackHost, u.port)
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
		//
		// A URL path is cleaned with path, not filepath: filepath.Clean turns
		// the separators into backslashes on Windows, so the /assets/ test
		// below would never match there and every hashed bundle would be
		// answered with index.html — a blank app, served with a 200.
		clean := path.Clean(r.URL.Path)
		if strings.HasPrefix(clean, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		// FromSlash is the other half: the cleaned URL becomes a path on this
		// filesystem only when its separators are this filesystem's.
		if clean != "/" {
			candidate := filepath.Join(root, filepath.FromSlash(clean))
			isFile, err := regularFileExists(candidate)
			if err != nil {
				// Only "it is not there" means "this is a client-side route". A
				// permission or I/O error means a shipped file cannot be READ,
				// and answering that with the shell dresses a broken
				// installation up as a working one — a 200 and a blank app.
				say("  the web files under %s cannot be read: %v\n", root, err)
				// The repo's baseline sends errors as RFC 7807 through
				// platform/httperr, and this module cannot: it is stdlib-only and
				// imports none of the backend, which is what keeps the launcher a
				// supervisor rather than a second composition root. The api's own
				// errors still go through httperr; this is the launcher's static
				// file server reporting that its own bundled files are unreadable,
				// to a browser, which needs a status far more than a media type.
				//nolint:forbidigo // stdlib-only module: platform/httperr is out of reach by design
				http.Error(w, "the installation's web files cannot be read", http.StatusInternalServerError)
				return
			}
			if isFile {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, index)
	})
}

// regularFileExists reports whether path is a regular file, distinguishing
// absent from unreadable.
//
// A bool alone cannot: it collapses "no such file", which is the SPA-route case,
// into "cannot be examined", which is a broken installation. The caller needs
// those apart, so the error is returned rather than folded into false.
func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
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

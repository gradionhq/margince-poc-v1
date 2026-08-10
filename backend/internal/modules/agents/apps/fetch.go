// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package apps

// How the api reads a view document.
//
// WHY THIS IS NOT WHAT netguard EXISTS TO STOP. netguard bounds TENANT-supplied
// hosts — "a website URL to read back, a mailbox to capture" — and refuses every
// non-public address for that reason. Here the origin is OPERATOR configuration
// and the path a compile-time constant derived from the catalog, and an operator
// running the api and the web tier on one private network is a legitimate
// deployment that RefusePrivate would break. A reader who finds an unguarded
// client beside a guarded one deserves that sentence, which is why it is here.
//
// The argument holds only while no request input can reach the URL, so Fetch
// takes a CATALOG URI and refuses anything else — there is no spelling of a
// caller-chosen path — and
// TestEveryFetchableURLIsTheConfiguredOriginPlusAConstantPath derives the whole
// reachable set from the catalog and asserts it.
//
// The policy is modelled on internal/platform/webread, which is this tree's
// pattern for an outbound GET, with one deliberate divergence: webread caps
// redirects and re-checks each hop, and this refuses them outright. The bytes
// here become executable UI inside someone's MCP host, and a hop off the
// configured origin is exactly the substitution the whole design is arranged
// against.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	// fetchTimeout bounds one document read end to end.
	fetchTimeout = 10 * time.Second
	// maxDocumentBytes caps a view. The built documents are ~11 KiB; a megabyte
	// is room for growth and still far below anything that could exhaust a boot.
	maxDocumentBytes = 1 << 20
	// documentPrefix is the path the web tier serves the views under, and the
	// only path segment this client will ever construct.
	documentPrefix = "mcp-apps"
)

// ErrNoViewsOrigin is the refusal for a fetcher with nothing configured to fetch
// from. It is an error rather than a relative request, because a relative URL
// would be retried against nothing forever and report only timeouts.
var ErrNoViewsOrigin = errors.New("crmapps: no views origin is configured")

// ErrPermanent marks a refusal that re-asking cannot change: a policy decision,
// a body that is the wrong shape, a URI nobody publishes.
//
// It exists so the startup fetch can tell "the web tier is not listening YET"
// from "this document will never be acceptable". Retrying the second for the
// length of the prime deadline delays every boot by that much, inflates the
// failure counters an alert is set against, and cannot succeed.
var ErrPermanent = errors.New("crmapps: refused for a reason re-asking cannot change")

// Fetcher reads view documents from the configured origin.
type Fetcher struct {
	base   *url.URL
	client *http.Client
}

// NewFetcher builds the client for one origin.
//
// The transport is EXPLICIT rather than http.DefaultTransport, so HTTP_PROXY and
// HTTPS_PROXY in the process environment cannot silently reroute the fetch: an
// operator's unrelated proxy setting must not decide where a user's rendered UI
// comes from. TLS verification is the default, deliberately unchanged.
func NewFetcher(base *url.URL) *Fetcher {
	return &Fetcher{
		base: base,
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: fetchTimeout,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
			},
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("crmapps: the views origin redirected to %s; a view is a "+
					"first-party document at a constant path and must not redirect", req.URL.Redacted())
			},
		},
	}
}

// configured reports whether this fetcher has an origin to read from. A Fetcher
// built around a nil base is a real value that can answer nothing, which is not
// the same as no fetcher at all — and telling them apart is what lets Prime
// report a misconfiguration instead of recording two views that "did not answer".
func (f *Fetcher) configured() bool { return f != nil && f.base != nil }

// documentURL is the ONE place a URL is built, and it is built from parsed URLs
// rather than by concatenating strings — a base with or without a trailing slash
// yields the same path either way, where concatenation yields a doubled or
// missing separator.
//
// The file name is DERIVED from the URI (ui://margince/account-brief.html is
// served at /mcp-apps/account-brief.html) rather than listed beside it, so there
// is no second list to keep true.
func (f *Fetcher) documentURL(uri string) (*url.URL, error) {
	if f.base == nil {
		return nil, ErrNoViewsOrigin
	}
	if !published(uri) {
		return nil, fmt.Errorf("%w: %q is not a view this server publishes", ErrPermanent, uri)
	}
	return f.base.JoinPath(documentPrefix, path.Base(uri)), nil
}

// published reports whether the catalog claims this URI. It is what closes the
// URL set: Fetch cannot be asked for anything else.
func published(uri string) bool {
	for _, v := range catalog {
		if v.uri == uri {
			return true
		}
	}
	return false
}

// Fetch reads one view document.
func (f *Fetcher) Fetch(ctx context.Context, uri string) (string, error) {
	target, err := f.documentURL(uri)
	if err != nil {
		return "", err
	}
	if err := requireSecureTransport(target); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("crmapps: building the request for %s: %w", uri, err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("crmapps: fetching %s: %w", target.Redacted(), err)
	}
	defer func() {
		// Drained up to the cap and closed. The drain is what lets a connection
		// be reused, and it is BOUNDED on purpose: an origin answering more than
		// the cap costs this process a fresh connection next time rather than an
		// unbounded read, which is the right way round. Neither result is worth
		// reporting — the body has already been judged, or refused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDocumentBytes))
		_ = resp.Body.Close()
	}()
	if err := requireHTMLDocument(resp, target); err != nil {
		return "", err
	}
	return readCapped(resp.Body, target)
}

// ValidateOrigin holds a configured origin to the SAME transport rule every
// fetch applies, so an operator learns at boot rather than from two views that
// silently never appear.
//
// It exists because the two readings were drifting apart: flag parsing admitted
// any well-formed bare origin while the fetcher refused cleartext to anything
// but a literal local address, and the gap was a deployment that started
// cleanly and served nothing.
func ValidateOrigin(origin *url.URL) error {
	if origin == nil {
		return ErrNoViewsOrigin
	}
	return requireSecureTransport(origin)
}

// requireSecureTransport refuses cleartext to anywhere but a development
// address. HTTPS is what makes the origin's answer hard to substitute, and these
// bytes are executed as UI.
func requireSecureTransport(target *url.URL) error {
	if target.Scheme == "https" {
		return nil
	}
	if local(target.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: the views origin %s is cleartext and its host is not a literal loopback "+
		"or private address; use https, or name the address rather than a hostname",
		ErrPermanent, target.Redacted())
}

// local reports whether a host is one a development or single-network
// deployment legitimately serves over cleartext.
func local(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A name this process cannot judge without resolving it, and resolving
		// here would make the decision depend on DNS at the moment of the check.
		// Unknown is refused: an operator who means a private network can name
		// the address or use https.
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// requireHTMLDocument refuses anything that is not exactly a 200 HTML answer.
// An ingress SPA fallback and a CDN error page both answer a body, and one of
// them answers 200 — so the media type is checked as well as the status.
func requireHTMLDocument(resp *http.Response, target *url.URL) error {
	if resp.StatusCode != http.StatusOK {
		// A 5xx is left RETRYABLE: it is what a tier answers while it is coming
		// up, or mid-rollout. Everything else — a 404 for a document that was
		// not deployed, a 403 — will answer the same way for as long as this
		// boot lasts.
		if resp.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("crmapps: %s answered status %d %s, want 200",
				target.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		return fmt.Errorf("%w: %s answered status %d %s, want 200",
			ErrPermanent, target.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	declared := resp.Header.Get("Content-Type")
	// Parsed, not compared: a correct server is entitled to add a charset, and a
	// string comparison would refuse every answer nginx actually gives.
	mediaType, _, err := mime.ParseMediaType(declared)
	if err != nil {
		return fmt.Errorf("%w: %s declared the unreadable content type %q: %v",
			ErrPermanent, target.Redacted(), declared, err)
	}
	if !strings.EqualFold(mediaType, "text/html") {
		return fmt.Errorf("%w: %s answered %q, want text/html — an error page or an app "+
			"shell served here would otherwise enter validation as a view", ErrPermanent, target.Redacted(), mediaType)
	}
	return nil
}

// readCapped reads the body at the cap, and reads ONE byte past it so an
// oversize body is detected rather than truncated: a document silently cut short
// would be admitted as a shorter view, which is worse than a refusal because
// nothing anywhere would say what changed.
func readCapped(body io.Reader, target *url.URL) (string, error) {
	read, err := io.ReadAll(io.LimitReader(body, maxDocumentBytes+1))
	if err != nil {
		return "", fmt.Errorf("crmapps: reading %s: %w", target.Redacted(), err)
	}
	if len(read) > maxDocumentBytes {
		return "", fmt.Errorf("%w: %s answered more than %d bytes; a view is one small "+
			"self-contained document", ErrPermanent, target.Redacted(), maxDocumentBytes)
	}
	return string(read), nil
}

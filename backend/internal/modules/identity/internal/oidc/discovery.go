// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oidc

// Provider discovery (OpenID Connect Discovery 1.0) and the two caches it
// feeds. Cache lifetime follows the provider's own Cache-Control, falling
// back to a conservative default — a provider that says "cache me for a
// day" is telling us how often it rotates, and second-guessing it would
// either hammer the endpoint or hold a stale key set through a rotation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cache floors and defaults. The floor exists so a provider sending
// `no-store` cannot turn every sign-in into three round-trips; the default
// applies when it sends nothing at all.
const (
	defaultCacheTTL = time.Hour
	minCacheTTL     = 5 * time.Minute
	// failureBackoff is how long a failed discovery is remembered as failed.
	// Short enough that a provider coming back is picked up within one human
	// retry; long enough that a provider that is down costs one round-trip per
	// window rather than one per request.
	failureBackoff = 15 * time.Second
	// maxBodyBytes bounds every provider response we read. A discovery
	// document or key set is kilobytes; anything larger is a hostile or
	// broken endpoint, not a provider.
	maxBodyBytes = 1 << 20
)

// discoveryDocument is the subset of the well-known metadata this relying
// party uses. Unknown fields are ignored on purpose: the document is the
// provider's, and it grows.
type discoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// validate refuses a document that cannot carry a sign-in. The issuer
// check is the one that matters most: OIDC Discovery §4.3 requires the
// document's issuer to equal the one used to fetch it, and skipping it
// would let a redirect at the discovery URL substitute a whole provider.
func (d discoveryDocument) validate(configuredIssuer string) error {
	if d.Issuer != configuredIssuer {
		return fmt.Errorf("%w: discovery document issuer %q does not match the configured issuer %q", ErrProviderUnavailable, d.Issuer, configuredIssuer)
	}
	for _, field := range []struct{ name, value string }{
		{"authorization_endpoint", d.AuthorizationEndpoint},
		{"token_endpoint", d.TokenEndpoint},
		{"jwks_uri", d.JWKSURI},
	} {
		if !strings.HasPrefix(field.value, "https://") && !isLoopbackIssuer(field.value) {
			return fmt.Errorf("%w: discovery document %s %q is not an https endpoint", ErrProviderUnavailable, field.name, field.value)
		}
	}
	return nil
}

// discover returns the cached discovery document, fetching it when the
// cache is cold or expired.
func (p *Provider) discover(ctx context.Context) (discoveryDocument, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.doc != nil && p.now().Before(p.docUntil) {
		return *p.doc, nil
	}
	// A provider that just failed is not dialled again for a moment. The start
	// endpoint is unauthenticated and discovers on every hit, and the fetch
	// happens under this lock — so without a failure floor a blackholed
	// provider would turn every arriving request into its own 15-second wait,
	// queued behind the last one.
	if p.now().Before(p.failedUntil) {
		return discoveryDocument{}, fmt.Errorf("%w: discovery failed moments ago and is not retried yet", ErrProviderUnavailable)
	}

	var doc discoveryDocument
	ttl, err := p.fetchJSON(ctx, strings.TrimRight(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", &doc)
	if err != nil {
		p.failedUntil = p.now().Add(failureBackoff)
		return discoveryDocument{}, err
	}
	if err := doc.validate(p.cfg.Issuer); err != nil {
		p.failedUntil = p.now().Add(failureBackoff)
		return discoveryDocument{}, err
	}
	p.doc = &doc
	p.docUntil = p.now().Add(ttl)
	// A re-fetched document may point at a NEW key set; keeping the old
	// keys cached against a rotated jwks_uri is exactly the stale-key bug
	// discovery caching exists to avoid.
	if p.keys.uri != doc.JWKSURI {
		p.keys = keySet{}
	}
	return doc, nil
}

// fetchJSON performs one bounded GET and decodes the body, returning the
// cache lifetime the response asks for. Every non-200 and every transport
// failure is ErrProviderUnavailable: from a relying party's side there is
// no useful distinction between a provider that is down and one that is
// answering nonsense.
func (p *Provider) fetchJSON(ctx context.Context, endpoint string, into any) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("oidc: building request for %s: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		// The URL is safe to name (it is the operator's configured provider)
		// but the transport error is not appended: it can carry proxy and
		// internal-host detail, and this error reaches an operator log.
		return 0, fmt.Errorf("%w: %s unreachable", ErrProviderUnavailable, endpoint)
	}
	//craft:ignore swallowed-errors best-effort close of a fully-read response body; the decode result above is the outcome
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: %s answered %d", ErrProviderUnavailable, endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, fmt.Errorf("%w: reading %s: %w", ErrProviderUnavailable, endpoint, err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		// The provider's body never reaches a caller or a log — only the
		// fact that it did not decode.
		return 0, fmt.Errorf("%w: %s returned undecodable JSON", ErrProviderUnavailable, endpoint)
	}
	return cacheTTL(resp.Header.Get("Cache-Control")), nil
}

// cacheTTL reads max-age from a Cache-Control header, clamped into
// [minCacheTTL, 24h]. An absent, unparsable, or no-store header takes the
// default: this is a cache lifetime, not an authorization decision, so the
// conservative answer is a short reuse window rather than none.
func cacheTTL(header string) time.Duration {
	const maxCacheTTL = 24 * time.Hour
	for _, directive := range strings.Split(header, ",") {
		name, value, found := strings.Cut(strings.TrimSpace(directive), "=")
		if !found || !strings.EqualFold(name, "max-age") {
			continue
		}
		secs, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			// A malformed max-age is no signal at all — fall through to the
			// default rather than treating the header as authoritative.
			break
		}
		return min(max(time.Duration(secs)*time.Second, minCacheTTL), maxCacheTTL)
	}
	return defaultCacheTTL
}

// errUnknownKeyID is the one recoverable validation failure: a token signed
// with a key the cached set predates. It triggers exactly one forced JWKS
// refresh, then becomes a real refusal.
var errUnknownKeyID = errors.New("oidc: unknown signing key id")

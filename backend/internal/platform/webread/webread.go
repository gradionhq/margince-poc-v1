// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package webread is the outbound public-web fetcher behind the ADR-0006
// scrape/enrichment seam: plain GETs of pages a tenant names, reduced to
// whitespace-normalized text. It owns the HTTP mechanics and nothing else — no
// extraction, no vocabulary, no discovery policy; those stay with the callers.
//
// Three properties hold for every fetch:
//   - SSRF-guarded: the dialer refuses non-public addresses POST-dial, so a
//     DNS answer cannot steer a tenant-supplied URL into the deployment's own
//     network, and every redirect hop re-enters the guard.
//   - robots.txt honored (the ADR-0006 "robots/ToS respected" promise): a
//     path the site disallows for us is refused HERE, not left to caller
//     discipline. An unreachable robots (5xx, network) reads as deny — when a
//     site cannot say what it permits, we do not guess in our own favor; a
//     missing one (4xx) reads as allow, the standard.
//   - attributable: one named User-Agent, so a site operator can identify and
//     block the bot rather than mistaking it for a browser.
package webread

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/netguard"
)

const (
	fetchTimeout  = 10 * time.Second
	maxFetchBytes = 1 << 20 // 1 MiB per page
	// UserAgent names the bot on every request, robots.txt lookups included.
	UserAgent = "margince-siteread/1.0"
	// robotsAgentProduct is the product name robots.txt group headers match on
	// (RFC 9309 calls this the product token).
	robotsAgentProduct = "margince-siteread"
	// robotsTTL bounds how long a fetched policy is trusted; a crawl session
	// asks once, a later session re-asks.
	robotsTTL = 15 * time.Minute
)

// ErrRobotsDisallowed marks a fetch the target site's robots.txt refuses for
// this bot. Callers report it as a skip reason — it is the site's answer, not
// a failure.
var ErrRobotsDisallowed = errors.New("webread: robots.txt disallows this path")

// Fetcher is the production fetcher. Safe for concurrent use.
type Fetcher struct {
	client *http.Client

	mu     sync.Mutex
	robots map[string]robotsEntry // per scheme://host
	now    func() time.Time
}

type robotsEntry struct {
	policy  robotsPolicy
	fetched time.Time
}

// New builds the guarded fetcher.
func New() *Fetcher {
	// netguard.RefusePrivate runs in the socket's Control hook — BEFORE the
	// connect completes — matching the ratified sibling egress path (the imap
	// connector). A post-dial check would let the TCP handshake reach an
	// internal service that acts on connect, and leave connect timing as a
	// port oracle. The hook sees the literal dial address, so DNS answers
	// cannot bypass it either.
	dialer := &net.Dialer{Timeout: fetchTimeout, Control: netguard.RefusePrivate}
	return newFetcher(&http.Transport{DialContext: dialer.DialContext})
}

// newFetcher wires the client policy every fetcher shares — the timeout, the
// redirect cap, and the per-hop robots re-check — over the given transport.
// Production passes the guarded transport; tests pass an unguarded one (their
// servers live on loopback, which the guard rightly refuses) and get the SAME
// redirect/robots behavior, so what the tests prove is what production does.
func newFetcher(transport http.RoundTripper) *Fetcher {
	f := &Fetcher{
		robots: map[string]robotsEntry{},
		now:    time.Now,
	}
	f.client = &http.Client{
		Timeout:   fetchTimeout,
		Transport: transport,
		// Every redirect hop re-enters the transport's dialer, and — because
		// an allowed path may 30x onto a path (or origin) the site's robots
		// disallow — every hop re-passes the robots gate too. The robots
		// fetches themselves are exempt or a redirecting robots.txt would
		// recurse into its own policy lookup.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("webread: too many redirects")
			}
			if req.URL.Path == "/robots.txt" {
				return nil
			}
			allowed, err := f.pathAllowed(req.Context(), req.URL)
			if err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("%w: redirect target %s", ErrRobotsDisallowed, req.URL.Path)
			}
			return nil
		},
	}
	return f
}

// Fetch retrieves one page as whitespace-normalized text, refusing what the
// site's robots.txt disallows for this bot.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	page, err := f.FetchPage(ctx, rawURL)
	if err != nil {
		return "", err
	}
	return page.Text, nil
}

// get is the capped GET treating anything but a 200 as failure — the page
// fetch's reading. Sitemap and robots lookups read statuses themselves.
func (f *Fetcher) get(ctx context.Context, rawURL string) (string, error) {
	body, status, err := f.getRaw(ctx, rawURL)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("webread: page answered %d", status)
	}
	return body, nil
}

// getRaw is the network-level capped GET: body and status, no status policy.
func (f *Fetcher) getRaw(ctx context.Context, rawURL string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for the fetch result
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", 0, err
	}
	return string(body), resp.StatusCode, nil
}

// pathAllowed resolves the host's robots policy (cached per host) and asks it
// about the path.
func (f *Fetcher) pathAllowed(ctx context.Context, page *url.URL) (bool, error) {
	origin := page.Scheme + "://" + page.Host

	f.mu.Lock()
	entry, cached := f.robots[origin]
	fresh := cached && f.now().Sub(entry.fetched) < robotsTTL
	f.mu.Unlock()

	if !fresh {
		policy, err := f.fetchRobots(ctx, origin)
		if err != nil {
			return false, err
		}
		entry = robotsEntry{policy: policy, fetched: f.now()}
		f.mu.Lock()
		f.robots[origin] = entry
		f.mu.Unlock()
	}
	path := page.EscapedPath()
	if path == "" {
		path = "/"
	}
	return entry.policy.allows(path), nil
}

// fetchRobots retrieves and parses <origin>/robots.txt. A 4xx answer means the
// site declares no policy — allow-all, the standard reading. A 5xx or network
// failure is NOT an answer: it reads as deny, because "the site could not say
// what it permits" must never resolve in our own favor.
func (f *Fetcher) fetchRobots(ctx context.Context, origin string) (robotsPolicy, error) {
	//nolint:gosec // G704: fetching tenant-named hosts is this package's purpose; egress is guarded beneath — the dialer's netguard.RefusePrivate control and the per-hop robots gate — not at request construction
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/robots.txt", nil)
	if err != nil {
		return robotsPolicy{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	//nolint:gosec // G704: same guard — the transport beneath refuses non-public addresses pre-connect
	resp, err := f.client.Do(req)
	if err != nil {
		return robotsPolicy{}, fmt.Errorf("webread: robots.txt unreachable (refusing to guess what %s permits): %w", origin, err)
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for the policy
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
		if err != nil {
			return robotsPolicy{}, err
		}
		return parseRobots(string(body)), nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return robotsPolicy{}, nil // no policy declared — allow-all
	default:
		return robotsPolicy{}, fmt.Errorf("webread: robots.txt answered %d (refusing to guess what %s permits)", resp.StatusCode, origin)
	}
}

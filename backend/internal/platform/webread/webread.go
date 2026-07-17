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
	dialer := &net.Dialer{Timeout: fetchTimeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			// Checked post-dial so DNS answers cannot bypass the guard.
			if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok && !netguard.PublicIP(tcp.IP) {
				//craft:ignore swallowed-errors best-effort close of a connection being refused — the SSRF refusal below is the error that matters
				_ = conn.Close()
				return nil, fmt.Errorf("webread: refusing non-public address %s", tcp.IP)
			}
			return conn, nil
		},
	}
	return &Fetcher{
		client: &http.Client{
			Timeout:   fetchTimeout,
			Transport: transport,
			// Every redirect hop re-enters the guarded dialer; the cap bounds
			// how long a redirect chain can hold the request.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("webread: too many redirects")
				}
				return nil
			},
		},
		robots: map[string]robotsEntry{},
		now:    time.Now,
	}
}

// Fetch retrieves one page as whitespace-normalized text, refusing what the
// site's robots.txt disallows for this bot.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("webread: %q is not a fetchable URL", rawURL)
	}
	allowed, err := f.pathAllowed(ctx, parsed)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", fmt.Errorf("%w: %s", ErrRobotsDisallowed, parsed.Path)
	}

	body, err := f.get(ctx, rawURL)
	if err != nil {
		return "", err
	}
	return StripTags(body), nil
}

// get is the raw capped GET both page and robots fetches share.
func (f *Fetcher) get(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for the fetch result
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("webread: page answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/robots.txt", nil)
	if err != nil {
		return robotsPolicy{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
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

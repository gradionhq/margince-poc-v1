// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reaching a company's site when the one URL we derived from its domain does
// not answer.
//
// A domain becomes a seed as `https://<domain>` and nothing else (people's
// EnrichTargetURL, capture's auto-enrich). That is the right first guess and
// the wrong only guess: a site can serve TLS on www but not on the apex, or
// have no TLS at all. On a real import of 162 companies, 37 site reads died on
// the seed fetch having read zero pages, and half of those answer perfectly
// well on another host or scheme — so half the companies with no logo, no
// facts and no profile had a reachable website the whole time.
//
// This is the ladder a browser walks when a person types a bare domain, and
// nothing more: the same site, named the way that site actually publishes it.

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// fetchSeed gets the landing page, or reports why the site could not be read
// at all. It returns the URL that ANSWERED, which is the site's own spelling
// of itself and what the crawl treats as on-site from then on.
func (c *siteCrawler) fetchSeed(ctx context.Context, pacer crawlPacer, seedURL string) (string, webread.Page, error) {
	page, err := c.fetchPaced(ctx, pacer, seedURL)
	if transientCrawlError(ctx, err) {
		// The landing page is the only irreplaceable discovery source. One
		// immediate retry absorbs a transient edge/CDN timeout while the
		// crawl's wall deadline still bounds the attempt.
		page, err = c.fetchPaced(ctx, pacer, seedURL)
	}
	if err == nil || errors.Is(err, webread.ErrRobotsDisallowed) {
		return seedURL, page, err
	}
	for _, candidate := range seedFallbacks(seedURL) {
		if ctx.Err() != nil {
			break
		}
		retryPage, retryErr := c.fetchPaced(ctx, pacer, candidate)
		if transientCrawlError(ctx, retryErr) {
			// The same single retry the first spelling gets. A CDN timeout on
			// the www spelling is not evidence that the site is unreachable,
			// and treating it as one loses a company its whole read.
			retryPage, retryErr = c.fetchPaced(ctx, pacer, candidate)
		}
		if retryErr == nil {
			return candidate, retryPage, nil
		}
		// A refusal is the site's answer, not a spelling that failed to
		// resolve. Every remaining candidate is the SAME site under another
		// host or scheme, so trying them would be answering a "no" by
		// knocking on the next door.
		if errors.Is(retryErr, webread.ErrRobotsDisallowed) {
			return seedURL, webread.Page{}, retryErr
		}
	}
	return seedURL, webread.Page{}, err
}

// seedFallbacks returns the other spellings of a seed worth trying, in order,
// after the seed itself has failed to answer. The seed is never repeated.
//
// Only the host and the scheme vary. A different PATH would be a different
// page and a different REGISTRABLE DOMAIN a different company, so neither is a
// fallback — it would be a guess about which site we meant.
//
// Downgrading to http is last and deliberate. Plain http is worth trying
// because a small site's marketing page is public either way and the crawl
// reads nothing private; it goes last so a working https is always preferred.
func seedFallbacks(seedURL string) []string {
	parsed, err := url.Parse(seedURL)
	if err != nil || parsed.Host == "" {
		return nil
	}
	if parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS {
		return nil
	}
	// Only the `www` convention, and only ever by ADDING it or removing that
	// exact label. Stripping any other label would point at a different host
	// that may serve someone else — `app.acme.com` is not `acme.com`.
	//
	// Adding www is offered for every host, including multi-label ones:
	// counting dots called `acme.co.uk` a subdomain and skipped the www
	// spelling, which is where a good share of UK and German companies
	// actually publish. A www prefix that means nothing simply fails to
	// resolve, which costs one bounded fetch; a missing one costs the whole
	// site read.
	host := parsed.Host
	var hosts []string
	if after, found := strings.CutPrefix(host, "www."); found {
		hosts = []string{host, after}
	} else {
		hosts = []string{host, "www." + host}
	}

	var out []string
	for _, scheme := range []string{schemeHTTPS, schemeHTTP} {
		for _, h := range hosts {
			candidate := *parsed
			candidate.Scheme = scheme
			candidate.Host = h
			if spelling := candidate.String(); spelling != seedURL {
				out = append(out, spelling)
			}
		}
	}
	return out
}

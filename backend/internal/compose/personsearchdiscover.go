// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Discovering a contact's public professional address from SEARCH RESULT
// METADATA, without fetching the page it points at (ADR-0081 / A126).
//
// This is the narrow, deterministic half of search-backed enrichment, and it
// is deliberately the half that needs no model. A search index returns a
// title, a description and a URL; when that URL is a public professional
// profile, the URL itself is the fact worth keeping, and the title and
// description are the receipt. Nothing reads the profile.
//
// That distinction is the whole reason the seam was worth ratifying. The
// platform's terms prohibit automated collection, so websearch.MayFetch
// refuses it — while the address remains perfectly citable, because a
// citation asserts only that the claim appears there. Throwing the URL away
// to honour a rule about FETCHING would discard exactly the field this
// product decided was most worth carrying (ADR-0078 §8: the profile URL is
// the one thing a ghost ever contributes to a real record).
//
// The richer half — reading a role out of a snippet — needs a declared AI
// task, and ai-tasks.yaml is generated from the ratified contract. It waits
// on that reconciliation rather than being smuggled in here.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/websearch"
)

// profileHosts maps a professional platform to the person_profile_field the
// addresses on it belong in.
//
// LinkedIn alone, because `linkedin` is the only profile field the schema
// admits (migration 0097's CHECK). Xing is deny-listed for FETCHING alongside
// it, but a Xing URL has no honest home on the record — filing one under
// `linkedin` would mislabel it, and inventing a column is a contract change,
// not something this consumer may do on its own.
//
// So this is a SUBSET of websearch's fetch deny-list rather than a mirror of
// it. What the two share is the posture: every host here is one this product
// finds by searching and never reads.
var profileHosts = map[string]string{
	"linkedin.com": "linkedin",
}

// discoverProfileURL searches for this person and returns the first result
// that is unmistakably their public professional profile.
//
// "Unmistakably" is doing real work. A search for a common name returns
// profiles of other people, so the query is anchored on the employer and the
// result is only accepted when the person's name actually appears in the
// result text. A wrong profile URL on a contact is worse than none: it is a
// confident claim about a different human.
func (g *PersonAutoEnrich) discoverProfileURL(ctx context.Context, name, employer string) (people.DiscoveredField, bool, error) {
	if g.search == nil || strings.TrimSpace(name) == "" || strings.TrimSpace(employer) == "" {
		// Without an employer to anchor on there is no query worth running:
		// a bare name is precisely the case that returns somebody else.
		return people.DiscoveredField{}, false, nil
	}
	results, err := g.search.Search(ctx, websearch.Query{
		Terms:      fmt.Sprintf("%q %q linkedin", name, employer),
		MaxResults: 5,
	})
	if err != nil {
		return people.DiscoveredField{}, false, err
	}
	for _, r := range results {
		if !isProfileURL(r.URL) || !mentionsName(r, name) {
			continue
		}
		canonical, ok := canonicalProfileURL(r.URL)
		if !ok {
			continue
		}
		// The field is derived from the HOST, never assumed. Storing a
		// xing.com address under `linkedin` would put a non-LinkedIn URL in
		// front of a reader who trusts the label, and every consumer treating
		// that field as a LinkedIn handle would be wrong about it.
		field := platformField(hostOf(canonical))
		if field == "" {
			continue
		}
		return people.DiscoveredField{
			Field: field,
			Value: canonical,
			// The result's own text, verbatim: this is what the reader checks
			// the address against, and it exists without anyone having opened
			// the profile.
			EvidenceSnippet: clip(strings.TrimSpace(r.Title+" — "+r.Snippet), maxSnippetLen),
			SourceRef: fmt.Sprintf("%s:%s:%s",
				searchSourceRefPrefix, g.search.Provider(), r.RetrievedAt.Format("2006-01-02")),
		}, true, nil
	}
	return people.DiscoveredField{}, false, nil
}

// searchSourceRefPrefix names the channel a discovered value came from, so a
// stored claim can say which index answered and on what date.
const searchSourceRefPrefix = "web_search"

// The bounds on what a provider may write into a record. The search response
// is external input on its way to a stored field, so it is validated at this
// boundary rather than trusted because it arrived over TLS.
const (
	maxProfileURLLen = 300
	maxSnippetLen    = 500
)

// canonicalProfileURL reduces a result URL to the form worth storing, or
// refuses it.
//
// It keeps scheme, host and path and DROPS everything else. Three reasons,
// all of them about what ends up on a person's record: a URL carrying
// userinfo would store credentials, a query string carries the tracking
// parameters a search result is decorated with rather than anything about
// the person, and an unbounded string is a write amplification a provider
// controls. https only — a profile address served over plaintext is not one
// worth recording.
func canonicalProfileURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return "", false
	}
	clean := (&url.URL{Scheme: "https", Host: u.Host, Path: u.Path}).String()
	if len(clean) > maxProfileURLLen {
		return "", false
	}
	return clean, true
}

// hostOf reads the hostname back out of an address this package has already
// canonicalised, so the field lookup and the stored value agree on one host.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// platformField names the profile field a host's addresses belong in, or ""
// when the host is not a professional platform.
//
// The field is derived from the HOST rather than assumed: storing a xing.com
// address under `linkedin` would put a non-LinkedIn URL in front of a reader
// who trusts the label, and every consumer treating that field as a LinkedIn
// handle would be wrong about it.
func platformField(hostname string) string {
	host := strings.ToLower(strings.TrimRight(strings.TrimSpace(hostname), "."))
	for domain, field := range profileHosts {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return field
		}
	}
	return ""
}

// clip bounds provider-supplied text at a stored length. The evidence
// snippet is a receipt, not an archive: what the reader checks the value
// against fits in a sentence or two.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// Back off to a rune boundary. Cutting bytes mid-character stores invalid
	// UTF-8, and this snippet is the evidence a reader checks the value
	// against — a mangled receipt is worse than a shorter one.
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// isProfileURL reports whether a result points at a personal profile on one
// of the professional platforms — not a company page, a jobs listing or a
// post, all of which live on the same hosts.
func isProfileURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if platformField(u.Hostname()) == "" {
		return false
	}
	path := strings.ToLower(u.Path)
	// The personal-profile prefixes on these platforms. A company page
	// (/company/…) or a posting (/jobs/…) is not a person and must never be
	// filed as one.
	return strings.HasPrefix(path, "/in/") || strings.HasPrefix(path, "/profile/")
}

// mentionsName reports whether the result text actually names this person.
//
// It is the guard against confidently filing a stranger's profile onto a
// contact, so it matches whole WORDS rather than substrings. Contains() would
// accept "Marianna Weberling" for "Anna Weber" — both parts appear inside
// longer words — and the result is a real LinkedIn URL for a different human
// stored on this contact's record.
//
// Every part must appear, including one-character parts. Skipping those made
// an initial ("J. Weber") contribute nothing, so the guard reduced to a
// surname, which matches too many people at one employer to be evidence of
// identity.
func mentionsName(r websearch.Result, name string) bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(
		strings.ToLower(r.Title+" "+r.Snippet+" "+r.URL),
		func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) },
	) {
		words[w] = true
	}
	parts := strings.Fields(strings.ToLower(name))
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		part = strings.Trim(part, ".,")
		if part == "" {
			continue
		}
		if !words[part] {
			return false
		}
	}
	return true
}

// discoverFromSearch is the consumer's fallback when the employer's staged
// pages had nothing for this person: ask the index for their public
// professional address.
//
// It runs only when search is configured. A deployment that bound no
// provider skips it silently, which is the honest sovereign posture rather
// than an error on every contact creation.
func (g *PersonAutoEnrich) discoverFromSearch(ctx context.Context, personID ids.PersonID, name, employer string) error {
	field, found, err := g.discoverProfileURL(ctx, name, employer)
	if err != nil {
		// A search that failed is not a person that failed. The contact is
		// already saved; the discovery is an improvement that did not land,
		// so it is reported and the pass ends cleanly.
		g.log.WarnContext(ctx, "public profile discovery did not run",
			"person", personID.String(), "err", err)
		return nil
	}
	if !found {
		return nil
	}
	applied, err := g.people.ApplyDiscoveredFields(ctx, personID, []people.DiscoveredField{field})
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		g.log.InfoContext(ctx, "public profile address discovered by search",
			"person", personID.String(), "fields", applied, "source", field.SourceRef)
	}
	return nil
}

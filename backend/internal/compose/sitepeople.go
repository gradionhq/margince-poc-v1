// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's person lane: a crawled team
// page spends its per-page category budget on PEOPLE — who the site
// itself publishes, and nothing more. The gate is stricter than the fact
// gate because this is the NEVER-8 boundary (thin, published-only): a
// person survives only when name AND role are verbatim on the page, and a
// published_email / linkedin_url is kept only when the page prints it
// verbatim — otherwise the contact detail is stripped while the person
// survives. Nothing is fabricated, nothing enriched from elsewhere.
// Contact pages keep their company category call and get NO people call:
// one call per page, and a contact page's deliberate content is the
// company's own contact identity, not a roster.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// siteLeadProposalKind is the staged per-person proposal's wire identity —
// one spelling for the staging worker and the accept executor
// (siteleadaccept.go). One 🟡 per person: each is decided on its own.
const siteLeadProposalKind = "site_lead"

// siteLeadProposal is the thin staged payload — exactly what the site
// published, plus the provenance the accept effect and the inbox need.
type siteLeadProposal struct {
	OrganizationID  ids.UUID `json:"organization_id"`
	SiteReadID      ids.UUID `json:"site_read_id"`
	Name            string   `json:"name"`
	Role            string   `json:"role"`
	PublishedEmail  string   `json:"published_email,omitempty"`
	LinkedinURL     string   `json:"linkedin_url,omitempty"`
	EvidenceSnippet string   `json:"evidence_snippet"`
	SourceURL       string   `json:"source_url"`
}

// sitePerson is one gate-surviving published person from a team page.
// Confidence stays extraction-internal (it ranks the cross-page merge);
// the staged payload carries only what the site published.
type sitePerson struct {
	Name            string
	Role            string
	PublishedEmail  string
	LinkedinURL     string
	EvidenceSnippet string
	SourceURL       string
	Confidence      float32
}

// verbatimOrEmpty keeps a claimed contact detail only when the page text
// itself prints it — the site published it, so relaying it stays inside
// the published-only boundary. Anything else is dropped, never repaired.
func verbatimOrEmpty(claimed, pageText string) string {
	claimed = strings.TrimSpace(claimed)
	if claimed == "" || !strings.Contains(pageText, claimed) {
		return ""
	}
	return claimed
}

// normalizedPersonName is a person's dedupe identity within one read AND
// the stable half of the cross-read lead natural key: casefolded,
// whitespace collapsed, so a re-read that reflows the page cannot mint a
// second lead for the same printed name.
func normalizedPersonName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// siteLeadSourceID is the lead's idempotency key under source_system
// "siteread": the ORGANIZATION plus the normalized name (plus a published
// email when the site prints one, so two distinct people who share a name
// stay distinct). Keyed on the org, not the page URL, so the same person is
// the same lead whether they were found on /team or /about, and whether a
// later crawl's page layout moved — a page-URL key would duplicate them.
func siteLeadSourceID(orgID ids.UUID, name, publishedEmail string) string {
	key := orgID.String() + "|" + normalizedPersonName(name)
	if e := strings.ToLower(strings.TrimSpace(publishedEmail)); e != "" {
		key += "|" + e
	}
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

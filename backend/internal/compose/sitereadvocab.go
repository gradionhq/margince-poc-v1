// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's category-fact vocabulary plumbing: the closed
// vocabulary lives with the people module (organization_fact's owner);
// this file carries the per-category prompt guidance the corpus call
// embeds and the fact dedupe identity the gate and the merges share.

import (
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// The extraction envelope's JSON keys and the chat role — the same
// spellings the company-fact extraction (enrichextract.go) uses, named so
// the schema builder and the request can share them.
const (
	extractionEnvelopeKey   = "fields"
	extractionFieldKey      = "field"
	extractionValueKey      = "value"
	extractionEvidenceKey   = "evidence_snippet"
	extractionConfidenceKey = "confidence"
	chatRoleUser            = "user"
)

// categoryGuidance is the per-category slice of the extraction prompt:
// what the fields mean and, for the multi-value categories, the
// "Name — short description" value spelling NormalizeFactValueKey
// dedupes on.
var categoryGuidance = map[string]string{
	companyWord: "founded_year is the year the company was founded; employee_range a stated headcount or range; " +
		"phone and contact_email the company's own contact details; location one entry per office or site the " +
		"company states (city and country as printed).",
	"offering": "service and product name what THIS company sells, at the level it sells them — the page's own subject, as a buyer would name it on an order. " +
		"A product is software or a repeatable packaged good the buyer uses; a service is work this company performs for the buyer. Use capability when the page states an ability but does not sell it as a named offer. " +
		"A method, technique, step, phase or deliverable USED TO DELIVER one offering is not itself an offering: on a page about a research service, " +
		"the service is what the page is about, while workshops, interviews, mapping and synthesis are how it is done — omit those. " +
		"A product, platform or vendor made by SOMEONE ELSE that this company integrates, migrates, partners on or builds upon is technology, NEVER product or service, " +
		"however deeply the page describes working with it. " +
		"capability names a delivery or technical capability the company declares about ITSELF — what it can do for any client — never an implementation detail, " +
		"configuration, or feature bullet of one project, page or engagement. One entry per item, repeating the field name.",
	"market": "served_industry, company_size, geography and language describe markets the company explicitly says it serves — one entry per grounded item, repeating the field name.",
	"signal": "certification names a held certification or standard; partner a named business partner; " +
		"named_customer a customer the site names; technology a platform, product or stack the company says it " +
		"works with or builds on; quantified_outcome preserves an exact measurable customer or case-study result " +
		"without strengthening the claim — one entry per item, repeating the field name.",
}

// factKey is a fact's dedupe identity — the columns of uq_org_fact minus
// the tenant and the org, both fixed within one read.
func factKey(f people.DeepReadFact) string {
	return f.Category + "\x00" + f.Field + "\x00" + f.ValueKey
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The domain-triage classifier: what a mail domain's own site says it IS,
// before any organization is created from it.
//
// It runs on the SEED PAGE ALONE, once, before the crawl proper. That is the
// whole point — a personal homepage or a parked domain is obvious from its
// front page, and answering there costs one page instead of twelve plus a
// profile call. A company answer, or an unclear one, falls through to the full
// read, which then produces the dossier the organization is named from.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
	"github.com/gradionhq/margince/backend/internal/shared/schema"
)

// The classes a seed page can fall into.
const (
	// siteKindCompany — an organization's own site. The read continues and the
	// company is created from what it states.
	siteKindCompany = "company"
	// siteKindPersonal — one individual's site: a personal homepage, a CV, a
	// blog, or a page whose only subject is the human who owns the domain.
	siteKindPersonal = "personal"
	// siteKindProvider — a business selling mailboxes, hosting, or domains to
	// the public. The site belongs to a real company; that company is not the
	// sender's employer, which is exactly why naming an organization after it
	// is wrong. This is the live.fr class.
	siteKindProvider = "provider"
	// siteKindParked — a registrar placeholder, a "coming soon", an error page:
	// nothing that identifies anybody.
	siteKindParked = "parked"
	// siteKindUnclear — the page does not say. Falls through to the full read
	// rather than guessing.
	siteKindUnclear = "unclear"
)

// siteTriageKinds is the closed vocabulary, in schema order.
var siteTriageKinds = []string{
	siteKindCompany, siteKindPersonal, siteKindProvider, siteKindParked, siteKindUnclear,
}

// triageAbortConfidence is how sure the classifier must be before its answer
// stops a crawl. Below it the read continues and the deterministic evidence —
// a legal entity, a grounded company name — gets its say. Set high because the
// two outcomes are not symmetric: continuing a crawl costs pages, while
// aborting one wrongly costs a real customer their company record.
const triageAbortConfidence = 0.8

// triageExcerptRunes bounds the seed-page excerpt. A front page identifies
// itself in its first screenful; more text buys navigation chrome and footers.
const triageExcerptRunes = 4_000

// triageSystem is the classifier's prompt.
const triageSystem = `You decide what a website IS, from the text of its front page, so a CRM knows whether the domain behind it belongs to a company.

Answer with ONLY a JSON object: {"kind":one of company|personal|provider|parked|unclear,"confidence":0.0-1.0,"reason":"one short sentence"}

company  — the site of an organization: it sells or offers something, names a team, or presents itself as a business, agency, institution, or association.
personal — the site of ONE individual: a personal homepage, CV, portfolio or blog, or a page whose subject is the person who owns the domain. A one-person business that presents itself AS a business is a company, not personal.
provider — a business selling email mailboxes, web hosting, or domain registration to the general public. Answer this ONLY for the vendor's own site; a company that merely HAS a website is not a provider.
parked   — a registrar placeholder, a "coming soon" or "under construction" page, a bare error page, or a domain-for-sale listing: nothing that identifies anybody.
unclear  — the page does not say. Prefer unclear over guessing; a wrong company or personal answer is worse than no answer.

Judge only what the page states. Do not infer from the domain name.`

// triageSystemFor names THIS call's data boundary; see promptfence.Fence.Rule.
func triageSystemFor(fence promptfence.Fence) string {
	return triageSystem + "\n" + fence.Rule("page")
}

// siteTriageVerdict is the classifier's reply.
type siteTriageVerdict struct {
	Kind       string            `json:"kind"`
	Confidence schema.Confidence `json:"confidence"`
	Reason     string            `json:"reason"`
}

// Aborts reports whether this verdict is sure enough, and negative enough, to
// stop the crawl before it starts.
func (v siteTriageVerdict) Aborts() bool {
	if v.Confidence < triageAbortConfidence {
		return false
	}
	return v.Kind == siteKindPersonal || v.Kind == siteKindProvider || v.Kind == siteKindParked
}

// triageShapeValid is the retry predicate: a reply the gate cannot read at all
// buys one re-ask rather than a dropped verdict.
func triageShapeValid(text string) error {
	var parsed siteTriageVerdict
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &parsed); err != nil {
		return fmt.Errorf("output must be {\"kind\":…,\"confidence\":…,\"reason\":…}: %w", err)
	}
	return nil
}

// The classifier reply's three field names, spelled once. They are this call's
// wire vocabulary and deliberately not the overlay's `kind` parameter, which
// happens to share a spelling and nothing else.
const (
	triageFieldKind       = "kind"
	triageFieldConfidence = "confidence"
	triageFieldReason     = "reason"
)

func triageSchema() json.RawMessage {
	return schema.Must(schema.Object(
		map[string]schema.Node{
			triageFieldKind:       schema.Enum(siteTriageKinds...).Describe("What this site is."),
			triageFieldConfidence: schema.Number().Describe("How confident the classification is, from 0 to 1."),
			triageFieldReason:     schema.String().Describe("One short sentence naming what on the page decided it."),
		},
		triageFieldKind, triageFieldConfidence, triageFieldReason,
	))
}

// triageRequest builds the ONE classification call. The fence is minted per
// request: the page text inside it is a crawled site's own writing, and a
// boundary reused across calls is one some site has already been shown.
func triageRequest(page crawlPage) model.Request {
	fence := promptfence.New()
	body := fmt.Sprintf("url: %s\n\n%s", page.URL, triageExcerpt(page.Text))
	return model.Request{
		System:         triageSystemFor(fence),
		Messages:       []model.Message{{Role: chatRoleUser, Content: fence.Wrap(body)}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		ResponseSchema: triageSchema(),
		SecretStripper: ai.NewSecretStripper(),
	}
}

// triageExcerpt bounds the page text to the budget, cutting on a rune boundary.
func triageExcerpt(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= triageExcerptRunes {
		return string(runes)
	}
	return string(runes[:triageExcerptRunes])
}

// gateTriageVerdict reads the reply and refuses anything outside the closed
// vocabulary or the confidence range. A malformed answer is `unclear`, which
// falls through to the full read — the safe direction, since an unreadable
// reply must never be the reason a real company loses its record.
func gateTriageVerdict(modelText string) siteTriageVerdict {
	var parsed siteTriageVerdict
	if err := json.Unmarshal([]byte(ai.Unfence(modelText)), &parsed); err != nil {
		return siteTriageVerdict{Kind: siteKindUnclear, Reason: "the classifier's reply could not be read"}
	}
	known := false
	for _, kind := range siteTriageKinds {
		known = known || kind == parsed.Kind
	}
	if !known {
		return siteTriageVerdict{Kind: siteKindUnclear, Reason: "the classifier named a kind that does not exist"}
	}
	if parsed.Confidence < 0 || parsed.Confidence > 1 {
		return siteTriageVerdict{Kind: siteKindUnclear, Reason: "the classifier reported a confidence outside 0 to 1"}
	}
	parsed.Reason = strings.TrimSpace(parsed.Reason)
	return parsed
}

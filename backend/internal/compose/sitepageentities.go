// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The legal-entity lane of a page call: the census a legal notice states,
// and the no-guess gate over the identity details printed with each entry.
// Split from the fact lane (sitepagefacts.go) for size, along the seam
// that was already there — a legal page answers this lane, no other page
// kind does, and its rules are about attribution rather than evidence.

import (
	"slices"
	"strings"
	"unicode"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func gatePageEntities(parsed pageFactsReply, page crawlPage, idx snippetIndex, drop func(lane, field, value, reason string)) []corpusLegalEntity {
	// The census is checked against the WHOLE page, not the cited
	// passage: the abstention's completeness must not hinge on where the
	// site's own layout breaks a second entity's name across passages —
	// an undercounted census silently applies the wrong company's legal
	// identity, the exact failure the census exists to prevent.
	pageNorm := normalizeEvidence(page.Text)
	var out []corpusLegalEntity
	for _, e := range parsed.Entities {
		name := strings.TrimSpace(e.N)
		switch {
		case name == "":
			drop(laneLegal, e.N, "", dropEmptyValue)
		case page.Kind != crmcontracts.SiteReadPageKindImpressum || !legalAuthorityPage(page.URL):
			drop(laneLegal, name, "", dropLegalNotFromLegalPage)
		case !strings.Contains(pageNorm, normalizeEvidence(name)):
			// A hallucinated entity must not force a false abstention.
			drop(laneLegal, name, "", dropValueNotInSnippet)
		default:
			entity := corpusLegalEntity{Name: name, SourceURL: page.URL}
			// The block this entity was cited from. The census NAME check
			// deliberately spans the whole page (a layout can split a name
			// across passages), but a DETAIL must come from the entity's own
			// block or it belongs to a sibling company.
			blockNorm := ""
			if ref, ok := idx.resolve(e.E); ok {
				entity.EvidenceSnippet = ref.passage
				blockNorm = ref.norm
			}
			// The block's details carry the same no-guess rule as the name:
			// printed on this page, or absent. A dropped detail costs one
			// field a human can type; an invented registration number is a
			// legal identity that was never theirs.
			entity.RegisteredAddress = groundedDetail(blockNorm, e.A)
			entity.RegisterNumber = groundedDetail(blockNorm, e.R)
			// A detail the model stated but the page does not print is the
			// no-guess gate working; report it rather than dropping it in
			// silence, so a systematically-lost field is visible.
			for field, claimed := range map[string]string{
				fieldRegisteredAddress: e.A, fieldRegisterVat: e.R,
			} {
				if strings.TrimSpace(claimed) != "" && groundedDetail(blockNorm, claimed) == "" {
					drop(laneLegal, field, claimed, dropValueNotInSnippet)
				}
			}
			out = append(out, entity)
		}
	}
	return out
}

// groundedDetail keeps an entity detail only when the CITED BLOCK prints
// it. Scope matters as much as presence: a legal notice lists several
// companies, and a detail that merely appears somewhere on the page may
// belong to a different one — attaching a sibling's registered address to
// the entity a human then selects is the wrong-company outcome the
// multi-entity abstention exists to prevent.
//
// An address survives a round trip through a model with its punctuation
// rearranged — a block printing "Singapore (179433)" comes back
// "Singapore 179433" — and refusing that costs the human the field they
// came for. So the test is that every content token of the value was
// printed in the block, WHOLE: "1234" is not evidence from a block that
// printed "HRB 123456", and a truncated register number is a legal
// identifier the company does not have. Substring containment would accept
// exactly that, which is why it is not used.
//
// The scoping is as strong as the page's own layout. Passages pack short
// lines together, so a legal notice compact enough to fit one passage IS
// the cited block, and a sibling's address inside it cannot be told apart
// from this entity's. The human picking from the census remains the check
// that catches it.
func groundedDetail(blockNorm, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || blockNorm == "" {
		return ""
	}
	claimed := contentTokens(normalizeEvidence(value))
	if len(claimed) == 0 {
		return ""
	}
	if !printedInOrder(contentTokens(blockNorm), claimed) {
		return ""
	}
	return value
}

// printedInOrder reports whether claimed appears in printed as a
// CONTIGUOUS run. A set test would accept a value assembled from tokens
// the block happens to contain separately — a notice printing "24114
// Kiel" and "HRB 123456" would vouch for the invented "HRB 24114" —
// which is how a fabricated legal identifier gets confirmed as fact.
// Contiguity still tolerates the punctuation the model rearranges,
// because punctuation is not a token.
func printedInOrder(printed, claimed []string) bool {
	if len(claimed) > len(printed) {
		return false
	}
	for start := 0; start+len(claimed) <= len(printed); start++ {
		if slices.Equal(printed[start:start+len(claimed)], claimed) {
			return true
		}
	}
	return false
}

// contentTokens splits normalized text into its letter/digit runs — the
// units a whole-token comparison compares.
func contentTokens(normalized string) []string {
	return strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

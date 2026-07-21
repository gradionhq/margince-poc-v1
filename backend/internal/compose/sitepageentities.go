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
		case groundedDetail(pageNorm, name) == "":
			// The same whole-token rule the details get, so a name cannot
			// pass on a partial token either. Only the SCOPE differs: the
			// census reads the whole page, because a layout can break an
			// entity's name across passages and an undercounted census
			// applies the wrong company's legal identity — the failure the
			// abstention exists to prevent. A hallucinated entity must not
			// force a false abstention either, which is what this rejects.
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

// groundedDetail keeps a claimed value only when the text it is judged
// against actually prints it. The CALLER chooses that scope, and the two
// scopes differ for a reason: an entity's name is judged against the whole
// page, because a layout can break it across passages and an undercounted
// census applies the wrong company's legal identity; a detail is judged
// against the entity's own cited block, because a notice lists several
// companies and a sibling's registered address must not attach to the one
// a human then selects.
//
// An address survives a round trip through a model with its punctuation
// rearranged — a block printing "Singapore (179433)" comes back
// "Singapore 179433" — and refusing that costs the human the field they
// came for. So the test is that every content token of the value was
// printed in that text, WHOLE: "1234" is not evidence from a notice that
// printed "HRB 123456", and a truncated register number is a legal
// identifier the company does not have. Substring containment would accept
// exactly that, which is why it is not used.
//
// Block scoping is only as strong as the page's own layout: passages pack
// short lines together, so a legal notice compact enough to fit one
// passage IS the cited block, and a sibling's address inside it cannot be
// told apart from this entity's. The human picking from the census remains
// the check that catches that.
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

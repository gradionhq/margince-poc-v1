// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The offer-draft response shape (split out of offerdraft.go to stay
// under the file-length gate): DraftOfferLines' result type and the
// before/after line-set comparison that fills its diff_from_previous.
// This orchestrator only ever ADDS staged lines (T7's
// AddStagedOfferLines never removes or edits an existing row), so
// removed/changed come back empty in practice today — the comparison is
// written generically rather than hand-asserted empty, so it stays
// correct if a future caller ever hands this two revisions that
// genuinely diverge.

import (
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// offerLineDiffFields mirrors crmcontracts.Offer.DiffFromPrevious's
// pointee type field-for-field (name, Go type, and JSON tag, in the same
// order) — T6 left diff_from_previous as an inlined anonymous object
// schema rather than a $ref, so oapi-codegen emitted no named Go type for
// it. Declaring the identical anonymous struct as a type ALIAS (not a new
// type) makes a value built here assign directly onto the generated
// field: Go struct types are identical when their field sequence, types,
// and tags match, and an alias is that same type, not a lookalike one.
type offerLineDiffFields = struct {
	Added   *[]crmcontracts.OfferLineItem `json:"added,omitempty"`
	Changed *[]struct {
		After  *crmcontracts.OfferLineItem `json:"after,omitempty"`
		Before *crmcontracts.OfferLineItem `json:"before,omitempty"`
	} `json:"changed,omitempty"`
	Removed *[]crmcontracts.OfferLineItem `json:"removed,omitempty"`
}

// DraftResult is what DraftOfferLines hands its caller (T9's regenerate
// handler): the offer AFTER staging, plus the same three facts flattened
// so the handler needs no Offer-field archaeology to build its response.
type DraftResult struct {
	Offer        crmcontracts.Offer
	AIGenerated  bool
	AIDisclosure *string
	Diff         *offerLineDiffFields
}

// linesOf reads an offer's nested line items defensively — GetOffer
// always populates LineItems, but a zero-value Offer{} (the honest-empty
// path's "before" on a hypothetical future caller) must not panic.
func linesOf(o crmcontracts.Offer) []crmcontracts.OfferLineItem {
	if o.LineItems == nil {
		return nil
	}
	return *o.LineItems
}

// diffOfferLines compares an offer's line set before and after a change
// by id: new ids are additions, missing ids are removals, ids present in
// both with different content are changes.
func diffOfferLines(before, after []crmcontracts.OfferLineItem) (added, removed []crmcontracts.OfferLineItem, changed []offerLineChange) {
	beforeByID := make(map[string]crmcontracts.OfferLineItem, len(before))
	for _, l := range before {
		beforeByID[l.Id.String()] = l
	}
	seen := make(map[string]bool, len(before))
	for _, l := range after {
		prior, ok := beforeByID[l.Id.String()]
		if !ok {
			added = append(added, l)
			continue
		}
		seen[l.Id.String()] = true
		if !sameOfferLineContent(prior, l) {
			changed = append(changed, offerLineChange{Before: prior, After: l})
		}
	}
	for _, l := range before {
		if !seen[l.Id.String()] {
			removed = append(removed, l)
		}
	}
	return added, removed, changed
}

// offerLineChange is one before/after pair diffOfferLines found.
type offerLineChange struct {
	Before crmcontracts.OfferLineItem
	After  crmcontracts.OfferLineItem
}

func sameOfferLineContent(a, b crmcontracts.OfferLineItem) bool {
	return a.Description == b.Description && a.Quantity == b.Quantity &&
		a.UnitPriceMinor == b.UnitPriceMinor && a.TaxRate == b.TaxRate && a.DiscountPct == b.DiscountPct
}

// buildOfferDiff renders the diff as the exact anonymous shape
// crmcontracts.Offer.DiffFromPrevious points to, or nil when nothing
// moved — a regenerate response with no AI lines carries no
// diff_from_previous at all, not an empty one.
func buildOfferDiff(added, removed []crmcontracts.OfferLineItem, changed []offerLineChange) *offerLineDiffFields {
	if len(added) == 0 && len(removed) == 0 && len(changed) == 0 {
		return nil
	}
	diff := &offerLineDiffFields{}
	if len(added) > 0 {
		diff.Added = &added
	}
	if len(removed) > 0 {
		diff.Removed = &removed
	}
	if len(changed) > 0 {
		pairs := make([]struct {
			After  *crmcontracts.OfferLineItem `json:"after,omitempty"`
			Before *crmcontracts.OfferLineItem `json:"before,omitempty"`
		}, len(changed))
		for i, c := range changed {
			c := c
			pairs[i] = struct {
				After  *crmcontracts.OfferLineItem `json:"after,omitempty"`
				Before *crmcontracts.OfferLineItem `json:"before,omitempty"`
			}{After: &c.After, Before: &c.Before}
		}
		diff.Changed = &pairs
	}
	return diff
}

func boolPtr(b bool) *bool { return &b }

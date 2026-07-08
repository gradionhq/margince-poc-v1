// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The staged-line confirm seam (E03.21a): an AI-proposed offer line sits
// at proposal_state='staged' and counts for NOTHING until a human
// accepts it — acceptance flips the state and re-derives the stored
// totals in the same transaction, re-acceptance is a no-op because the
// desired end-state already holds, and the draft gate governs it exactly
// like every other line write.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// offerDeskPerms is the deal-desk grant this suite drives the offer
// store under; row scope all keeps visibility out of the frame here.
var offerDeskPerms = principal.Permissions{
	RoleKeys: []string{"deal_desk"},
	Objects: map[string]principal.ObjectGrant{
		"deal":  {Create: true, Read: true, Update: true},
		"offer": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeAll,
}

// offerTotals reads the derived money triple off the contract shape.
func offerTotals(t *testing.T, o crmcontracts.Offer) (net, tax, gross int64) {
	t.Helper()
	if o.NetMinor == nil || o.TaxMinor == nil || o.GrossMinor == nil {
		t.Fatalf("offer %s ships without derived totals", o.Id)
	}
	return *o.NetMinor, *o.TaxMinor, *o.GrossMinor
}

// stageLine plants one AI-proposed line on the offer the way the
// drafting seam stores it: proposal_state='staged', position appended.
func stageLine(t *testing.T, e *Env, offerID crmcontracts.Id, position int) ids.UUID {
	t.Helper()
	lineID := ids.NewV7()
	e.WsExec(t, `
		INSERT INTO offer_line_item (id, workspace_id, offer_id, position, description,
		                             quantity, unit_price_minor, tax_rate, proposal_state)
		VALUES ($1, $2, $3, $4, 'AI-proposed support', 2, 5000, 19.00, 'staged')`,
		lineID, e.WS, ids.UUID(offerID), position)
	return lineID
}

func TestAcceptOfferLineItemFlipsStagedIntoTheTotals(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Staged-line deal", pipeline, open, &e.Rep1)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)

	// One human-entered line: 1 × 100.00 @19% → net 10000, tax 1900.
	description, price, taxRate := "Retainer", int64(10000), "19.00"
	created, err := e.Deals.CreateOffer(ctx, ids.From[ids.DealKind](dealID), deals.CreateOfferInput{
		Currency: "EUR", Source: "manual",
		LineItems: []deals.OfferLineInputRow{{
			Description: &description, Quantity: "1", UnitPriceMinor: &price, TaxRate: &taxRate,
		}},
	})
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	offerID := ids.From[ids.OfferKind](ids.UUID(created.Id))

	// A staged proposal exists but counts for nothing yet.
	stagedLine := stageLine(t, e, created.Id, 2)
	beforeAccept, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read offer with staged line: %v", err)
	}
	if net, tax, gross := offerTotals(t, beforeAccept); net != 10000 || tax != 1900 || gross != 11900 {
		t.Fatalf("totals with a staged line = %d/%d/%d, want the human line only (10000/1900/11900)", net, tax, gross)
	}

	// Accept: the staged 2 × 50.00 @19% joins the totals in one step.
	accepted, err := e.Deals.AcceptOfferLineItem(ctx, offerID, stagedLine)
	if err != nil {
		t.Fatalf("accept staged line: %v", err)
	}
	if net, tax, gross := offerTotals(t, accepted); net != 20000 || tax != 3800 || gross != 23800 {
		t.Fatalf("totals after accept = %d/%d/%d, want both lines (20000/3800/23800)", net, tax, gross)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM offer_line_item WHERE id = $1 AND proposal_state = 'accepted'`, stagedLine); n != 1 {
		t.Fatalf("staged line's proposal_state did not flip to accepted")
	}
	auditedAccepts := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'offer' AND action = 'update'`)

	// Re-accepting is a no-op: same totals, no second audit fact.
	again, err := e.Deals.AcceptOfferLineItem(ctx, offerID, stagedLine)
	if err != nil {
		t.Fatalf("re-accept: %v", err)
	}
	if net, tax, gross := offerTotals(t, again); net != 20000 || tax != 3800 || gross != 23800 {
		t.Fatalf("totals after re-accept drifted to %d/%d/%d", net, tax, gross)
	}
	if after := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'offer' AND action = 'update'`); after != auditedAccepts {
		t.Fatalf("re-accept wrote %d extra audit rows — the no-op must not manufacture history", after-auditedAccepts)
	}

	// A line this offer never held reads as absent.
	if _, err := e.Deals.AcceptOfferLineItem(ctx, offerID, ids.NewV7()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("accept of an absent line → %v, want ErrNotFound", err)
	}

	// Once the offer leaves draft, the confirm seam refuses like every
	// other line write.
	e.WsExec(t, `UPDATE offer SET status = 'sent' WHERE id = $1`, ids.UUID(created.Id))
	var notDraft *deals.OfferNotDraftError
	if _, err := e.Deals.AcceptOfferLineItem(ctx, offerID, stagedLine); !errors.As(err, &notDraft) {
		t.Fatalf("accept on a sent offer → %v, want OfferNotDraftError", err)
	}
}

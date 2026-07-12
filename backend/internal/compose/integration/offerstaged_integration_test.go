// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The staged-line PERSISTENCE seam's store-level coverage (E03.21a):
// AddStagedOfferLines is model-free — no ai/model import anywhere near
// it — and is the write path the compose offer-drafting orchestrator
// (T8) will call once it has already talked to the model. Every staged
// line lands with its verbatim evidence and price_grounded flag, is
// excluded from the offer's server-computed totals until a human accepts
// it (offer_acceptline_integration_test.go proves that acceptance flip
// already round-trips), and is governed by the same draft gate, RBAC
// object grant, and row scope as every other offer-line write.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// agentOfferDraftCtx binds the acting principal the compose orchestrator
// uses when it calls AddStagedOfferLines: a system-type actor carrying
// the agent:offer-drafting identity, exactly like deals' own overnight
// reconciler (reconcile.go) and people's coldstart/enrich approval
// effects (compose/coldstartaccept.go) — auth.Require's RBAC check is a
// no-op for a system principal, and storekit.Audit/Emit stamp this
// identity onto the audit_log row automatically, which is the only place
// offer_line_item's AI provenance lands (the table itself carries no
// captured_by column).
func agentOfferDraftCtx(e *Env) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "agent:offer-drafting"})
}

// baseDraftOffer seeds a deal and a draft offer carrying one human-entered
// line (1 × 100.00 @19% → net 10000, tax 1900, gross 11900) — the fixed
// baseline every test below asserts a staged batch never moves.
func baseDraftOffer(ctx context.Context, t *testing.T, e *Env, name string) ids.OfferID {
	t.Helper()
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, name, pipeline, open, &e.Rep1)
	description, price, taxRate := "Retainer", int64(10000), "19.00"
	created, err := e.Deals.CreateOffer(ctx, ids.From[ids.DealKind](dealID), deals.CreateOfferInput{
		Currency: "EUR", Source: "manual",
		LineItems: []deals.OfferLineInputRow{{
			Description: &description, Quantity: "1", UnitPriceMinor: &price, TaxRate: &taxRate,
		}},
	})
	if err != nil {
		t.Fatalf("seed draft offer: %v", err)
	}
	return ids.From[ids.OfferKind](ids.UUID(created.Id))
}

// groundedStagedLines is two AI-drafted, evidence-bearing, fully-grounded
// lines — the common-case batch AddStagedOfferLines persists.
func groundedStagedLines() []deals.StagedOfferLineInput {
	return []deals.StagedOfferLineInput{
		{
			Description: "Onboarding workshop", Quantity: "1", UnitPriceMinor: 20000, TaxRate: "19.00",
			Evidence:      deals.StagedOfferLineEvidence{Snippet: `"we'd want a kickoff workshop"`, SourceID: "activity-1"},
			PriceGrounded: true,
		},
		{
			Description: "Monthly support retainer", Quantity: "3", UnitPriceMinor: 5000, TaxRate: "19.00",
			Evidence:      deals.StagedOfferLineEvidence{Snippet: `"three months of support"`, SourceID: "activity-2"},
			PriceGrounded: true,
		},
	}
}

func TestAddStagedOfferLinesInsertsEvidencedLinesExcludedFromTotals(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	offerID := baseDraftOffer(ctx, t, e, "Staged-add deal")

	before, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read offer before staging: %v", err)
	}
	beforeNet, beforeTax, beforeGross := offerTotals(t, before)

	added, err := e.Deals.AddStagedOfferLines(agentOfferDraftCtx(e), offerID, groundedStagedLines())
	if err != nil {
		t.Fatalf("add staged offer lines: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("added %d lines, want 2", len(added))
	}
	for i, line := range added {
		if line.Evidence == nil {
			t.Fatalf("line %d: evidence did not round-trip on the insert response", i)
		}
		snippet, _ := (*line.Evidence)["snippet"].(string)
		sourceID, _ := (*line.Evidence)["source_id"].(string)
		if snippet == "" || sourceID == "" {
			t.Fatalf("line %d: evidence %+v missing snippet/source_id", i, *line.Evidence)
		}
		if line.PriceGrounded == nil || !*line.PriceGrounded {
			t.Fatalf("line %d: price_grounded = %v, want true", i, line.PriceGrounded)
		}
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM offer_line_item WHERE offer_id = $1 AND proposal_state = 'staged'`, offerID.UUID); n != 2 {
		t.Fatalf("offer_line_item has %d staged rows, want 2", n)
	}

	// Excluded from totals: the offer's derived money is unchanged.
	after, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read offer after staging: %v", err)
	}
	net, tax, gross := offerTotals(t, after)
	if net != beforeNet || tax != beforeTax || gross != beforeGross {
		t.Fatalf("totals moved to %d/%d/%d after staging, want unchanged %d/%d/%d",
			net, tax, gross, beforeNet, beforeTax, beforeGross)
	}
	// The evidence and price_grounded flag persisted, not just echoed on
	// the insert response — a fresh read shows the same shape.
	if got := len(*after.LineItems); got != 3 {
		t.Fatalf("offer has %d lines after staging (1 human + 2 staged), got %d", 3, got)
	}
	var sawStagedEvidence int
	for _, l := range *after.LineItems {
		if l.Evidence != nil {
			sawStagedEvidence++
			if l.PriceGrounded == nil || !*l.PriceGrounded {
				t.Fatalf("re-read staged line %s: price_grounded = %v, want true", l.Id, l.PriceGrounded)
			}
		}
	}
	if sawStagedEvidence != 2 {
		t.Fatalf("re-read offer shows %d lines with evidence, want 2", sawStagedEvidence)
	}
}

func TestAddStagedOfferLinesThenAcceptCountsInTotals(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	offerID := baseDraftOffer(ctx, t, e, "Staged-accept deal")

	added, err := e.Deals.AddStagedOfferLines(agentOfferDraftCtx(e), offerID, groundedStagedLines())
	if err != nil {
		t.Fatalf("add staged offer lines: %v", err)
	}

	// Still excluded before acceptance: net 10000/1900/11900 (human line only).
	beforeAccept, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read offer before accept: %v", err)
	}
	if net, tax, gross := offerTotals(t, beforeAccept); net != 10000 || tax != 1900 || gross != 11900 {
		t.Fatalf("totals with staged lines only = %d/%d/%d, want the human line alone (10000/1900/11900)", net, tax, gross)
	}

	// Accept the first staged line (1 × 200.00 @19% → net 20000, tax 3800):
	// the substrate round-trips through the existing confirm seam.
	accepted, err := e.Deals.AcceptOfferLineItem(ctx, offerID, ids.UUID(added[0].Id))
	if err != nil {
		t.Fatalf("accept staged line: %v", err)
	}
	if net, tax, gross := offerTotals(t, accepted); net != 30000 || tax != 5700 || gross != 35700 {
		t.Fatalf("totals after accepting one staged line = %d/%d/%d, want 30000/5700/35700", net, tax, gross)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM offer_line_item WHERE id = $1 AND proposal_state = 'accepted'`, ids.UUID(added[0].Id)); n != 1 {
		t.Fatalf("accepted staged line's proposal_state did not flip to accepted")
	}
	// The second staged line is untouched: still staged, still excluded.
	if n := e.WsCount(t,
		`SELECT count(*) FROM offer_line_item WHERE id = $1 AND proposal_state = 'staged'`, ids.UUID(added[1].Id)); n != 1 {
		t.Fatalf("the un-accepted staged line drifted out of proposal_state='staged'")
	}
}

func TestAddStagedOfferLinesUngroundedPriceIsTheHonestZeroSentinel(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	offerID := baseDraftOffer(ctx, t, e, "Ungrounded-price deal")

	// A price the drafting engine could not ground: zero sentinel, never
	// a guess — this must be accepted and marked price_grounded=false.
	ungrounded := []deals.StagedOfferLineInput{{
		Description: "Bespoke integration work", Quantity: "1", UnitPriceMinor: 0, TaxRate: "0.00",
		Evidence:      deals.StagedOfferLineEvidence{Snippet: `"we'd also need a custom integration"`, SourceID: "activity-3"},
		PriceGrounded: false,
	}}
	added, err := e.Deals.AddStagedOfferLines(agentOfferDraftCtx(e), offerID, ungrounded)
	if err != nil {
		t.Fatalf("add ungrounded staged line: %v", err)
	}
	if added[0].PriceGrounded == nil || *added[0].PriceGrounded {
		t.Fatalf("ungrounded line's price_grounded = %v, want false", added[0].PriceGrounded)
	}
	if added[0].UnitPriceMinor != 0 {
		t.Fatalf("ungrounded line priced at %d, want the zero sentinel", added[0].UnitPriceMinor)
	}

	// A claimed-ungrounded price that is NOT zero is a caller bug this
	// seam refuses to persist silently.
	bogus := []deals.StagedOfferLineInput{{
		Description: "Bogus line", Quantity: "1", UnitPriceMinor: 500, TaxRate: "0.00",
		Evidence:      deals.StagedOfferLineEvidence{Snippet: "x", SourceID: "activity-4"},
		PriceGrounded: false,
	}}
	var mismatch *deals.UngroundedPriceNotZeroError
	if _, err := e.Deals.AddStagedOfferLines(agentOfferDraftCtx(e), offerID, bogus); !errors.As(err, &mismatch) {
		t.Fatalf("ungrounded+nonzero price = %v, want UngroundedPriceNotZeroError", err)
	}
}

func TestAddStagedOfferLinesDeniesWithoutOfferUpdateGrant(t *testing.T) {
	e := Setup(t)
	admin := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	offerID := baseDraftOffer(admin, t, e, "Staged-deny deal")

	readOnly := principal.Permissions{
		RoleKeys: []string{"read_only"},
		Objects:  map[string]principal.ObjectGrant{"offer": {Read: true}},
		RowScope: principal.RowScopeAll,
	}
	denied := e.As(e.Rep2, []ids.UUID{e.Team1}, readOnly)
	if _, err := e.Deals.AddStagedOfferLines(denied, offerID, groundedStagedLines()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("add staged lines with offer-read only = %v, want ErrPermissionDenied", err)
	}
}

func TestAddStagedOfferLinesRejectsANonDraftOffer(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	offerID := baseDraftOffer(ctx, t, e, "Staged-nondraft deal")

	e.WsExec(t, `UPDATE offer SET status = 'sent' WHERE id = $1`, offerID.UUID)
	var notDraft *deals.OfferNotDraftError
	if _, err := e.Deals.AddStagedOfferLines(agentOfferDraftCtx(e), offerID, groundedStagedLines()); !errors.As(err, &notDraft) {
		t.Fatalf("add staged lines on a sent offer = %v, want OfferNotDraftError", err)
	}
}

func TestAddStagedOfferLinesCrossTenantOfferIsNotFound(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	// A real offer — just seeded in an entirely separate workspace — must
	// answer the same existence-hiding 404 an unknown id would.
	owner := OwnerConn(t)
	otherOfferID := seedOfferRenderWorkspaceB(t, e, owner)
	if _, err := e.Deals.AddStagedOfferLines(ctx, otherOfferID, groundedStagedLines()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("add staged lines on a cross-tenant offer = %v, want ErrNotFound", err)
	}
}

func TestAddStagedOfferLinesAuditCarriesTheAgentProvenance(t *testing.T) {
	e := Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDeskPerms)
	offerID := baseDraftOffer(ctx, t, e, "Staged-provenance deal")

	if _, err := e.Deals.AddStagedOfferLines(agentOfferDraftCtx(e), offerID, groundedStagedLines()); err != nil {
		t.Fatalf("add staged offer lines: %v", err)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log
		 WHERE entity_type = 'offer' AND entity_id = $1 AND action = 'update'
		   AND actor_type = 'system' AND actor_id = 'agent:offer-drafting'`,
		offerID.UUID); n != 1 {
		t.Fatalf("staged-lines audit row does not carry the agent:offer-drafting provenance")
	}
}

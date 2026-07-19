// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The AI-drafted offer regeneration orchestrator (arc 4b, T8): grounded
// candidates stage as evidence-bearing lines and disclose (features/07
// §11 gate 9); ungrounded ones are dropped, honestly, with no disclosure
// and no diff; price grounds on conversation evidence first, the rate
// card second, and the zero sentinel last — never a guess (§8b); every
// outbound model payload is secret-stripped; and the server-computed
// totals never move, because staging is the only write this orchestrator
// makes and AddStagedOfferLines (T7) already excludes staged lines from
// them.

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// offerDraftPerms is the deal-desk grant this suite drives the drafter
// under: deal/offer read+write to seed and drive, product read for the
// rate-card lookup. Row scope all keeps visibility out of the frame —
// the evidence gate, not RBAC, is what these tests exercise.
var offerDraftPerms = principal.Permissions{
	RoleKeys: []string{"deal_desk"},
	Objects: map[string]principal.ObjectGrant{
		"deal":    {Create: true, Read: true, Update: true},
		"offer":   {Create: true, Read: true, Update: true},
		"product": {Create: true, Read: true},
	},
	RowScope: principal.RowScopeAll,
}

// newOfferDrafterFixture wires an offerDrafter over the harness pool: the
// search module's retriever (the retrieval seam decision this task made —
// no bespoke context store) and a fresh, per-test FakeClient, ridden
// through the router (fakeModelPath) on the same OfferDraft lane
// production wires (compose.WithOfferDraft(modelPath.OfferDraft, …)) so
// each test's assertions about what was scripted/recorded stay
// independent.
func newOfferDrafterFixture(t *testing.T, e *integration.Env, brain *ai.FakeClient) offerDrafter {
	return offerDrafter{
		brain:   fakeModelPath(t, brain).OfferDraft,
		deals:   e.Deals,
		context: search.NewRetriever(search.NewStore(e.Pool), nil),
	}
}

// seedDraftOfferWithDealActivity seeds a deal, a draft offer with one
// human-entered line (so a test can prove staging never moves that
// baseline), and ONE activity linked to the deal carrying subjectText —
// the deal's "captured context" this orchestrator reads. It returns the
// offer id and the seeded activity's id (the source_id a candidate must
// cite to ground against it).
func seedDraftOfferWithDealActivity(ctx context.Context, t *testing.T, e *integration.Env, name, subjectText string) (ids.OfferID, string) {
	t.Helper()
	pipeline, open, _ := integration.DealFixture(t, e)
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

	owner := integration.OwnerConn(t)
	activityID := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		 VALUES ($1, $2, 'note', $3, '2026-07-01T10:00:00Z', 'manual', 'human:x')`,
		activityID, e.WS, subjectText); err != nil {
		t.Fatalf("seed deal activity: %v", err)
	}
	integration.LinkActivity(t, owner, e.WS, activityID, "deal", dealID)

	return ids.From[ids.OfferKind](ids.UUID(created.Id)), "activity:" + activityID.String()
}

// offerLineByDescription finds one line on the offer by description —
// staged lines land after the seeded human line, but position is an
// implementation detail these tests should not depend on.
func offerLineByDescription(t *testing.T, o crmcontracts.Offer, description string) crmcontracts.OfferLineItem {
	t.Helper()
	if o.LineItems != nil {
		for _, l := range *o.LineItems {
			if l.Description == description {
				return l
			}
		}
	}
	t.Fatalf("offer %s carries no line %q", o.Id, description)
	return crmcontracts.OfferLineItem{}
}

func offerTotals(t *testing.T, o crmcontracts.Offer) (net, tax, gross int64) {
	t.Helper()
	if o.NetMinor == nil || o.TaxMinor == nil || o.GrossMinor == nil {
		t.Fatalf("offer %s ships without derived totals", o.Id)
	}
	return *o.NetMinor, *o.TaxMinor, *o.GrossMinor
}

func TestDraftOfferLinesStagesGroundedLinesAndDiscloses(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	offerID, workshopSource := seedDraftOfferWithDealActivity(ctx, t, e, "Grounded-draft deal",
		`Client said: "we'd want a kickoff workshop" and agreed to 20000 cents for it.`)

	before, err := e.Deals.GetOffer(ctx, offerID, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read offer before drafting: %v", err)
	}
	beforeNet, beforeTax, beforeGross := offerTotals(t, before)

	// Two candidates both cite REAL evidence from the same source, but
	// only "Kickoff workshop" quotes a snippet that actually STATES the
	// 20000 price — "Follow-up session" cites a genuine snippet too, just
	// one that says nothing about money, and must NOT be machine-labeled
	// "grounded" for a price it merely rides alongside (OFFER-AC-14: a
	// real citation is not itself proof of a price the citation doesn't
	// state).
	fake := ai.NewFakeClient().Script(`{"lines":[
		{"description":"Kickoff workshop","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"agreed to 20000 cents for it","source_id":"` + workshopSource + `",
		 "conversation_price_minor":20000},
		{"description":"Follow-up session","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"we'd want a kickoff workshop","source_id":"` + workshopSource + `",
		 "conversation_price_minor":20000}
	]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	result, err := drafter.DraftOfferLines(ctx, offerID)
	if err != nil {
		t.Fatalf("draft offer lines: %v", err)
	}

	if !result.AIGenerated {
		t.Fatalf("AIGenerated = false, want true (two candidates ground)")
	}
	if result.AIDisclosure == nil || *result.AIDisclosure != signals.Art50Disclosure {
		t.Fatalf("AIDisclosure = %v, want the Art.50 disclosure", result.AIDisclosure)
	}
	if result.Diff == nil || result.Diff.Added == nil || len(*result.Diff.Added) != 2 {
		t.Fatalf("Diff.Added = %+v, want 2 staged lines", result.Diff)
	}
	if result.Diff.Removed != nil || result.Diff.Changed != nil {
		t.Fatalf("Diff.Removed/Changed = %+v/%+v, want both nil (staging only ever adds)", result.Diff.Removed, result.Diff.Changed)
	}

	workshop := offerLineByDescription(t, result.Offer, "Kickoff workshop")
	if workshop.Evidence == nil {
		t.Fatalf("staged line carries no evidence")
	}
	if workshop.PriceGrounded == nil || !*workshop.PriceGrounded || workshop.UnitPriceMinor != 20000 {
		t.Fatalf("workshop line price_grounded/unit_price = %v/%d, want true/20000 (its own cited snippet states the price)", workshop.PriceGrounded, workshop.UnitPriceMinor)
	}

	followUp := offerLineByDescription(t, result.Offer, "Follow-up session")
	if followUp.PriceGrounded == nil || *followUp.PriceGrounded || followUp.UnitPriceMinor != 0 {
		t.Fatalf("follow-up line price_grounded/unit_price = %v/%d, want false/0 — its cited snippet never states 20000, only a DIFFERENT line's evidence does",
			followUp.PriceGrounded, followUp.UnitPriceMinor)
	}

	// Never AI-computed: the server's derived totals are exactly the
	// pre-staging baseline (the retainer alone) — staged lines are
	// excluded until a human accepts them (T7).
	net, tax, gross := offerTotals(t, result.Offer)
	if net != beforeNet || tax != beforeTax || gross != beforeGross {
		t.Fatalf("totals moved to %d/%d/%d after AI staging, want unchanged %d/%d/%d", net, tax, gross, beforeNet, beforeTax, beforeGross)
	}

	// The agent provenance lands on the audit row — the one place
	// offer_line_item's AI authorship is recorded (T7).
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'offer' AND entity_id = $1
		 AND action = 'update' AND actor_type = 'system' AND actor_id = 'agent:offer-drafting'`,
		offerID.UUID); n != 1 {
		t.Fatalf("staged-lines audit row does not carry the agent:offer-drafting provenance")
	}
}

func TestDraftOfferLinesDropsUngroundedCandidateAsHonestEmpty(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	offerID, source := seedDraftOfferWithDealActivity(ctx, t, e, "Ungrounded-draft deal",
		"Client mentioned they liked our website.")

	fake := ai.NewFakeClient().Script(`{"lines":[
		{"description":"Bespoke consulting package","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"this text never appears in the deal's context","source_id":"` + source + `",
		 "conversation_price_minor":50000}
	]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	result, err := drafter.DraftOfferLines(ctx, offerID)
	if err != nil {
		t.Fatalf("draft offer lines: %v", err)
	}
	if result.AIGenerated {
		t.Fatalf("AIGenerated = true, want false (the only candidate is ungrounded)")
	}
	if result.AIDisclosure != nil {
		t.Fatalf("AIDisclosure = %v, want nil on an honest empty draft", *result.AIDisclosure)
	}
	if result.Diff != nil {
		t.Fatalf("Diff = %+v, want nil on an honest empty draft", result.Diff)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM offer_line_item WHERE offer_id = $1 AND proposal_state = 'staged'`, offerID.UUID); n != 0 {
		t.Fatalf("ungrounded candidate staged %d rows, want 0 — never fabricate", n)
	}
}

func TestDraftOfferLinesGroundsPriceOnTheRateCardWhenNoConversationPrice(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	offerID, source := seedDraftOfferWithDealActivity(ctx, t, e, "Rate-card-draft deal",
		"The client wants ongoing onboarding support for their new hires.")

	unit := "unit"
	product, err := e.Deals.CreateProduct(ctx, deals.CreateProductInput{
		Name: "Onboarding Support", Unit: &unit, UnitPriceMinor: 15000, Currency: "EUR", Source: "manual",
	})
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}

	fake := ai.NewFakeClient().Script(`{"lines":[
		{"description":"Onboarding support","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"ongoing onboarding support for their new hires","source_id":"` + source + `",
		 "product_id":"` + product.Id.String() + `"}
	]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	result, err := drafter.DraftOfferLines(ctx, offerID)
	if err != nil {
		t.Fatalf("draft offer lines: %v", err)
	}
	line := offerLineByDescription(t, result.Offer, "Onboarding support")
	if line.PriceGrounded == nil || !*line.PriceGrounded {
		t.Fatalf("price_grounded = %v, want true (rate-card fallback)", line.PriceGrounded)
	}
	if line.UnitPriceMinor != 15000 {
		t.Fatalf("unit_price_minor = %d, want the product's 15000 (never re-typed by the model)", line.UnitPriceMinor)
	}
}

// TestDraftOfferLinesNeverGroundsARateCardPriceInAMismatchedCurrency
// guards OFFER-AC-14's currency rule: a same-id product match is not
// enough to trust its price — a product priced in a DIFFERENT currency
// than the offer must never be machine-labeled "grounded" with that
// wrong-currency amount. The candidate's description/evidence still
// grounds (that gate is currency-blind by design), only the price falls
// through to the honest zero sentinel.
func TestDraftOfferLinesNeverGroundsARateCardPriceInAMismatchedCurrency(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	offerID, source := seedDraftOfferWithDealActivity(ctx, t, e, "Currency-mismatch-draft deal",
		"The client wants ongoing onboarding support for their new hires.")

	unit := "unit"
	product, err := e.Deals.CreateProduct(ctx, deals.CreateProductInput{
		// The offer is EUR (seedDraftOfferWithDealActivity's fixture); this
		// rate-card product is priced in USD — same id space, different
		// currency, so its unit_price_minor must never ground the EUR line.
		Name: "Onboarding Support", Unit: &unit, UnitPriceMinor: 15000, Currency: "USD", Source: "manual",
	})
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}

	fake := ai.NewFakeClient().Script(`{"lines":[
		{"description":"Onboarding support","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"ongoing onboarding support for their new hires","source_id":"` + source + `",
		 "product_id":"` + product.Id.String() + `"}
	]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	result, err := drafter.DraftOfferLines(ctx, offerID)
	if err != nil {
		t.Fatalf("draft offer lines: %v", err)
	}
	if !result.AIGenerated {
		t.Fatalf("AIGenerated = false, want true — the description/evidence still grounds, only the price does not")
	}
	line := offerLineByDescription(t, result.Offer, "Onboarding support")
	if line.PriceGrounded == nil || *line.PriceGrounded {
		t.Fatalf("price_grounded = %v, want false — the matched product is priced in USD, the offer is EUR, never stamp that price grounded",
			line.PriceGrounded)
	}
	if line.UnitPriceMinor != 0 {
		t.Fatalf("unit_price_minor = %d, want the honest zero sentinel, never the USD product's 15000 mislabeled as EUR", line.UnitPriceMinor)
	}
}

// TestGroundOfferLinesPropagatesANonNotFoundProductLookupError guards the
// other half of resolvePrice's error handling: GetProduct failing for a
// reason OTHER than "no such product" is a real fault, not a grounding
// verdict, and must abort the draft rather than being silently relabeled
// "ungrounded" (an infra/permission error must never masquerade as an
// honest zero-priced line). A canceled context is the deterministic way
// to force a genuine backend error out of a real Postgres-backed
// GetProduct call without reaching for a mock seam the store has none
// of: RBAC admission reads only the principal already bound to the
// context (unaffected by cancellation), so the failure surfaces from the
// transaction itself, not from apperrors.ErrNotFound.
func TestGroundOfferLinesPropagatesANonNotFoundProductLookupError(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)

	unit := "unit"
	product, err := e.Deals.CreateProduct(ctx, deals.CreateProductInput{
		Name: "Support Plan", Unit: &unit, UnitPriceMinor: 5000, Currency: "EUR", Source: "manual",
	})
	if err != nil {
		t.Fatalf("seed product: %v", err)
	}

	drafter := offerDrafter{deals: e.Deals}
	dealContext := []dealContextItem{{SourceID: "activity:1", Snippet: "Client wants the support plan."}}
	candidates := []offerLineCandidate{{
		Description: "Support plan", Quantity: "1", TaxRate: "19.00",
		EvidenceSnippet: "wants the support plan", SourceID: "activity:1",
		ProductID: product.Id.String(),
	}}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := drafter.groundOfferLines(canceled, candidates, dealContext, "EUR"); err == nil {
		t.Fatalf("groundOfferLines error = nil, want a propagated error — a failed product lookup must never be silently treated as ungrounded")
	}
}

func TestDraftOfferLinesNeverGuessesAnUngroundedPrice(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	offerID, source := seedDraftOfferWithDealActivity(ctx, t, e, "No-price-draft deal",
		"The client asked for a custom integration with their internal tools.")

	fake := ai.NewFakeClient().Script(`{"lines":[
		{"description":"Custom integration","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"custom integration with their internal tools","source_id":"` + source + `"}
	]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	result, err := drafter.DraftOfferLines(ctx, offerID)
	if err != nil {
		t.Fatalf("draft offer lines: %v", err)
	}
	if !result.AIGenerated {
		t.Fatalf("AIGenerated = false, want true — the description/evidence still grounds, only the price does not")
	}
	line := offerLineByDescription(t, result.Offer, "Custom integration")
	if line.PriceGrounded == nil || *line.PriceGrounded {
		t.Fatalf("price_grounded = %v, want false (no conversation price, no product match)", line.PriceGrounded)
	}
	if line.UnitPriceMinor != 0 {
		t.Fatalf("unit_price_minor = %d, want the honest zero sentinel, never a guess", line.UnitPriceMinor)
	}
}

func TestDraftOfferLinesDropsStoreInvalidQuantityWithoutErroringTheBatch(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	offerID, source := seedDraftOfferWithDealActivity(ctx, t, e, "Bad-decimal-draft deal",
		"The client wants a workshop and also asked about an onboarding session.")

	// "1e3" and "0" both parse fine under strconv.ParseFloat — the gate a
	// naive validator would apply — but the store's stricter exact-decimal
	// column parser (ratFromDecimal) rejects scientific notation, and "0"
	// is not a real quantity. Neither may error the whole
	// AddStagedOfferLines transaction; each must drop, leaving the one
	// well-formed candidate to stage on its own.
	fake := ai.NewFakeClient().Script(`{"lines":[
		{"description":"Workshop","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"wants a workshop","source_id":"` + source + `"},
		{"description":"Onboarding (scientific notation)","quantity":"1e3","tax_rate":"19.00",
		 "evidence_snippet":"onboarding session","source_id":"` + source + `"},
		{"description":"Onboarding (zero quantity)","quantity":"0","tax_rate":"19.00",
		 "evidence_snippet":"onboarding session","source_id":"` + source + `"}
	]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	result, err := drafter.DraftOfferLines(ctx, offerID)
	if err != nil {
		t.Fatalf("draft offer lines: %v, want no error — a store-invalid decimal must drop its line, never fail the batch", err)
	}
	if !result.AIGenerated || result.Diff == nil || result.Diff.Added == nil || len(*result.Diff.Added) != 1 {
		t.Fatalf("Diff.Added = %+v, want exactly 1 staged line (only the well-formed candidate survives)", result.Diff)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM offer_line_item WHERE offer_id = $1 AND proposal_state = 'staged'`, offerID.UUID); n != 1 {
		t.Fatalf("staged rows = %d, want exactly 1", n)
	}
	offerLineByDescription(t, result.Offer, "Workshop")
}

func TestDraftOfferLinesStripsSecretsBeforeTheModelCall(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, offerDraftPerms)
	secret := "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	offerID, _ := seedDraftOfferWithDealActivity(ctx, t, e, "Secret-context deal",
		"Internal note: our vendor API key is "+secret+" — do not share with the client.")

	fake := ai.NewFakeClient().Script(`{"lines":[]}`)
	drafter := newOfferDrafterFixture(t, e, fake)

	if _, err := drafter.DraftOfferLines(ctx, offerID); err != nil {
		t.Fatalf("draft offer lines: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want exactly 1", len(calls))
	}
	call := calls[0]
	if call.Report.Findings == 0 {
		t.Fatalf("strip report found 0 secrets, want at least 1 (the planted api_key)")
	}
	if strings.Contains(string(call.Payload), secret) {
		t.Fatalf("the outbound payload still contains the raw secret: %s", call.Payload)
	}
}

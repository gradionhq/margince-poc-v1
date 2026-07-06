// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The E03 offer engine end-to-end (B-E03.16–.20): rate-card products
// with snapshot semantics, server-computed money totals that reject any
// client-supplied total, the draft→sent→accepted/rejected lifecycle with
// FX honesty at send and the deal-amount sync at accept, revision
// versioning, and the ADR-0055 🟡 governance of send for agent
// principals.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// offerFixture bootstraps a workspace + session and returns the seeded
// default pipeline's first open stage plus a deal to hang offers on.
func offerFixture(t *testing.T, e *env) (dealID string) {
	t.Helper()
	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID       string `json:"id"`
				Semantic string `json:"semantic"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK || len(pipelines.Data) == 0 {
		t.Fatalf("pipelines → %d %+v", status, pipelines)
	}
	var stageID string
	for _, s := range pipelines.Data[0].Stages {
		if s.Semantic == "open" {
			stageID = s.ID
			break
		}
	}
	if stageID == "" {
		t.Fatal("no open stage in the seeded pipeline")
	}
	var deal struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/deals", anyMap{
		"name": "Offer-bearing deal", "pipeline_id": pipelines.Data[0].ID, "stage_id": stageID, "source": "manual",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("create deal → %d", status)
	}
	return deal.ID
}

type offerBody struct {
	ID          string `json:"id"`
	OfferNumber string `json:"offer_number"`
	Revision    int    `json:"revision"`
	Status      string `json:"status"`
	Currency    string `json:"currency"`
	NetMinor    int64  `json:"net_minor"`
	TaxMinor    int64  `json:"tax_minor"`
	GrossMinor  int64  `json:"gross_minor"`
	FxRate      string `json:"fx_rate_to_base"`
	AcceptedAt  string `json:"accepted_at"`
	Version     int64  `json:"version"`
	LineItems   []struct {
		ID             string  `json:"id"`
		Position       int     `json:"position"`
		Description    string  `json:"description"`
		Quantity       float64 `json:"quantity"`
		UnitPriceMinor int64   `json:"unit_price_minor"`
		LineNetMinor   int64   `json:"line_net_minor"`
		LineTaxMinor   int64   `json:"line_tax_minor"`
		LineTotalMinor int64   `json:"line_total_minor"`
	} `json:"line_items"`
}

// reconcile asserts the offer's stored totals equal the sum of its
// displayed lines exactly — the P11 zero-drift bar at the wire.
func reconcile(t *testing.T, o offerBody) {
	t.Helper()
	var net, tax, total int64
	for _, l := range o.LineItems {
		net += l.LineNetMinor
		tax += l.LineTaxMinor
		total += l.LineTotalMinor
	}
	if o.NetMinor != net || o.TaxMinor != tax || o.GrossMinor != total {
		t.Fatalf("offer totals drift from lines: net %d vs %d, tax %d vs %d, gross %d vs %d",
			o.NetMinor, net, o.TaxMinor, tax, o.GrossMinor, total)
	}
}

func TestOfferProductSnapshotAndDerivedTotals(t *testing.T) {
	e := setup(t)
	e.slug = "offers-e2e"
	bootstrapWorkspaceSession(t, e, "Offers E2E", "offers@fable.test")
	dealID := offerFixture(t, e)

	// Rate-card product: money is integer minor units; SKU unique when present.
	var product struct {
		ID             string `json:"id"`
		UnitPriceMinor int64  `json:"unit_price_minor"`
		Version        int64  `json:"version"`
	}
	if status := e.call(t, "POST", "/v1/products", anyMap{
		"name": "Consulting day", "sku": "CONS-DAY", "unit": "day",
		"unit_price_minor": 120000, "currency": "EUR", "default_tax_rate": 19.0, "source": "manual",
	}, nil, &product); status != http.StatusCreated {
		t.Fatalf("create product → %d", status)
	}
	if status := e.call(t, "POST", "/v1/products", anyMap{
		"name": "Duplicate", "sku": "CONS-DAY", "unit_price_minor": 1, "currency": "EUR", "source": "manual",
	}, nil, nil); status != http.StatusConflict {
		t.Fatalf("duplicate live sku → %d, want 409", status)
	}

	// A client-supplied total is rejected 422 — totals are derived (P11).
	var problem struct {
		Code    string `json:"code"`
		Details struct {
			Errors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"errors"`
		} `json:"details"`
	}
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual", "net_minor": 999999,
	}, nil, &problem); status != http.StatusUnprocessableEntity || problem.Details.Errors[0].Code != "totals_derived" {
		t.Fatalf("client-supplied net_minor → %d %+v, want 422 totals_derived", status, problem)
	}

	// Create with a product-snapshot line and a free-form discounted line:
	// 2 days × 1200.00 @19% → net 240000, tax 45600
	// 3 × 99.99 − 10% = 269.97…→ 26997 @7% → tax 1890 (1889.79 → 1890)
	var offer offerBody
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{
			{"product_id": product.ID, "quantity": 2},
			{"description": "Licence", "quantity": 3, "unit_price_minor": 9999, "discount_pct": 10.0, "tax_rate": 7.0},
		},
	}, nil, &offer); status != http.StatusCreated {
		t.Fatalf("create offer → %d", status)
	}
	if offer.Status != "draft" || offer.Revision != 1 || !strings.HasPrefix(offer.OfferNumber, "A-") {
		t.Fatalf("created offer = %+v, want draft revision 1 with an A- number", offer)
	}
	if offer.NetMinor != 240000+26997 || offer.TaxMinor != 45600+1890 || offer.GrossMinor != 285600+28887 {
		t.Fatalf("derived totals = net %d tax %d gross %d, want 266997/47490/314487",
			offer.NetMinor, offer.TaxMinor, offer.GrossMinor)
	}
	reconcile(t, offer)

	// Snapshot semantics (B-E03.17): re-pricing the product must NOT
	// mutate the existing line.
	if status := e.call(t, "PATCH", "/v1/products/"+product.ID, anyMap{"unit_price_minor": 999999}, nil, nil); status != http.StatusOK {
		t.Fatalf("re-price product → %d", status)
	}
	var after offerBody
	if status := e.call(t, "GET", "/v1/offers/"+offer.ID, nil, nil, &after); status != http.StatusOK {
		t.Fatalf("get offer → %d", status)
	}
	if after.LineItems[0].UnitPriceMinor != 120000 || after.NetMinor != offer.NetMinor {
		t.Fatalf("product re-price mutated the line snapshot: %+v", after.LineItems[0])
	}

	// A total smuggled into a line-item write is 422 too.
	if status := e.call(t, "POST", "/v1/offers/"+offer.ID+"/line-items", anyMap{
		"description": "Sneaky", "quantity": 1, "unit_price_minor": 100, "line_total_minor": 1,
	}, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Fatalf("client-supplied line_total_minor → %d, want 422", status)
	}

	// Draft line CRUD recomputes the totals every time.
	var withLine offerBody
	if status := e.call(t, "POST", "/v1/offers/"+offer.ID+"/line-items", anyMap{
		"description": "Support", "quantity": 1.5, "unit_price_minor": 20000, "tax_rate": 19.0,
	}, nil, &withLine); status != http.StatusCreated {
		t.Fatalf("add line → %d", status)
	}
	if withLine.NetMinor != offer.NetMinor+30000 {
		t.Fatalf("net after add = %d, want %d", withLine.NetMinor, offer.NetMinor+30000)
	}
	reconcile(t, withLine)

	lineID := withLine.LineItems[len(withLine.LineItems)-1].ID
	var updated offerBody
	if status := e.call(t, "PATCH", "/v1/offers/"+offer.ID+"/line-items/"+lineID, anyMap{
		"quantity": 2.0,
	}, nil, &updated); status != http.StatusOK {
		t.Fatalf("update line → %d", status)
	}
	if updated.NetMinor != offer.NetMinor+40000 {
		t.Fatalf("net after quantity change = %d, want %d", updated.NetMinor, offer.NetMinor+40000)
	}
	reconcile(t, updated)

	var removed offerBody
	if status := e.call(t, "DELETE", "/v1/offers/"+offer.ID+"/line-items/"+lineID, nil, nil, &removed); status != http.StatusOK {
		t.Fatalf("remove line → %d", status)
	}
	if removed.NetMinor != offer.NetMinor {
		t.Fatalf("net after remove = %d, want %d", removed.NetMinor, offer.NetMinor)
	}
	reconcile(t, removed)
}

func TestOfferLifecycleSendAcceptRegenerate(t *testing.T) {
	e := setup(t)
	e.slug = "offers-life"
	bootstrapWorkspaceSession(t, e, "Offers Life", "life@fable.test")
	dealID := offerFixture(t, e)

	var wsID string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatal(err)
	}

	createOffer := func(currency string) offerBody {
		t.Helper()
		var o offerBody
		if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
			"currency": currency, "source": "manual",
			"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
		}, nil, &o); status != http.StatusCreated {
			t.Fatalf("create %s offer → %d", currency, status)
		}
		return o
	}

	// An empty draft has nothing to send.
	var empty offerBody
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
	}, nil, &empty); status != http.StatusCreated {
		t.Fatalf("create empty offer → %d", status)
	}
	if status := e.call(t, "POST", "/v1/offers/"+empty.ID+"/send", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("send empty offer → %d, want 422", status)
	}

	// FX honesty (RT-PR-C2): sending a USD offer with no daily rate is a
	// hard 422 — never rate=1. With a rate, send freezes it.
	usd := createOffer("USD")
	var problem struct {
		Detail string `json:"detail"`
	}
	if status := e.call(t, "POST", "/v1/offers/"+usd.ID+"/send", nil, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Fatalf("send with missing fx rate → %d, want 422", status)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		 VALUES ($1, 'USD', 'EUR', 0.9200000000, current_date)`, wsID); err != nil {
		t.Fatal(err)
	}
	var sent offerBody
	if status := e.call(t, "POST", "/v1/offers/"+usd.ID+"/send", nil, nil, &sent); status != http.StatusOK {
		t.Fatalf("send with seeded fx rate → %d", status)
	}
	if sent.Status != "sent" || !strings.HasPrefix(sent.FxRate, "0.92") {
		t.Fatalf("sent offer = status %q fx %q, want sent with the frozen 0.92 rate", sent.Status, sent.FxRate)
	}

	// A sent offer is immutable: header, lines and re-send all refuse.
	if status := e.call(t, "PATCH", "/v1/offers/"+usd.ID, anyMap{"intro_text": "rewrite"}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("patch sent offer → %d, want 422", status)
	}
	if status := e.call(t, "POST", "/v1/offers/"+usd.ID+"/line-items", anyMap{
		"description": "Late line", "quantity": 1, "unit_price_minor": 1,
	}, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("add line to sent offer → %d, want 422", status)
	}
	if status := e.call(t, "POST", "/v1/offers/"+usd.ID+"/send", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("re-send sent offer → %d, want 422", status)
	}

	// Accept: status flips, accepted_at lands, and the DEAL takes the
	// accepted gross as its headline amount (forecast honesty).
	var accepted offerBody
	if status := e.call(t, "POST", "/v1/offers/"+usd.ID+"/accept", nil, nil, &accepted); status != http.StatusOK {
		t.Fatalf("accept → %d", status)
	}
	if accepted.Status != "accepted" || accepted.AcceptedAt == "" {
		t.Fatalf("accepted offer = %+v, want status accepted with accepted_at", accepted)
	}
	var deal struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	}
	if status := e.call(t, "GET", "/v1/deals/"+dealID, nil, nil, &deal); status != http.StatusOK {
		t.Fatalf("get deal → %d", status)
	}
	if deal.AmountMinor != accepted.GrossMinor || deal.Currency != "USD" {
		t.Fatalf("deal after accept = %d %s, want the accepted gross %d USD",
			deal.AmountMinor, deal.Currency, accepted.GrossMinor)
	}
	// Accept is terminal: a second accept refuses.
	if status := e.call(t, "POST", "/v1/offers/"+usd.ID+"/accept", nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("double accept → %d, want 422", status)
	}

	// Reject: a second sent offer takes the decline (with reason).
	eur := createOffer("EUR")
	if e.call(t, "POST", "/v1/offers/"+eur.ID+"/send", nil, nil, nil) != http.StatusOK {
		t.Fatal("send EUR offer failed")
	}
	var rejected offerBody
	if status := e.call(t, "POST", "/v1/offers/"+eur.ID+"/reject", anyMap{"reason": "budget cut"}, nil, &rejected); status != http.StatusOK || rejected.Status != "rejected" {
		t.Fatalf("reject → %d %q", status, rejected.Status)
	}

	// Regenerate: a third sent offer mints revision 2 as a fresh draft
	// and the original becomes superseded — never mutated in place.
	third := createOffer("EUR")
	if e.call(t, "POST", "/v1/offers/"+third.ID+"/send", nil, nil, nil) != http.StatusOK {
		t.Fatal("send third offer failed")
	}
	var nextRev offerBody
	if status := e.call(t, "POST", "/v1/offers/"+third.ID+"/regenerate", nil, nil, &nextRev); status != http.StatusCreated {
		t.Fatalf("regenerate → %d", status)
	}
	if nextRev.Revision != 2 || nextRev.Status != "draft" || nextRev.OfferNumber != third.OfferNumber || len(nextRev.LineItems) != 1 {
		t.Fatalf("regenerated = %+v, want draft revision 2 of %s with the copied line", nextRev, third.OfferNumber)
	}
	var prior offerBody
	if status := e.call(t, "GET", "/v1/offers/"+third.ID, nil, nil, &prior); status != http.StatusOK || prior.Status != "superseded" {
		t.Fatalf("prior revision after regenerate = %d %q, want superseded", 200, prior.Status)
	}

	// The event trail: every lifecycle fact shipped through the outbox.
	var created, sentN, acceptedN, rejectedN, supersededN, dealUpdated int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FILTER (WHERE envelope->>'type' = 'offer.created'),
		       count(*) FILTER (WHERE envelope->>'type' = 'offer.sent'),
		       count(*) FILTER (WHERE envelope->>'type' = 'offer.accepted'),
		       count(*) FILTER (WHERE envelope->>'type' = 'offer.rejected'),
		       count(*) FILTER (WHERE envelope->>'type' = 'offer.superseded'),
		       count(*) FILTER (WHERE envelope->>'type' = 'deal.updated')
		FROM event_outbox`).Scan(&created, &sentN, &acceptedN, &rejectedN, &supersededN, &dealUpdated); err != nil {
		t.Fatal(err)
	}
	// 4 creates + 1 regenerate-create; 3 sends; 1 accept (+ its paired
	// deal.updated); 1 reject; 1 supersede.
	if created != 5 || sentN != 3 || acceptedN != 1 || rejectedN != 1 || supersededN != 1 || dealUpdated < 1 {
		t.Fatalf("offer event trail: created=%d sent=%d accepted=%d rejected=%d superseded=%d deal.updated=%d",
			created, sentN, acceptedN, rejectedN, supersededN, dealUpdated)
	}
}

// ADR-0055 + ADR-0036 on the offer surface: an agent may draft (🟢) but
// sending leaves the workspace — the 🟡 gate stages an approval only a
// human can decide, and the approved retry redeems the token.
func TestOfferAgentSendRequiresApproval(t *testing.T) {
	e := setup(t)
	e.slug = "offers-agent"
	bootstrapWorkspaceSession(t, e, "Offers Agent", "agent@fable.test")
	dealID := offerFixture(t, e)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "offer agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// 🟢 create_record: the agent drafts the offer, provenance is the agent.
	var offer offerBody
	if status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "mcp",
		"line_items": []anyMap{{"description": "Pilot", "quantity": 1, "unit_price_minor": 250000, "tax_rate": 19.0}},
	}, bearer, &offer); status != http.StatusCreated {
		t.Fatalf("agent 🟢 offer draft → %d", status)
	}

	// 🟡 send: refused with a staged approval; the offer stays draft.
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status := e.call(t, "POST", "/v1/offers/"+offer.ID+"/send", nil, bearer, &problem); status != http.StatusForbidden || problem.Code != "approval_required" {
		t.Fatalf("agent send → %d %q, want 403 approval_required", status, problem.Code)
	}
	var still offerBody
	if status := e.call(t, "GET", "/v1/offers/"+offer.ID, nil, bearer, &still); status != http.StatusOK || still.Status != "draft" {
		t.Fatalf("offer after staged send = %q, want draft (no effect before approval)", still.Status)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The agent cannot approve its own staging; the human can.
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, bearer, nil); status != http.StatusForbidden {
		t.Fatalf("agent self-approval → %d, want 403", status)
	}
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("human approve → %d", status)
	}

	// The identical retry with the token executes exactly once.
	withToken := map[string]string{"Authorization": "Bearer " + minted.Token, "X-Approval-Token": approvalID}
	var sent offerBody
	if status := e.call(t, "POST", "/v1/offers/"+offer.ID+"/send", nil, withToken, &sent); status != http.StatusOK || sent.Status != "sent" {
		t.Fatalf("approved send retry → %d %q, want 200 sent", status, sent.Status)
	}
	if e.call(t, "POST", "/v1/offers/"+offer.ID+"/send", nil, withToken, nil) == http.StatusOK {
		t.Fatal("a consumed approval token authorized a second send")
	}

	// Recording the buyer's decision is a human attestation: the agent is
	// rejected outright on accept, whatever its scopes.
	if status := e.call(t, "POST", "/v1/offers/"+offer.ID+"/accept", nil, bearer, nil); status != http.StatusForbidden {
		t.Fatalf("agent accept → %d, want 403 (human-only)", status)
	}
}

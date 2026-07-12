// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The regenerateOffer HTTP round trip with AI-drafted regeneration wired
// (arc 4b, T9): compose's regenerateOffer shadow (offerregenerate.go)
// runs the mechanical mint FIRST — deals' own revision-mint/supersede
// backbone (offer_lifecycle.go), unchanged — and, only when
// WithOfferDraft is wired, layers the evidence-gated AI draft on top of
// the freshly minted revision. The no-offerDrafter-wired mechanical-only
// path is exercised by TestOfferLifecycleSendAcceptRegenerate
// (offers_integration_test.go, whose default harness wires no model path
// at all); this file adds the AI-wired grounded/ungrounded cases that
// suite cannot reach.

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/modules/signals"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// setupWithOfferDraft boots the e2e harness with the offer-drafting brain
// wired over the real search retriever — the SAME retrieval seam
// production wiring rides (cmd/api's offerDraftOptions), just fed a
// scripted fake in place of the routed model (the --ai-fake dev path).
// The retriever needs its own pool at option-construction time, before
// setupWithOptions has opened the harness's own — the same "a boot
// option built from a pool opens ITS OWN, ahead of the harness's" shape
// SchemaPool(t) already established for WithSchemaPool. fake is handed
// back so the caller can script it AFTER seeding its deal context (the
// scripted candidate needs to cite a source_id minted during that seed).
func setupWithOfferDraft(t *testing.T) (*env, *ai.FakeClient) {
	t.Helper()
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if appDSN == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	pool, err := database.NewPool(context.Background(), appDSN)
	if err != nil {
		t.Fatalf("opening the offer-draft retriever's pool: %v", err)
	}
	t.Cleanup(pool.Close)
	retriever := search.NewRetriever(search.NewStore(pool), nil)
	fake := ai.NewFakeClient()
	return setupWithOptions(t, compose.WithOfferDraft(fake, retriever)), fake
}

// seedDealContextActivity logs one activity linked to the deal — "the
// deal's captured context" the offerDrafter reads — and returns the
// source_id a scripted candidate must cite to ground against it.
func seedDealContextActivity(t *testing.T, e *env, wsID, dealID, subject string) string {
	t.Helper()
	activityID := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		 VALUES ($1, $2, 'note', $3, '2026-07-01T10:00:00Z', 'manual', 'human:x')`,
		activityID, wsID, subject); err != nil {
		t.Fatalf("seed deal context activity: %v", err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, deal_id) VALUES ($1, $2, 'deal', $3)`,
		wsID, activityID, dealID); err != nil {
		t.Fatalf("link deal context activity: %v", err)
	}
	return "activity:" + activityID.String()
}

// wsIDBySlug resolves the workspace's own id — the raw-SQL activity seed
// above needs it, and the HTTP surface never exposes a workspace's id by
// its slug.
func wsIDBySlug(t *testing.T, e *env, slug string) string {
	t.Helper()
	var wsID string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM workspace WHERE slug = $1`, slug).Scan(&wsID); err != nil {
		t.Fatalf("resolve workspace id for slug %q: %v", slug, err)
	}
	return wsID
}

// stagedLineCount counts proposal_state='staged' rows on one offer — the
// same check offerdraft_integration_test.go's Env.WsCount runs, over
// this file's own raw owner connection.
func stagedLineCount(t *testing.T, e *env, offerID string) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM offer_line_item WHERE offer_id = $1 AND proposal_state = 'staged'`, offerID).Scan(&n); err != nil {
		t.Fatalf("count staged offer lines: %v", err)
	}
	return n
}

// regeneratedOffer is the regenerate response shape this suite reads —
// offerBody's (offers_integration_test.go) money/status fields plus the
// three AI fields this operation alone ever populates.
type regeneratedOffer struct {
	ID               string  `json:"id"`
	Revision         int     `json:"revision"`
	Status           string  `json:"status"`
	NetMinor         int64   `json:"net_minor"`
	TaxMinor         int64   `json:"tax_minor"`
	GrossMinor       int64   `json:"gross_minor"`
	AiGenerated      *bool   `json:"ai_generated"`
	AiDisclosure     *string `json:"ai_disclosure"`
	DiffFromPrevious *struct {
		Added []struct {
			Description string `json:"description"`
		} `json:"added"`
	} `json:"diff_from_previous"`
	LineItems []struct {
		Description string `json:"description"`
	} `json:"line_items"`
}

func lineDescriptions(o regeneratedOffer) []string {
	out := make([]string, len(o.LineItems))
	for i, l := range o.LineItems {
		out[i] = l.Description
	}
	return out
}

func containsDescription(descriptions []string, want string) bool {
	for _, d := range descriptions {
		if d == want {
			return true
		}
	}
	return false
}

func TestOfferRegenerateHTTP_GroundedAIDraftStagesAndDisclosesWithoutMovingTotals(t *testing.T) {
	e, fake := setupWithOfferDraft(t)
	e.slug = "offer-regen-ai"
	bootstrapWorkspaceSession(t, e, "Offer Regen AI", "offerai@fable.test")
	wsID := wsIDBySlug(t, e, e.slug)
	dealID := offerFixture(t, e)
	source := seedDealContextActivity(t, e, wsID, dealID,
		`Client said: "we'd want a kickoff workshop" and agreed to 20000 cents for it.`)
	fake.Script(`{"lines":[
		{"description":"Kickoff workshop","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"agreed to 20000 cents for it","source_id":"` + source + `",
		 "conversation_price_minor":20000}
	]}`)

	sent := createOfferInCurrency(t, e, dealID, "EUR")
	if status := e.call(t, "POST", "/v1/offers/"+sent.ID+"/send", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("send offer for AI regenerate → %d", status)
	}

	var regenerated regeneratedOffer
	if status := e.call(t, "POST", "/v1/offers/"+sent.ID+"/regenerate", nil, nil, &regenerated); status != http.StatusCreated {
		t.Fatalf("AI regenerate → %d %+v, want 201", status, regenerated)
	}
	if regenerated.Revision != 2 || regenerated.Status != "draft" {
		t.Fatalf("regenerated = %+v, want draft revision 2", regenerated)
	}
	if regenerated.AiGenerated == nil || !*regenerated.AiGenerated {
		t.Fatalf("ai_generated = %v, want true (the candidate grounds)", regenerated.AiGenerated)
	}
	if regenerated.AiDisclosure == nil || *regenerated.AiDisclosure != signals.Art50Disclosure {
		t.Fatalf("ai_disclosure = %v, want the Art.50 disclosure", regenerated.AiDisclosure)
	}
	if regenerated.DiffFromPrevious == nil || len(regenerated.DiffFromPrevious.Added) != 1 ||
		regenerated.DiffFromPrevious.Added[0].Description != "Kickoff workshop" {
		t.Fatalf("diff_from_previous = %+v, want exactly one added line, \"Kickoff workshop\"", regenerated.DiffFromPrevious)
	}
	descriptions := lineDescriptions(regenerated)
	if !containsDescription(descriptions, "Retainer") || !containsDescription(descriptions, "Kickoff workshop") {
		t.Fatalf("line_items = %v, want both the cloned Retainer line and the staged Kickoff workshop line", descriptions)
	}

	// Never AI-computed: the staged line is excluded from the derived
	// totals until a human accepts it — the response's money is exactly
	// the cloned Retainer line's totals (500000 @19%).
	if regenerated.NetMinor != 500000 || regenerated.TaxMinor != 95000 || regenerated.GrossMinor != 595000 {
		t.Fatalf("totals = %d/%d/%d, want 500000/95000/595000 (the staged AI line must not move them)",
			regenerated.NetMinor, regenerated.TaxMinor, regenerated.GrossMinor)
	}
	if n := stagedLineCount(t, e, regenerated.ID); n != 1 {
		t.Fatalf("staged rows on the new revision = %d, want exactly 1", n)
	}
}

func TestOfferRegenerateHTTP_UngroundedCandidateFallsBackToMechanicalCloneOnly(t *testing.T) {
	e, fake := setupWithOfferDraft(t)
	e.slug = "offer-regen-ungrounded"
	bootstrapWorkspaceSession(t, e, "Offer Regen Ungrounded", "ungrounded@fable.test")
	wsID := wsIDBySlug(t, e, e.slug)
	dealID := offerFixture(t, e)
	seedDealContextActivity(t, e, wsID, dealID, "Client mentioned they liked our website.")
	fake.Script(`{"lines":[
		{"description":"Bespoke consulting package","quantity":"1","tax_rate":"19.00",
		 "evidence_snippet":"this text never appears anywhere in the deal's context","source_id":"activity:` + ids.NewV7().String() + `",
		 "conversation_price_minor":50000}
	]}`)

	sent := createOfferInCurrency(t, e, dealID, "EUR")
	if status := e.call(t, "POST", "/v1/offers/"+sent.ID+"/send", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("send offer for AI regenerate → %d", status)
	}

	var regenerated regeneratedOffer
	if status := e.call(t, "POST", "/v1/offers/"+sent.ID+"/regenerate", nil, nil, &regenerated); status != http.StatusCreated {
		t.Fatalf("regenerate with ungrounded candidate → %d %+v, want 201", status, regenerated)
	}
	if regenerated.AiGenerated != nil && *regenerated.AiGenerated {
		t.Fatalf("ai_generated = true, want false/absent — the only candidate is ungrounded")
	}
	if regenerated.AiDisclosure != nil {
		t.Fatalf("ai_disclosure = %v, want absent on an honest empty draft", *regenerated.AiDisclosure)
	}
	if regenerated.DiffFromPrevious != nil {
		t.Fatalf("diff_from_previous = %+v, want absent on an honest empty draft", regenerated.DiffFromPrevious)
	}
	descriptions := lineDescriptions(regenerated)
	if len(descriptions) != 1 || descriptions[0] != "Retainer" {
		t.Fatalf("line_items = %v, want only the mechanically cloned Retainer line — never fabricate", descriptions)
	}
	if n := stagedLineCount(t, e, regenerated.ID); n != 0 {
		t.Fatalf("staged rows for the ungrounded candidate = %d, want 0", n)
	}
}

// TestOfferRegenerateHTTP_NonSentOfferRefusesMechanically proves the
// shadow's error path: the mechanical Store.RegenerateOffer call still
// answers its own typed refusal (deals.WriteOfferError, the SAME mapping
// deals.Handlers.RegenerateOffer would have written) BEFORE offerDrafter
// is ever consulted — a draft offer has nothing to regenerate from,
// whether or not an AI brain is wired.
func TestOfferRegenerateHTTP_NonSentOfferRefusesMechanically(t *testing.T) {
	e, _ := setupWithOfferDraft(t)
	e.slug = "offer-regen-not-sent"
	bootstrapWorkspaceSession(t, e, "Offer Regen Not Sent", "notsent@fable.test")
	dealID := offerFixture(t, e)
	draft := createOfferInCurrency(t, e, dealID, "EUR")

	var problem struct {
		Code    string `json:"code"`
		Details struct {
			Errors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"errors"`
		} `json:"details"`
	}
	if status := e.call(t, "POST", "/v1/offers/"+draft.ID+"/regenerate", nil, nil, &problem); status != http.StatusUnprocessableEntity ||
		len(problem.Details.Errors) == 0 || problem.Details.Errors[0].Code != "offer_not_sent" {
		t.Fatalf("regenerate a draft offer → %d %+v, want 422 offer_not_sent", status, problem)
	}
}

// TestOfferRegenerateHTTP_MalformedModelResponseFallsBackToMechanicalClone
// proves the drafting-failure degrade path: a model response DraftOfferLines
// cannot even parse (a real failure mode, not an evidence-gate drop) logs a
// warning and serves the mechanical mint that already committed — the
// caller never loses the revision it just minted behind a 500.
func TestOfferRegenerateHTTP_MalformedModelResponseFallsBackToMechanicalClone(t *testing.T) {
	e, fake := setupWithOfferDraft(t)
	e.slug = "offer-regen-malformed"
	bootstrapWorkspaceSession(t, e, "Offer Regen Malformed", "malformed@fable.test")
	wsID := wsIDBySlug(t, e, e.slug)
	dealID := offerFixture(t, e)
	seedDealContextActivity(t, e, wsID, dealID, "Client asked about a custom rollout plan.")
	fake.Script(`this is not json at all`)

	sent := createOfferInCurrency(t, e, dealID, "EUR")
	if status := e.call(t, "POST", "/v1/offers/"+sent.ID+"/send", nil, nil, nil); status != http.StatusOK {
		t.Fatalf("send offer for AI regenerate → %d", status)
	}

	var regenerated regeneratedOffer
	if status := e.call(t, "POST", "/v1/offers/"+sent.ID+"/regenerate", nil, nil, &regenerated); status != http.StatusCreated {
		t.Fatalf("regenerate over a malformed model response → %d %+v, want 201 (the mechanical mint already committed)", status, regenerated)
	}
	if regenerated.Revision != 2 || regenerated.Status != "draft" {
		t.Fatalf("regenerated = %+v, want draft revision 2 despite the drafting failure", regenerated)
	}
	if regenerated.AiGenerated != nil && *regenerated.AiGenerated {
		t.Fatalf("ai_generated = true, want false/absent — drafting failed outright, nothing was staged")
	}
	descriptions := lineDescriptions(regenerated)
	if len(descriptions) != 1 || descriptions[0] != "Retainer" {
		t.Fatalf("line_items = %v, want only the mechanically cloned Retainer line", descriptions)
	}
}

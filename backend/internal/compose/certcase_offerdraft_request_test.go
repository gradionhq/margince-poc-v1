// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The offer-draft case beside the orchestrator it stands for: the same fixture,
// answered by the same model text, must produce the same request and stage the
// same lines on both sides. Every other test in this case's files would stay
// green through the drift this one catches — a case that has quietly become a
// copy of the path it certifies.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// The claim this case makes is that it certifies the shipped path. The proof is
// running the shipped path beside it: the same fixture, answered by the same
// model text, must produce the same request and stage the same lines in the
// orchestrator as in the case.
//
// What it cannot cover is DraftOfferLines' own orchestration — the offer read,
// the staging write and the disclosure it stamps all need a database, and the
// integration lane owns them. What it does cover is everything between the two
// reads and the staged lines, which is everything a model touches.
func TestOfferDraftCaseRunsWhatProductionRuns(t *testing.T) {
	cases := []struct {
		name  string
		reply func(t *testing.T, req model.Request) string
		// wantStaged is what the orchestrator carries into AddStagedOfferLines,
		// and the case owes the same verdict about the same lines.
		wantStaged []deals.StagedOfferLineInput
		wantResult string
	}{
		{
			name: "one line priced by the conversation, one by the rate card",
			reply: func(t *testing.T, req model.Request) string {
				return draftReply(
					kickoffLine(`"conversation_price_minor":20000`),
					supportLine(`"product_id":"`+catalogIDFor(t, req, offerDraftSupportPlan)+`"`),
				)
			},
			wantStaged: []deals.StagedOfferLineInput{
				{
					Description: "Kickoff workshop", Quantity: "1", UnitPriceMinor: 20000, TaxRate: "19.00",
					Evidence:      deals.StagedOfferLineEvidence{Snippet: "agreed to 20000 cents for it", SourceID: offerDraftKickoffSource},
					PriceGrounded: true,
				},
				{
					Description: "Support plan", Quantity: "1", UnitPriceMinor: 5000, TaxRate: "19.00",
					Evidence:      deals.StagedOfferLineEvidence{Snippet: "quote the standard support plan", SourceID: offerDraftSupportSource},
					PriceGrounded: true,
				},
			},
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name: "one line grounded, one citing what the context never said",
			reply: func(*testing.T, model.Request) string {
				return draftReply(
					kickoffLine(`"conversation_price_minor":20000`),
					draftedLine("Invented add-on", "a discount nobody discussed", offerDraftKickoffSource),
				)
			},
			wantStaged: []deals.StagedOfferLineInput{{
				Description: "Kickoff workshop", Quantity: "1", UnitPriceMinor: 20000, TaxRate: "19.00",
				Evidence:      deals.StagedOfferLineEvidence{Snippet: "agreed to 20000 cents for it", SourceID: offerDraftKickoffSource},
				PriceGrounded: true,
			}},
			wantResult: aitasks.OutcomeAccepted,
		},
		{
			name: "nothing the orchestrator can stage",
			reply: func(*testing.T, model.Request) string {
				return draftReply(draftedLine("Kickoff workshop", "agreed to a 40% discount", offerDraftKickoffSource))
			},
			wantStaged: nil,
			wantResult: aitasks.OutcomeInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := prepareOfferDraftCase(t, offerDraftDealFixture(), offerDraftKickoffExpected(t))

			outcome, trace := runOfferDraftCase(t, c, func(req model.Request) string { return tc.reply(t, req) })

			assertOrchestratorAgrees(t, c, trace, tc.wantStaged)
			if outcome.Result != tc.wantResult {
				t.Fatalf("Result = %q (%s), want %q", outcome.Result, outcome.Detail, tc.wantResult)
			}
		})
	}
}

// assertOrchestratorAgrees runs the shipped path over the same fixture, answered
// with the text the case's model answered with, so the only thing that can
// differ between the two is the code.
func assertOrchestratorAgrees(
	t *testing.T, c *offerDraftCase, trace aitasks.Trace, wantStaged []deals.StagedOfferLineInput,
) {
	t.Helper()
	brain := &replyBrainStub{response: model.Response{Text: trace.Output}}
	orchestrator := offerDrafter{brain: brain, rateCard: c.drafter.rateCard}
	candidates, err := orchestrator.draftCandidates(context.Background(), c.dealContext, c.catalog)
	if err != nil {
		t.Fatalf("the orchestrator refused the reply outright: %v", err)
	}
	staged, err := orchestrator.groundOfferLines(context.Background(), candidates, c.dealContext, c.currency)
	if err != nil {
		t.Fatalf("the gate faulted: %v", err)
	}

	assertSameStagedLines(t, "the orchestrator", staged, wantStaged)
	// The case asks the gate one candidate at a time so it can say WHICH line was
	// dropped; this is the proof that means the same thing.
	certified, refusals, err := c.gate(candidates)
	if err != nil {
		t.Fatalf("the case's gate faulted where the orchestrator's did not: %v", err)
	}
	assertSameStagedLines(t, "the case", certified, staged)
	if len(refusals) != len(candidates)-len(certified) {
		t.Errorf("the case names %d refusals for %d dropped candidates: %v",
			len(refusals), len(candidates)-len(certified), refusals)
	}
	assertSameOfferDraftRequest(t, brain.request, trace.Requests[0])
}

// faultingRateCard is the catalogue read failing for a reason that is NOT "no
// such product" — a real infra or permission fault. It is the one answer this
// case's own rate card cannot give, and the whole point of the ladder's
// distinction between a lookup that says no and a lookup that cannot answer.
type faultingRateCard struct{ err error }

func (f faultingRateCard) GetProduct(
	context.Context, ids.ProductID, storekit.ArchivedFilter,
) (crmcontracts.Product, error) {
	return crmcontracts.Product{}, f.err
}

// A lookup fault is not a grounding verdict, and it does not leave a partial
// draft behind either: the ladder propagates it, the gate returns it, and
// production abandons the whole draft — the lines that had already grounded
// never reach a human. So a case that went on grading the survivors would grade
// an offer nobody is shown.
//
// The reply is written so the two sides can disagree: its FIRST line grounds and
// prices itself off the conversation, and only the SECOND reaches the catalogue.
func TestOfferDraftCaseAbandonsTheDraftProductionAbandons(t *testing.T) {
	c := prepareOfferDraftCase(t, offerDraftDealFixture(), offerDraftKickoffExpected(t))
	fault := errors.New("the products table is unreachable")
	c.drafter.rateCard = faultingRateCard{err: fault}

	outcome, trace := runOfferDraftCase(t, c, func(req model.Request) string {
		return draftReply(
			kickoffLine(`"conversation_price_minor":20000`),
			supportLine(`"product_id":"`+catalogIDFor(t, req, offerDraftSupportPlan)+`"`),
		)
	})

	orchestrator := offerDrafter{
		brain:    &replyBrainStub{response: model.Response{Text: trace.Output}},
		rateCard: c.drafter.rateCard,
	}
	candidates, err := orchestrator.draftCandidates(context.Background(), c.dealContext, c.catalog)
	if err != nil {
		t.Fatalf("the orchestrator refused the reply outright: %v", err)
	}
	staged, err := orchestrator.groundOfferLines(context.Background(), candidates, c.dealContext, c.currency)
	if err == nil {
		t.Fatal("the orchestrator ground this reply without a fault, so the scenario proves nothing")
	}
	if len(staged) != 0 {
		t.Errorf("the orchestrator staged %d lines beside the fault it raised", len(staged))
	}

	if outcome.Result != aitasks.OutcomeInvalid {
		t.Fatalf("Result = %q (%s), want the run to report the draft production abandoned",
			outcome.Result, outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, fault.Error()) {
		t.Errorf("Detail = %q, want it to name the fault that ended the draft", outcome.Detail)
	}
}

func assertSameStagedLines(t *testing.T, whose string, got, want []deals.StagedOfferLineInput) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s staged %d lines, want %d: %+v", whose, len(got), len(want), got)
	}
	for i, line := range want {
		if got[i] != line {
			t.Errorf("%s staged %+v at %d, want %+v", whose, got[i], i, line)
		}
	}
}

// assertSameOfferDraftRequest compares two requests for the same deal. The fence
// marker is minted per call, so it is normalised away — every other byte of a
// request the certification lane claims production sends must match the one
// production sent.
func assertSameOfferDraftRequest(t *testing.T, production, certified model.Request) {
	t.Helper()
	normalize := func(req model.Request) model.Request {
		marker, declared := promptfence.MarkerIn(req.System)
		if !declared {
			t.Fatalf("the request declares no data boundary: %q", req.System)
		}
		out := req
		out.System = strings.ReplaceAll(req.System, marker, "MARKER")
		out.Messages = make([]model.Message, len(req.Messages))
		for i, message := range req.Messages {
			out.Messages[i] = model.Message{
				Role:    message.Role,
				Content: strings.ReplaceAll(message.Content, marker, "MARKER"),
			}
		}
		return out
	}
	production, certified = normalize(production), normalize(certified)
	if production.System != certified.System {
		t.Errorf("the certified system prompt is not production's:\n%q\n%q", certified.System, production.System)
	}
	if len(production.Messages) != len(certified.Messages) {
		t.Fatalf("the certified request carries %d turns, production sent %d",
			len(certified.Messages), len(production.Messages))
	}
	for i, message := range production.Messages {
		if certified.Messages[i] != message {
			t.Errorf("certified turn %d = %+v, production sent %+v", i, certified.Messages[i], message)
		}
	}
	// This site sends no response schema, and the case must not invent one: a
	// certified request carrying a schema production does not send would measure
	// a model that was told the shape it must answer in.
	if certified.MaxTokens != production.MaxTokens ||
		string(certified.ResponseSchema) != string(production.ResponseSchema) ||
		certified.SecretStripper == nil {
		t.Errorf("the certified request lost the governed bounds production sends: %+v", certified)
	}
}

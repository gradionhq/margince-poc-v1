// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The shipped corpus's third self-test, for the scenarios whose right answer is
// silence.
//
// Preparation proves a scenario is runnable; it says nothing about whether the
// scenario can tell a right answer from a wrong one. That gap is worst here. A
// scenario asserting an abstention passes when the model produces nothing, and
// "produced nothing" is what a fabricating model looks like too once a gate has
// refused everything it claimed — so an abstention scenario that graded both the
// same would pass whatever the model did, which is worse than no scenario at
// all: it would report a fabrication-rate floor that was never measured.
//
// So each of these scenarios is run twice against the case its site binds: once
// with the answer it says is correct, and once with the fabrication it exists to
// catch. The first must reach the outcome the scenario expects and the second
// must not. The replies are canned rather than generated — what is under test is
// the scenario's discrimination, not a model's behaviour.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// cannedCompleter answers every request with one reply. A canned reply is the
// point: these tests measure what the scenario does with an answer, not what a
// model would have answered.
type cannedCompleter struct{ reply string }

func (c cannedCompleter) Complete(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Text: c.reply}, nil
}

// abstentionProof is one committed scenario, the reply it says is right, and the
// fabrication it exists to refuse.
type abstentionProof struct {
	scenario string
	// correct must reach the scenario's own expect.outcome.
	correct string
	// fabricated must NOT, and wantFabricated says which outcome it reaches
	// instead — the two failures are different findings and a proof that
	// accepted either would not notice them swapping.
	fabricated     string
	wantFabricated string
	// wantDetail is what the refusal must name, so a reviewer reading the record
	// learns which fabrication happened rather than that one did.
	wantDetail string
}

// The passage id every one of these fixtures numbers its only passage as. Both
// site_extract fixtures reduce to a single passage, which is what makes the
// citation in a fabricated reply resolvable — an unresolvable id would be
// refused for the wrong reason and prove nothing about the claim.
const abstentionPassageID = "s0"

func TestEachAbstentionScenarioCatchesTheFabricationItTargets(t *testing.T) {
	proofs := []abstentionProof{
		{
			// A JS-only shell states nothing, so any field is invented. The
			// fabrication that matters is the one the citation gate CANNOT refuse:
			// icp is a paraphrase field, checked for overlap and never dropped for
			// it, so it survives to be graded — and only the scenario's negative
			// claim can call it wrong.
			scenario:       "js_only_page_yields_no_fabrication",
			correct:        profileReplyJSON(),
			fabricated:     profileReplyJSON(profileClaimJSON("icp", "mid-market grocery chains")),
			wantFabricated: aitasks.OutcomeWrongAnswer,
			wantDetail:     "icp",
		},
		{
			// The same page, the other fabrication: a hard-gated field whose value
			// no passage carries. The gate refuses it, nothing survives, and the
			// reply is unusable — which must not be scored as the abstention it
			// superficially resembles.
			scenario:       "js_only_page_yields_no_fabrication",
			correct:        profileReplyJSON(),
			fabricated:     profileReplyJSON(profileClaimJSON("display_name", "LoadingShell Inc.")),
			wantFabricated: aitasks.OutcomeInvalid,
			wantDetail:     "display_name",
		},
		{
			// The whole point of the legal scenario: the imprint really does contain
			// this entity verbatim, so the citation gate passes it and the record
			// would read "accepted" without a claim about what must NOT be grounded.
			scenario:       "one_legal_page_naming_two_entities",
			correct:        profileReplyJSON(),
			fabricated:     profileReplyJSON(profileClaimJSON("legal_name", "Kestrel Fold Consulting GmbH")),
			wantFabricated: aitasks.OutcomeWrongAnswer,
			wantDetail:     "Kestrel Fold Consulting GmbH",
		},
		{
			// Nothing was captured, so every citation is invented and the no-guess
			// gate refuses the line. That is a usable-reply failure, not silence.
			scenario:       "no_captured_context_yields_no_lines",
			correct:        `{"lines":[]}`,
			fabricated:     offerDraftLineJSON("Consulting services", "as discussed", "activity:invented"),
			wantFabricated: aitasks.OutcomeInvalid,
			wantDetail:     "Consulting services",
		},
	}

	scenarios := loadShippedCorpus(t)
	for _, proof := range proofs {
		sc, found := scenarios[proof.scenario]
		if !found {
			t.Fatalf("the corpus carries no scenario named %q", proof.scenario)
		}
		if sc.Expect.Outcome != aitasks.OutcomeAbstained {
			t.Fatalf("%s expects %q, and this proof is only meaningful for an abstention scenario",
				proof.scenario, sc.Expect.Outcome)
		}
		t.Run(proof.scenario+"/"+proof.wantFabricated, func(t *testing.T) {
			correct := evaluateScenario(t, sc, proof.correct)
			if correct.Result != sc.Expect.Outcome {
				t.Errorf("the answer this scenario calls correct reached %q (%s), want %q",
					correct.Result, correct.Detail, sc.Expect.Outcome)
			}
			fabricated := evaluateScenario(t, sc, proof.fabricated)
			if fabricated.Result != proof.wantFabricated {
				t.Fatalf("the fabrication reached %q (%s), want %q",
					fabricated.Result, fabricated.Detail, proof.wantFabricated)
			}
			if !strings.Contains(fabricated.Detail, proof.wantDetail) {
				t.Errorf("the refusal does not name what was fabricated: %q", fabricated.Detail)
			}
		})
	}
}

// loadShippedCorpus reads the committed corpus through the census that certifies
// it, keyed by scenario name — the name a proof above refers to, so a renamed
// scenario fails loudly here instead of leaving a proof silently unrun.
func loadShippedCorpus(t *testing.T) map[string]aicert.Scenario {
	t.Helper()
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	loaded, err := aicert.LoadCorpus("corpus", census)
	if err != nil {
		t.Fatalf("LoadCorpus(corpus): %v", err)
	}
	byName := make(map[string]aicert.Scenario, len(loaded))
	for _, sc := range loaded {
		byName[sc.Name] = sc
	}
	return byName
}

// evaluateScenario drives one committed scenario's bound case over one canned
// reply and returns the outcome the site's own validator reached.
func evaluateScenario(t *testing.T, sc aicert.Scenario, reply string) aitasks.Outcome {
	t.Helper()
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	factory, bound := census.CaseFor(ai.Task(sc.Task), sc.Site)
	if !bound {
		t.Fatalf("site %s/%s binds no certification case", sc.Task, sc.Site)
	}
	prepared, err := factory.Prepare(json.RawMessage(sc.Fixture), json.RawMessage(sc.Expect.Answer))
	if err != nil {
		t.Fatalf("preparing %s: %v", sc.Name, err)
	}
	trace, err := prepared.Run(context.Background(), cannedCompleter{reply: reply})
	if err != nil {
		t.Fatalf("running %s: %v", sc.Name, err)
	}
	return prepared.Evaluate(trace)
}

// profileClaimJSON is one field the profile lane could return, citing this
// fixture's only passage.
func profileClaimJSON(field, value string) string {
	return `{"f":"` + field + `","v":"` + value + `","e":"` + abstentionPassageID + `","c":0.9}`
}

func profileReplyJSON(claims ...string) string {
	return `{"fields":[` + strings.Join(claims, ",") + `]}`
}

// offerDraftLineJSON is one drafted line in the shape the offer-draft envelope
// takes, carrying every field the gate requires so what refuses it is the
// citation and not the shape.
func offerDraftLineJSON(description, evidence, sourceID string) string {
	return `{"lines":[{"description":"` + description + `","quantity":"1","tax_rate":"19.00",` +
		`"evidence_snippet":"` + evidence + `","source_id":"` + sourceID + `"}]}`
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// A validated-but-unanswerable predicate answers with its note and NO rows.
// Rows returned while the predicate was quietly dropped would answer a wider
// question in a shape indistinguishable from the right one — and the row count
// would leak the size of an answer the caller cannot have.
func TestAnUnavailablePredicateAnswersWithNotesAndNoRows(t *testing.T) {
	ctx := readerFor(entityOrganization)
	plan := validatedPlanDoc(ctx, t, `{
		"version": "v1", "target": "organization",
		"where": [{"field": "address", "op": "within_radius",
		           "value": {"center": "Stuttgart", "radius_km": 50}}]}`)
	if len(plan.Unavailable) != 1 {
		t.Fatalf("the validator marked %d predicates unavailable", len(plan.Unavailable))
	}
	// No store and no schema reader: reaching either would mean the executor
	// ran a statement for a plan it had already been told it cannot answer.
	result, err := (&QueryExecutor{}).Execute(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("an unanswerable plan returned %d rows", len(result.Rows))
	}
	if result.Coverage != CoveragePartialDegraded {
		t.Errorf("coverage is %q", result.Coverage)
	}
	if len(result.Notes) != 1 || result.Notes[0].Code != CodeDistanceRankingUnavailable {
		t.Fatalf("notes are %v", result.Notes)
	}
	if result.Notes[0].Path != "where[0]" {
		t.Errorf("the note names %q rather than the predicate it is about", result.Notes[0].Path)
	}
	// The sentence says so too: a note in a field nobody reads is a note
	// nobody reads.
	if !strings.Contains(result.Narrative, "No rows are returned") {
		t.Errorf("narrative is %q", result.Narrative)
	}
}

// The vocabulary's nil pass-through WIDENS what may be asked. Executing a
// widened vocabulary against a table nobody checked is the one thing that
// pass-through must never reach.
func TestAnExecutorWithNoSchemaReaderRefusesToRun(t *testing.T) {
	ctx := readerFor(entityDeal)
	plan := validatedPlanDoc(ctx, t, `{"version": "v1", "target": "deal"}`)
	if _, err := (&QueryExecutor{}).Execute(ctx, plan); err == nil {
		t.Fatal("an executor with no schema reader ran a plan")
	}
}

// The exit criterion, as a unit: no ranked answer ever labels itself complete.
func TestCoverageNeverClaimsCompletenessForAnAnswerThatLacksIt(t *testing.T) {
	ranked := ValidatedPlan{Plan: Plan{SimilarTo: "manufacturers who churned"}}
	exact := ValidatedPlan{}
	for _, tc := range []struct {
		name      string
		plan      ValidatedPlan
		truncated bool
		degraded  bool
		want      string
	}{
		{"an exact answer that fits", exact, false, false, CoverageCompleteExact},
		{"an exact answer cut off at the page", exact, true, false, CoveragePartialDegraded},
		{"a ranked answer", ranked, false, false, CoverageRankedSemantic},
		{"a ranked answer with no embedding lane", ranked, false, true, CoveragePartialDegraded},
		{"a ranked answer cut off at the page", ranked, true, false, CoveragePartialDegraded},
		{"an exact answer with no embedding lane to degrade", exact, false, true, CoveragePartialDegraded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := coverageOf(tc.plan, answerShape{truncated: tc.truncated, degraded: tc.degraded}); got != tc.want {
				t.Errorf("coverage is %q, want %q", got, tc.want)
			}
		})
	}
}

// Membership is decided by the exact predicates; ORDER is the retriever's.
func TestARankedAnswerIsReturnedInTheRankingsOrder(t *testing.T) {
	first, second, third := ids.NewV7(), ids.NewV7(), ids.NewV7()
	rows := []QueryRow{{ID: third}, {ID: first}, {ID: second}}
	ranked := []Hit{{ID: first, Score: 0.9}, {ID: second, Score: 0.5}, {ID: third, Score: 0.1}}
	ordered := orderByRank(rows, ranked, 10)
	if ordered[0].ID != first || ordered[1].ID != second || ordered[2].ID != third {
		t.Fatalf("order is %v", ordered)
	}
	if ordered[0].Score != 0.9 {
		t.Errorf("the row carries score %v rather than the rank it came back with", ordered[0].Score)
	}
}

// An exact answer has no order to justify, so its rows keep the statement's.
func TestAnExactAnswerKeepsTheStatementsOrder(t *testing.T) {
	first, second := ids.NewV7(), ids.NewV7()
	rows := []QueryRow{{ID: second}, {ID: first}}
	ordered := orderByRank(rows, nil, 10)
	if ordered[0].ID != second {
		t.Fatalf("an exact answer was reordered: %v", ordered)
	}
	if ordered[0].Score != 0 {
		t.Errorf("an unranked row carries score %v", ordered[0].Score)
	}
}

func TestARankedAnswerIsCutToThePageAfterItIsOrdered(t *testing.T) {
	first, second := ids.NewV7(), ids.NewV7()
	ordered := orderByRank([]QueryRow{{ID: second}, {ID: first}},
		[]Hit{{ID: first, Score: 0.9}, {ID: second, Score: 0.2}}, 1)
	if len(ordered) != 1 || ordered[0].ID != first {
		t.Fatalf("the page kept %v; ordering must precede the cut, or the page is the wrong rows", ordered)
	}
}

// The hybrid arm degrades to the lexical lane SILENTLY. The executor asks the
// same question it asks, so it can say so: a degradation nobody is told about
// is indistinguishable from a working feature.
func TestAnUnboundEmbeddingLaneIsReportedRatherThanHidden(t *testing.T) {
	if embeddingLaneBound(nil) {
		t.Error("a nil embedder reports a bound lane")
	}
	if embeddingLaneBound(stubEmbedder{}) {
		t.Error("an embedder with no identity reports a bound lane")
	}
	if !embeddingLaneBound(stubEmbedder{identity: "openai:text-embedding-3-small:1536"}) {
		t.Error("a bound embedder reports an unbound lane")
	}
}

// stubEmbedder is the embedding seam: identity is what the query side reads to
// decide whether there is a lane to rank against at all.
type stubEmbedder struct {
	identity string
}

func (e stubEmbedder) EmbedIdentity() (string, int) { return e.identity, 1536 }

func (e stubEmbedder) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{}, nil
}

// A plan naming a record type that is not searchable is a wiring fault rather
// than a caller error — the validator settled the target against the same
// closed set — so it fails loudly instead of answering an empty page.
func TestAPlanBoundToAnUnsearchableRecordTypeFailsLoudly(t *testing.T) {
	executor := &QueryExecutor{columns: stubColumns{tables: planTables}}
	_, err := executor.bindPlan(readerFor(entityDeal), ValidatedPlan{Target: TargetVocabulary{Target: "workspace"}})
	if err == nil {
		t.Fatal("a plan bound to a record type this module does not search")
	}
}

// The hop's binding carries the hop's own columns, so a hop predicate is
// compiled against the table it filters rather than against the target's.
func TestTheHopIsBoundToItsOwnTablesColumns(t *testing.T) {
	ctx := readerFor(entityDeal, entityOrganization)
	plan := validatedPlanDoc(ctx, t, `{
		"version": "v1", "target": "deal",
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`)
	executor := &QueryExecutor{columns: stubColumns{tables: planTables}}
	binding, err := executor.bindPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if binding.hop == nil {
		t.Fatal("the plan takes a hop and the binding carries none")
	}
	if !binding.hop.columns.answers(newField("address.city", KindText)) {
		t.Error("the hop's columns do not answer the hop's own field")
	}
	if binding.hop.columns.answers(newField("amount_minor", KindNumber)) {
		t.Error("the hop's columns answer the TARGET's field; the hop is bound to the wrong table")
	}
}

// A schema read that fails must not become an empty schema: every field would
// then refuse as `unknown_field`, which reads exactly like a caller who
// mistyped one.
func TestASchemaReadThatFailsStopsTheExecution(t *testing.T) {
	executor := &QueryExecutor{columns: stubColumns{err: errFakeSchemaRead}}
	_, err := executor.bindPlan(readerFor(entityDeal), ValidatedPlan{Target: TargetVocabulary{Target: entityDeal}})
	if err == nil {
		t.Fatal("a failed schema read bound a plan")
	}
}

var errFakeSchemaRead = errSchemaRead{}

type errSchemaRead struct{}

func (errSchemaRead) Error() string { return "connection reset" }

// Whitespace is PRESENT to the grammar and empty to the retriever. Left alone
// it reaches the search store, whose own refusal names `q` — a field this plan
// does not have, sending a caller to look for something they never sent.
func TestABlankSimilarityClauseIsRefusedUnderItsOwnName(t *testing.T) {
	decoded, err := DecodePlan([]byte(`{"version": "v1", "target": "deal", "similar_to": "   "}`))
	if err != nil {
		t.Fatal(err)
	}
	validator := NewPlanValidator(NewVocabularyResolver().WithColumnReader(stubColumns{tables: planTables}))
	_, err = validator.Validate(readerFor(entityDeal), decoded)
	var refusal *PlanRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a blank similarity clause validated: %v", err)
	}
	faults := refusal.FieldFaults()
	if len(faults) != 1 || faults[0].Field != "similar_to" || faults[0].Code != CodeValueMissing {
		t.Fatalf("faults are %v", faults)
	}
}

// An ABSENT similarity clause is a different and perfectly good plan: rank
// nothing, answer exactly.
func TestAnAbsentSimilarityClauseIsNotARefusal(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal), t, `{"version": "v1", "target": "deal"}`)
	if plan.Plan.SimilarTo != "" {
		t.Fatalf("similarity clause is %q", plan.Plan.SimilarTo)
	}
}

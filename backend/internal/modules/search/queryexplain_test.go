// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"strings"
	"testing"
)

// The sentence describes what RAN. It is rendered from the validated plan, so
// a caller reading rows beside it is reading a description of the query those
// rows came from.
func TestTheNarrativeDescribesTheExecutedPlan(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal, entityOrganization), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "eq", "value": "open"},
		          {"field": "amount_minor", "op": "gte", "value": 100000}],
		"traverse": {"relation": "organization",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]},
		"similar_to": "manufacturers who churned after a pilot",
		"limit": 25}`)
	got := explainPlan(plan)
	want := `deal records where status is "open" and amount_minor is at least 100000, ` +
		`linked to an organization record where address.city is "Stuttgart", ` +
		`ranked by similarity to "manufacturers who churned after a pilot"; at most 25.`
	if got != want {
		t.Errorf("narrative is\n  %s\nwant\n  %s", got, want)
	}
}

// An exact answer says its order, because "newest first" is the only ordering
// a caller can reason about when nothing ranked the rows.
func TestAnExactPlanSaysItsOrder(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal", "limit": 10}`)
	if got := explainPlan(plan); got != "deal records; at most 10, newest first." {
		t.Errorf("narrative is %q", got)
	}
}

// Every operator reads as words. An operator rendered as its own machine name
// inside an English sentence reads as a bug in the answer.
func TestEveryOperatorHasAReading(t *testing.T) {
	for _, op := range []string{OpEq, OpNeq, OpIn, OpLt, OpLte, OpGt, OpGte, OpWithinRadius} {
		phrase := comparatorPhrase(op)
		if phrase == op {
			t.Errorf("%q reads as itself", op)
		}
		if !strings.HasPrefix(phrase, "is") {
			t.Errorf("%q reads as %q, which does not continue the sentence", op, phrase)
		}
	}
}

// A membership test reads its list, not its single-value member — the operand
// the operator actually used.
func TestAMembershipTestReadsItsList(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "status", "op": "in", "values": ["open", "won"]}]}`)
	if got := explainPlan(plan); !strings.Contains(got, `status is one of ["open","won"]`) {
		t.Errorf("narrative is %q", got)
	}
}

// The sentence carries the predicate that did NOT run, and says what that cost
// the answer. A note in a machine field nobody reads is a note nobody reads.
func TestTheNarrativeNamesThePredicateThatCouldNotRun(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityOrganization), t, `{
		"version": "v1", "target": "organization",
		"where": [{"field": "address", "op": "within_radius",
		           "value": {"center": "Stuttgart", "radius_km": 50}}]}`)
	got := explainPlan(plan)
	for _, want := range []string{"address is within", "where[0]", "No rows are returned"} {
		if !strings.Contains(got, want) {
			t.Errorf("narrative %q lacks %q", got, want)
		}
	}
}

// The operand is the caller's own text and already JSON, so it is compacted
// rather than re-encoded: a re-encoding would quietly normalise the value the
// sentence claims ran.
func TestTheOperandIsShownAsTheCallerWroteIt(t *testing.T) {
	plan := validatedPlanDoc(readerFor(entityDeal), t, `{
		"version": "v1", "target": "deal",
		"where": [{"field": "amount_minor", "op": "gt", "value":    100000   }]}`)
	if got := explainPlan(plan); !strings.Contains(got, "amount_minor is more than 100000") {
		t.Errorf("narrative is %q", got)
	}
}

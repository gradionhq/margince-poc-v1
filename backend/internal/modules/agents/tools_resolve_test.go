// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// An exact key hit is the one answer a caller may act on, and it says so.
func TestAnExactKeyHitAnswersMatched(t *testing.T) {
	person := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		person: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	result := handleResolve(t, provider, `{"candidates":[{"kind":"person","ref":"card-1","emails":["anna@acme.example"]}]}`,
		[]ResolveOutcome{{Verdict: ResolveVerdictExact, Refs: []ResolveRef{
			{Kind: "person", ID: person, Confidence: 1, MatchedOn: "email"},
		}}})

	answer := result.Candidates[0]
	if answer.Ref != "card-1" {
		t.Errorf("ref = %q, want the caller's own label echoed back", answer.Ref)
	}
	if answer.Decision != ResolveDecisionMatched {
		t.Errorf("decision = %q, want matched", answer.Decision)
	}
	if len(answer.Matches) != 1 || answer.Matches[0].Record.ID != person {
		t.Fatalf("matches = %+v, want the one record the key named", answer.Matches)
	}
	if answer.Matches[0].MatchedOn != "email" || answer.Matches[0].Confidence != 1 {
		t.Errorf("match = %+v, want the axis and certainty the ladder reported", answer.Matches[0])
	}
}

// A near match is NEVER `matched`, whatever it scored. Deciding that two records
// are the same person is a human's call, and a caller told "this is them" would
// write against a record nobody confirmed.
func TestANearMatchIsNeverPresentedAsAMatch(t *testing.T) {
	person := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		person: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	result := handleResolve(t, provider, `{"candidates":[{"kind":"person","name":"Anna Weber"}]}`,
		[]ResolveOutcome{{Verdict: ResolveVerdictAmbiguous, Refs: []ResolveRef{
			{Kind: "person", ID: person, Confidence: 0.99, MatchedOn: "full_name"},
		}}})

	if got := result.Candidates[0].Decision; got != ResolveDecisionAmbiguous {
		t.Errorf("decision = %q for a 0.99 near match, want ambiguous — the score is not the decision", got)
	}
}

// A candidate with no label answers without one, rather than with an empty
// string a caller might read as a label they chose.
func TestACandidateWithNoLabelCarriesNone(t *testing.T) {
	result := handleResolve(t, &queryProbeProvider{}, `{"candidates":[{"kind":"person","name":"Nobody"}]}`,
		[]ResolveOutcome{{Verdict: ResolveVerdictNone}})

	if result.Candidates[0].Ref != "" {
		t.Errorf("ref = %q, want none", result.Candidates[0].Ref)
	}
	if result.Candidates[0].Decision != ResolveDecisionUnresolved {
		t.Errorf("decision = %q, want unresolved", result.Candidates[0].Decision)
	}
	if result.Candidates[0].Matches == nil {
		t.Error("matches is null; an agent iterating a batch should not have to branch on it")
	}
}

// The sharpest rule here: a candidate whose ONLY match is outside the caller's
// visibility answers `unresolved` — the same word a genuine miss gets.
//
// A distinct decision would be a probe. Send one address at a time and a
// "resolved, but not for you" answer tells the caller that a record they are not
// allowed to know exists holds that address.
func TestAMatchTheCallerCannotSeeIsIndistinguishableFromNoMatch(t *testing.T) {
	hidden := ids.NewV7()
	provider := &queryProbeProvider{fail: map[ids.UUID]error{hidden: apperrors.ErrPermissionDenied}}
	withheld := handleResolve(t, provider, `{"candidates":[{"kind":"person","emails":["anna@acme.example"]}]}`,
		[]ResolveOutcome{{Verdict: ResolveVerdictExact, Refs: []ResolveRef{
			{Kind: "person", ID: hidden, Confidence: 1, MatchedOn: "email"},
		}}})
	genuine := handleResolve(t, &queryProbeProvider{}, `{"candidates":[{"kind":"person","emails":["nobody@acme.example"]}]}`,
		[]ResolveOutcome{{Verdict: ResolveVerdictNone}})

	if withheld.Candidates[0].Decision != ResolveDecisionUnresolved {
		t.Errorf("a withheld match answered %q, which distinguishes it from a real miss",
			withheld.Candidates[0].Decision)
	}
	if len(withheld.Candidates[0].Matches) != 0 {
		t.Errorf("a record the caller may not read was served anyway: %+v", withheld.Candidates[0].Matches)
	}
	if withheld.Candidates[0].Decision != genuine.Candidates[0].Decision {
		t.Errorf("withheld answers %q and a genuine miss answers %q — the pair is a probe",
			withheld.Candidates[0].Decision, genuine.Candidates[0].Decision)
	}
}

// Ambiguity is NOT collapsed by row scope. Answering `matched` because the rival
// happens to be hidden would settle a disagreement using the caller's own
// blindness — telling them "this is definitely them" precisely because the
// record that contradicts it is out of reach.
func TestRowScopeDoesNotCollapseAnAmbiguousAnswer(t *testing.T) {
	visible, hidden := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{
		records: map[ids.UUID]datasource.Record{
			visible: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		},
		fail: map[ids.UUID]error{hidden: apperrors.ErrNotFound},
	}
	result := handleResolve(t, provider, `{"candidates":[{"kind":"person","emails":["anna@acme.example"]}]}`,
		[]ResolveOutcome{{Verdict: ResolveVerdictAmbiguous, Refs: []ResolveRef{
			{Kind: "person", ID: visible, Confidence: 1, MatchedOn: "email"},
			{Kind: "person", ID: hidden, Confidence: 1, MatchedOn: "phone"},
		}}})

	if got := result.Candidates[0].Decision; got != ResolveDecisionAmbiguous {
		t.Errorf("decision = %q, want ambiguous — one survivor of a disagreement is not a resolution", got)
	}
	if len(result.Candidates[0].Matches) != 1 {
		t.Errorf("matches = %+v, want only the record the caller may read", result.Candidates[0].Matches)
	}
}

// The narrowing is reported ONCE for the call, with no count and no candidate
// named. Per candidate it would restore the probe the drop exists to prevent.
func TestTheRowScopeWarningNamesNoCandidateAndNoCount(t *testing.T) {
	first, second := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{fail: map[ids.UUID]error{
		first: apperrors.ErrPermissionDenied, second: apperrors.ErrPermissionDenied,
	}}
	tool := resolveEntities{p: provider, resolve: fixedResolver([]ResolveOutcome{
		{Verdict: ResolveVerdictExact, Refs: []ResolveRef{{Kind: "person", ID: first, MatchedOn: "email"}}},
		{Verdict: ResolveVerdictExact, Refs: []ResolveRef{{Kind: "person", ID: second, MatchedOn: "phone"}}},
	})}
	registry, _, ctx := chargingRegistry(t, tool)

	raw, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(
		`{"candidates":[{"kind":"person","ref":"a"},{"kind":"person","ref":"b"}]}`))
	if err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}
	var envelope struct {
		Warnings []Warning `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}

	seen := 0
	for _, w := range envelope.Warnings {
		if w.Code != CodeResolutionRowScoped {
			continue
		}
		seen++
		if strings.ContainsAny(w.Message, "0123456789") {
			t.Errorf("the warning carries a number, which sizes the hidden set: %q", w.Message)
		}
		if strings.Contains(w.Message, `"a"`) || strings.Contains(w.Message, `"b"`) {
			t.Errorf("the warning names a candidate, which turns a batch into a probe: %q", w.Message)
		}
	}
	if seen != 1 {
		t.Errorf("the narrowing was reported %d times, want once for the whole call", seen)
	}
}

// Resolution is a bulk read and is charged like one: a batch naming four records
// spends four, not one.
func TestResolutionIsChargedPerRecordServed(t *testing.T) {
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{}}
	var outcomes []ResolveOutcome
	for range 4 {
		id := ids.NewV7()
		provider.records[id] = recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true)
		outcomes = append(outcomes, ResolveOutcome{Verdict: ResolveVerdictExact, Refs: []ResolveRef{
			{Kind: "person", ID: id, Confidence: 1, MatchedOn: "email"},
		}})
	}
	tool := resolveEntities{p: provider, resolve: fixedResolver(outcomes)}
	registry, charger, ctx := chargingRegistry(t, tool)

	if _, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(
		`{"candidates":[{"kind":"person"},{"kind":"person"},{"kind":"person"},{"kind":"person"}]}`)); err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}

	if charger.charged != 4 {
		t.Errorf("charged %d for four resolved records, want 4", charger.charged)
	}
}

// An empty batch and an oversized one are both named as the argument that is
// wrong, before the resolver runs.
func TestTheCandidateBatchIsBounded(t *testing.T) {
	oversized := `{"candidates":[` + strings.Repeat(`{"kind":"person"},`, resolveMaxCandidates) + `{"kind":"person"}]}`
	for name, args := range map[string]string{
		"empty":     `{"candidates":[]}`,
		"oversized": oversized,
	} {
		t.Run(name, func(t *testing.T) {
			tool := resolveEntities{p: &queryProbeProvider{}, resolve: unreachedResolver(t)}
			_, err := tool.Handle(t.Context(), json.RawMessage(args))
			if err == nil {
				t.Fatal("the batch was accepted")
			}
			if !strings.Contains(err.Error(), "candidates") {
				t.Errorf("the refusal never names `candidates`: %v", err)
			}
		})
	}
}

// A resolver answering a different number of outcomes than it was asked about
// would misalign every label after the gap — a caller acting on the wrong
// candidate's answer, silently. It is a defect in the seam, and it fails.
func TestAMisalignedResolverAnswerFailsRatherThanShifting(t *testing.T) {
	tool := resolveEntities{p: &queryProbeProvider{}, resolve: fixedResolver([]ResolveOutcome{{Verdict: ResolveVerdictNone}})}

	_, err := tool.Handle(t.Context(), json.RawMessage(
		`{"candidates":[{"kind":"person","ref":"a"},{"kind":"person","ref":"b"}]}`))
	if err == nil {
		t.Fatal("an answer covering one of two candidates was accepted, so every later label shifted")
	}
}

// A store fault is not `unresolved`. Answering the safest-sounding decision
// because nothing could be read is how this tool would cause the duplicate it
// exists to prevent.
func TestAnUnreachableStoreDoesNotReadAsNoMatch(t *testing.T) {
	id := ids.NewV7()
	provider := &queryProbeProvider{fail: map[ids.UUID]error{id: errors.New("the pool is exhausted")}}
	tool := resolveEntities{p: provider, resolve: fixedResolver([]ResolveOutcome{
		{Verdict: ResolveVerdictExact, Refs: []ResolveRef{{Kind: "person", ID: id, MatchedOn: "email"}}},
	})}

	if _, err := tool.Handle(t.Context(), json.RawMessage(`{"candidates":[{"kind":"person"}]}`)); err == nil {
		t.Fatal("an unreachable store answered `unresolved`, which tells the caller creating is safe")
	}
}

// An installation with no resolver serves no tool, rather than one that refuses
// every call.
func TestNoResolverRegistersNoTool(t *testing.T) {
	r := NewRegistry(nil, nil)
	RegisterResolveTool(r, &queryProbeProvider{}, nil)

	for _, spec := range r.Specs() {
		if spec.Name == "resolve_entities" {
			t.Fatal("resolve_entities was registered over an absent resolver")
		}
	}
}

// --- helpers ---

func handleResolve(t *testing.T, provider datasource.SystemOfRecordProvider, args string, outcomes []ResolveOutcome) ResolveEntitiesResult {
	t.Helper()
	tool := resolveEntities{p: provider, resolve: fixedResolver(outcomes)}
	raw, err := tool.Handle(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("handling the batch: %v", err)
	}
	var result ResolveEntitiesResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("the result is not the shape this tool declares: %v", err)
	}
	if len(result.Candidates) != len(outcomes) {
		t.Fatalf("got %d answers for %d candidates", len(result.Candidates), len(outcomes))
	}
	return result
}

// fixedResolver answers one prepared batch, whatever it is asked.
func fixedResolver(outcomes []ResolveOutcome) EntityResolver {
	return func(context.Context, []ResolveCandidate) ([]ResolveOutcome, error) {
		return outcomes, nil
	}
}

// unreachedResolver fails the test if a refused call reaches the seam anyway.
func unreachedResolver(t *testing.T) EntityResolver {
	return func(context.Context, []ResolveCandidate) ([]ResolveOutcome, error) {
		t.Error("the resolver was reached by a call that should have been refused")
		return nil, nil
	}
}

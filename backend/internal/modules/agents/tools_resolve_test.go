// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		[]ResolveOutcome{{Refs: []ResolveRef{
			{Kind: "person", ID: person, Exact: true, Confidence: 1, MatchedOn: "email"},
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
		[]ResolveOutcome{{Refs: []ResolveRef{
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
		[]ResolveOutcome{{}})

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

// THE SHARPEST RULE HERE, and it is asserted on the WHOLE ANSWER rather than on
// the decision word.
//
// A candidate whose only match is outside the caller's visibility must be
// indistinguishable from one that names nothing at all — and "indistinguishable"
// is a claim about everything the caller can observe, not about one field.
// Comparing only `decision` is how the conditional row-scope warning survived a
// review: both answers said `unresolved`, and the envelope beside them said
// which was which.
//
// So this compares the two sealed results byte for byte, with the trace id
// blanked because it is unique per call by construction and carries nothing
// about the answer.
func TestAWithheldMatchAndAGenuineMissAreByteIdentical(t *testing.T) {
	hidden := ids.NewV7()
	args := `{"candidates":[{"kind":"person","ref":"probe","emails":["anna@acme.example"]}]}`

	withheld := sealedResolve(t, resolveEntities{
		p:       &queryProbeProvider{fail: map[ids.UUID]error{hidden: apperrors.ErrPermissionDenied}},
		resolve: fixedResolver([]ResolveOutcome{{Refs: []ResolveRef{{Kind: "person", ID: hidden, Exact: true, Confidence: 1, MatchedOn: "email"}}}}),
	}, args)
	genuine := sealedResolve(t, resolveEntities{
		p:       &queryProbeProvider{},
		resolve: fixedResolver([]ResolveOutcome{{}}),
	}, args)

	if withheld != genuine {
		t.Errorf("a withheld match and a genuine miss are told apart by the answer itself:\n withheld = %s\n  genuine = %s",
			withheld, genuine)
	}
}

// THE DECISION WORD IS COMPUTED FROM THE VISIBLE SET ALONE, and this is the
// test that pins it against the oracle it closes.
//
// A candidate can mix a key the caller can read with a key they are guessing. If
// the ladder's own verdict reached the decision, the guessed key would answer
// `ambiguous` when it belongs to a hidden record and `matched` when it belongs
// to nobody — with an identical single visible match either way, so the WORD
// answers a question about a record they may not read, one probe at a time.
func TestAHiddenRivalCannotChangeTheDecisionWord(t *testing.T) {
	visible, hidden := ids.NewV7(), ids.NewV7()
	records := map[ids.UUID]datasource.Record{
		visible: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}
	args := `{"candidates":[{"kind":"person","emails":["anna@acme.example"],"phones":["+4915112345678"]}]}`
	visibleRef := ResolveRef{Kind: "person", ID: visible, Exact: true, Confidence: 1, MatchedOn: "email"}

	// The guessed phone belongs to a record outside the caller's reach: the
	// ladder names both, and the rival is dropped at hydration.
	withRival := handleResolve(t,
		&queryProbeProvider{records: records, fail: map[ids.UUID]error{hidden: apperrors.ErrPermissionDenied}},
		args, []ResolveOutcome{{Refs: []ResolveRef{
			visibleRef,
			{Kind: "person", ID: hidden, Exact: true, Confidence: 1, MatchedOn: "phone"},
		}}})
	// The guessed phone belongs to nobody.
	withoutRival := handleResolve(t, &queryProbeProvider{records: records}, args,
		[]ResolveOutcome{{Refs: []ResolveRef{visibleRef}}})

	if withRival.Candidates[0].Decision != withoutRival.Candidates[0].Decision {
		t.Fatalf("a hidden rival answered %q where no rival answers %q — the decision word is an oracle",
			withRival.Candidates[0].Decision, withoutRival.Candidates[0].Decision)
	}
	if withRival.Candidates[0].Decision != ResolveDecisionMatched {
		t.Errorf("decision = %q, want matched: one exact key, one visible record", withRival.Candidates[0].Decision)
	}
	if len(withRival.Candidates[0].Matches) != 1 {
		t.Errorf("matches = %+v, want only the record the caller may read", withRival.Candidates[0].Matches)
	}
}

// Two rivals the caller CAN see are ambiguous, so the collapse above is a
// visibility rule and not a blanket "one match wins".
func TestTwoVisibleRivalsStayAmbiguous(t *testing.T) {
	first, second := ids.NewV7(), ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		first:  recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
		second: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	result := handleResolve(t, provider, `{"candidates":[{"kind":"person","emails":["anna@acme.example"]}]}`,
		[]ResolveOutcome{{Refs: []ResolveRef{
			{Kind: "person", ID: first, Exact: true, Confidence: 1, MatchedOn: "email"},
			{Kind: "person", ID: second, Exact: true, Confidence: 1, MatchedOn: "phone"},
		}}})

	if got := result.Candidates[0].Decision; got != ResolveDecisionAmbiguous {
		t.Errorf("decision = %q, want ambiguous — two keys named two people the caller can see", got)
	}
}

// THE CAVEAT IS UNCONDITIONAL, and this test is the reason it has to be.
//
// A caveat raised only when something was withheld is a signal that something
// was withheld — and a batch of ONE candidate turns that call-level signal into
// a per-address one. So the envelope must be indistinguishable between a probe
// that hit a hidden record and one that hit nothing at all.
func TestTheVisibilityCaveatIsRaisedWhetherOrNotAnythingWasWithheld(t *testing.T) {
	hidden := ids.NewV7()
	args := `{"candidates":[{"kind":"person","ref":"a"}]}`

	withheld := warningCodes(t, resolveEntities{
		p:       &queryProbeProvider{fail: map[ids.UUID]error{hidden: apperrors.ErrPermissionDenied}},
		resolve: fixedResolver([]ResolveOutcome{{Refs: []ResolveRef{{Kind: "person", ID: hidden, Exact: true}}}}),
	}, args)
	absent := warningCodes(t, resolveEntities{
		p:       &queryProbeProvider{},
		resolve: fixedResolver([]ResolveOutcome{{}}),
	}, args)

	if !withheld[CodeResolutionBoundedByVisibility] || !absent[CodeResolutionBoundedByVisibility] {
		t.Fatalf("the caveat is conditional: withheld=%v absent=%v", withheld, absent)
	}
	if len(withheld) != len(absent) {
		t.Errorf("a withheld match carries %d warnings and a genuine miss %d — the difference is the oracle",
			len(withheld), len(absent))
	}
}

// The caveat never sizes what it is about, and never names a candidate.
func TestTheVisibilityCaveatSizesNothing(t *testing.T) {
	tool := resolveEntities{p: &queryProbeProvider{}, resolve: fixedResolver([]ResolveOutcome{{}})}
	registry, _, ctx := chargingRegistry(t, tool)

	raw, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(`{"candidates":[{"kind":"person","ref":"anna"}]}`))
	if err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}
	var envelope struct {
		Warnings []Warning `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	for _, w := range envelope.Warnings {
		if w.Code != CodeResolutionBoundedByVisibility {
			continue
		}
		if strings.ContainsAny(w.Message, "0123456789") {
			t.Errorf("the caveat carries a number: %q", w.Message)
		}
		if strings.Contains(w.Message, "anna") {
			t.Errorf("the caveat names a candidate: %q", w.Message)
		}
	}
}

// A candidate answered with no record still costs a read. Left free,
// `unresolved` is an unlimited lookup: ask about every address in turn and the
// bound never moves, so the meter that is meant to bound probing sees exactly
// the calls that are pure probing as costing nothing.
func TestAnUnansweredCandidateIsStillCharged(t *testing.T) {
	tool := resolveEntities{p: &queryProbeProvider{}, resolve: fixedResolver([]ResolveOutcome{{}, {}})}
	registry, charger, ctx := chargingRegistry(t, tool)

	if _, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(
		`{"candidates":[{"kind":"person"},{"kind":"person"}]}`,
	)); err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}

	if charger.reads() != 2 {
		t.Errorf("charged %d for two unanswered candidates, want 2", charger.reads())
	}
}

// A kind outside the pair this tool answers is the CALLER's mistake and reads as
// one. Unchecked it reaches the resolver's own switch and comes back as an
// internal fault carrying the owning module's name.
func TestAnUnknownKindIsRefusedAsAnArgument(t *testing.T) {
	tool := resolveEntities{p: &queryProbeProvider{}, resolve: unreachedResolver(t)}

	for _, args := range []string{
		`{"candidates":[{"kind":"lead","name":"Anna"}]}`,
		`{"candidates":[{"name":"Anna"}]}`,
	} {
		var bad *BadArgsError
		_, err := tool.Handle(t.Context(), json.RawMessage(args))
		if !errors.As(err, &bad) {
			t.Errorf("%s answered %v, want an argument refusal", args, err)
		}
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
		outcomes = append(outcomes, ResolveOutcome{Refs: []ResolveRef{
			{Kind: "person", ID: id, Exact: true, Confidence: 1, MatchedOn: "email"},
		}})
	}
	tool := resolveEntities{p: provider, resolve: fixedResolver(outcomes)}
	registry, charger, ctx := chargingRegistry(t, tool)

	if _, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(
		`{"candidates":[{"kind":"person"},{"kind":"person"},{"kind":"person"},{"kind":"person"}]}`,
	)); err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}

	if charger.reads() != 4 {
		t.Errorf("charged %d for four resolved records, want 4", charger.reads())
	}
}

// One record named by two candidates is charged ONCE. A card carrying two
// addresses, or a name and a phone number belonging to the same person, is the
// ordinary case — and the bound counts records handed over, not answers.
func TestARecordNamedByTwoCandidatesIsChargedOnce(t *testing.T) {
	person := ids.NewV7()
	provider := &queryProbeProvider{records: map[ids.UUID]datasource.Record{
		person: recordAt(datasource.EntityPerson, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), true),
	}}
	shared := ResolveOutcome{Refs: []ResolveRef{
		{Kind: "person", ID: person, Exact: true, Confidence: 1, MatchedOn: "email"},
	}}
	tool := resolveEntities{p: provider, resolve: fixedResolver([]ResolveOutcome{shared, shared})}
	registry, charger, ctx := chargingRegistry(t, tool)

	raw, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(
		`{"candidates":[{"kind":"person","ref":"a"},{"kind":"person","ref":"b"}]}`,
	))
	if err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}

	if charger.reads() != 1 {
		t.Errorf("charged %d for one record named by two candidates, want 1", charger.reads())
	}
	// Both answers still carry it: charging once must not cost the second
	// candidate its match.
	var sealed struct {
		Data ResolveEntitiesResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &sealed); err != nil {
		t.Fatalf("reading the result: %v", err)
	}
	for i, answer := range sealed.Data.Candidates {
		if len(answer.Matches) != 1 || answer.Matches[0].Record.ID != person {
			t.Errorf("candidate %d = %+v, want the shared record", i, answer)
		}
	}
}

// The key lists are bounded, and the refusal names the field that is over.
//
// The read asks the exact lanes ONE KEY AT A TIME, which is what stops two
// addresses collapsing into one answer — and is exactly what makes an unbounded
// list a multiplier: twenty candidates of a thousand addresses each would be
// twenty thousand sequential lookups in one transaction, from a passport holding
// nothing but `read`.
func TestTheKeysOnOneCandidateAreBounded(t *testing.T) {
	for _, field := range []string{"emails", "phones", "domains"} {
		t.Run(field, func(t *testing.T) {
			keys := make([]string, resolveMaxKeysPerCandidate+1)
			for i := range keys {
				keys[i] = fmt.Sprintf(`"k%d@acme.example"`, i)
			}
			args := fmt.Sprintf(`{"candidates":[{"kind":"person","%s":[%s]}]}`, field, strings.Join(keys, ","))
			tool := resolveEntities{p: &queryProbeProvider{}, resolve: unreachedResolver(t)}

			var bad *BadArgsError
			_, err := tool.Handle(t.Context(), json.RawMessage(args))
			if !errors.As(err, &bad) {
				t.Fatalf("an oversized `%s` answered %v, want an argument refusal", field, err)
			}
			if !strings.Contains(err.Error(), field) {
				t.Errorf("the refusal never names `%s`: %v", field, err)
			}
		})
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
	tool := resolveEntities{p: &queryProbeProvider{}, resolve: fixedResolver([]ResolveOutcome{{}})}

	_, err := tool.Handle(t.Context(), json.RawMessage(
		`{"candidates":[{"kind":"person","ref":"a"},{"kind":"person","ref":"b"}]}`,
	))
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
		{Refs: []ResolveRef{{Kind: "person", ID: id, Exact: true, MatchedOn: "email"}}},
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

// warningCodes invokes a tool and returns the set of warning codes its envelope
// carried, which is what a caller can actually observe about a call.
func warningCodes(t *testing.T, tool resolveEntities, args string) map[string]bool {
	t.Helper()
	registry, _, ctx := chargingRegistry(t, tool)
	raw, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(args))
	if err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}
	var envelope struct {
		Warnings []Warning `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	codes := map[string]bool{}
	for _, w := range envelope.Warnings {
		codes[w.Code] = true
	}
	return codes
}

// sealedResolve invokes the tool and returns its whole sealed result with the
// per-call trace id blanked, so two calls can be compared as answers.
func sealedResolve(t *testing.T, tool resolveEntities, args string) string {
	t.Helper()
	registry, _, ctx := chargingRegistry(t, tool)
	raw, err := registry.Invoke(ctx, "resolve_entities", json.RawMessage(args))
	if err != nil {
		t.Fatalf("invoking resolve_entities: %v", err)
	}
	var sealed map[string]any
	if err := json.Unmarshal(raw, &sealed); err != nil {
		t.Fatalf("the result is not an envelope: %v (%s)", err, raw)
	}
	delete(sealed, "trace_id")
	normalized, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("re-encoding the result: %v", err)
	}
	return string(normalized)
}

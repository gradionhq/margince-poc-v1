// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// readsOnlyEntry is a catalog entry in the shape the scope model forces one on
// the real catalog: this goal reads and nothing else, while the passport behind
// it still admits update_record and send_email — the surface's write and send
// verbs — because scopes are granted in blocks and an entry is the only place
// that can say "not those".
var readsOnlyEntry = []string{"read_record"}

// The refusal has to happen at INVOCATION and not only in the prompt, because
// the prompt is not a boundary. Registry.Offered says so itself — "this narrows
// what is advertised and enforces nothing" — and Registry.Invoke admits against
// the passport, which has never heard of a catalog entry. A model that names a
// verb the entry omits is inside its passport's authority and outside its own.
func TestAToolOutsideTheAgentsEntryIsRefusedBeforeItIsInvoked(t *testing.T) {
	surface := &fakeSurface{results: map[string]json.RawMessage{
		"update_record": json.RawMessage(`{"updated":true}`),
	}}
	brain := &scriptedBrain{texts: []string{
		`{"tool":"update_record","args":{"record_id":"x"}}`,
		`{"final":{"summary":"gave up on the write"}}`,
	}}
	job := Job{Goal: "note the risk", Tools: readsOnlyEntry}

	res, err := New(surface, brain).Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	// The surface holds a working update_record. Reaching it at all is the
	// defect, so the call log is the assertion — not the outcome.
	if len(surface.calls) != 0 {
		t.Fatalf("a tool outside the agent's entry reached the governed surface: %+v", surface.calls)
	}
	if len(res.Steps) == 0 || res.Steps[0].Admission != "refused" {
		t.Fatalf("the refused step must be recorded as refused: %+v", res.Steps)
	}
	if !strings.Contains(res.Steps[0].Observation, "do not call it again in this run") {
		t.Fatalf("an allowlist refusal is permanent for the run and must say so: %q", res.Steps[0].Observation)
	}
}

// An approved 🟡 call redeems BEFORE the resumed loop takes a step, so the
// loop's own gate never sees it. A tool dropped from the entry while the run was
// parked would otherwise execute on the strength of an approval granted when the
// entry still named it — the human authorised an action, not an authority that
// outlives the entry.
func TestAnApprovedRedemptionIsRefusedWhenTheEntryNoLongerNamesTheTool(t *testing.T) {
	surface := &fakeSurface{results: map[string]json.RawMessage{
		"send_email": json.RawMessage(`{"sent":true}`),
	}}
	brain := &scriptedBrain{texts: []string{`{"final":{"summary":"could not send"}}`}}
	pending := Pending{
		TranscriptVersion: neutralisedObservations,
		ApprovalID:        ids.New[ids.ApprovalKind](), Tool: "send_email",
		Args:      json.RawMessage(`{"to":"a@b.c"}`),
		Window:    []model.Message{{Role: "user", Content: "Goal: follow up"}},
		Fence:     promptfence.New(),
		StepsUsed: 3, OutputTokens: 100,
	}
	job := Job{Goal: "follow up", Tools: readsOnlyEntry}

	res, err := New(surface, brain).Resume(context.Background(), job,
		Decision{Pending: pending, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(surface.calls) != 0 {
		t.Fatalf("an approved call outside the agent's entry was still redeemed: %+v", surface.calls)
	}
	if len(res.Steps) == 0 || res.Steps[0].Admission != "refused" {
		t.Fatalf("an unredeemed approval must record admission %q: %+v", "refused", res.Steps)
	}
}

// The same approval, the same surface, with the tool INSIDE the entry — so the
// test above is known to be measuring the allowlist and not a redemption that
// was broken all along.
func TestAnApprovedRedemptionStillRunsWhenTheEntryNamesTheTool(t *testing.T) {
	surface := &fakeSurface{results: map[string]json.RawMessage{
		"send_email": json.RawMessage(`{"sent":true}`),
	}}
	brain := &scriptedBrain{texts: []string{`{"final":{"summary":"sent"}}`}}
	pending := Pending{
		TranscriptVersion: neutralisedObservations,
		ApprovalID:        ids.New[ids.ApprovalKind](), Tool: "send_email",
		Args:   json.RawMessage(`{"to":"a@b.c"}`),
		Window: []model.Message{{Role: "user", Content: "Goal: follow up"}},
		Fence:  promptfence.New(),
	}
	job := Job{Goal: "follow up", Tools: []string{"read_record", "send_email"}}

	res, err := New(surface, brain).Resume(context.Background(), job,
		Decision{Pending: pending, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(surface.calls) != 1 {
		t.Fatalf("an approved call the entry names must redeem: %+v", surface.calls)
	}
	if res.Steps[0].Admission != "executed" {
		t.Fatalf("a redeemed approval must record admission %q: %+v", "executed", res.Steps)
	}
}

// The window lists the INTERSECTION, and the tools it drops are the ones the
// model would otherwise be choosing among for all forty steps.
func TestTheWindowListsOnlyWhatTheEntryAndThePassportBothAdmit(t *testing.T) {
	surface := &fakeSurface{results: map[string]json.RawMessage{
		"read_record": json.RawMessage(`{"record_type":"deal"}`),
	}}
	brain := &scriptedBrain{texts: []string{`{"final":{"summary":"done"}}`}}
	job := Job{Goal: "note the risk", Tools: []string{"read_record"}}

	if _, err := New(surface, brain).Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(brain.requests) == 0 {
		t.Fatal("no request reached the model")
	}
	system := brain.requests[0].System
	if !strings.Contains(system, "read_record") {
		t.Fatalf("the entry's own tool is missing from the listing: %q", system)
	}
	for _, dropped := range []string{"update_record", "send_email"} {
		if strings.Contains(system, dropped) {
			t.Errorf("%q is outside the agent's entry and must not be listed: %q", dropped, system)
		}
	}
}

// An empty Job.Tools means NO narrowing — the certification lane and every
// non-catalog caller ride this door, so it is asserted rather than assumed.
func TestAJobWithNoEntryIsNarrowedByThePassportAlone(t *testing.T) {
	surface := &fakeSurface{results: map[string]json.RawMessage{
		"update_record": json.RawMessage(`{"updated":true}`),
	}}
	brain := &scriptedBrain{texts: []string{
		`{"tool":"update_record","args":{"record_id":"x"}}`,
		`{"final":{"summary":"wrote it"}}`,
	}}

	if _, err := New(surface, brain).Run(context.Background(), Job{Goal: "write"}); err != nil {
		t.Fatal(err)
	}
	if len(surface.calls) != 1 || surface.calls[0].Tool != "update_record" {
		t.Fatalf("a job with no entry must reach whatever the passport admits: %+v", surface.calls)
	}
}

// A shortfall between the entry and the passport fails the run, naming the
// missing grant — and it fails BEFORE any completion, so a misconfigured agent
// costs nothing to discover.
//
// The partial case is the one that matters: a sweep holding every read tool and
// not its one write verb finds every at-risk deal, logs none of them, and
// reports a quiet night.
func TestAnEntryThePassportCannotFundFailsTheRunBeforeAnyModelSpend(t *testing.T) {
	surface := &fakeSurface{}
	// A read-only passport: update_record is a write and is not admitted.
	surface.offered = surface.scopedTo("read")
	brain := &scriptedBrain{texts: []string{`{"final":{"summary":"should never be asked"}}`}}

	res, err := New(surface, brain).Run(context.Background(),
		Job{Goal: "note the risk", Tools: []string{"read_record", "update_record"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDegraded {
		t.Fatalf("an unfundable entry must degrade loudly, got %q", res.Outcome)
	}
	if !strings.Contains(res.DegradeReason, "update_record") {
		t.Fatalf("the reason must name the missing tool: %q", res.DegradeReason)
	}
	if len(brain.requests) != 0 {
		t.Fatalf("a misconfigured agent must not spend a model call: %d requests", len(brain.requests))
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

// A decision that already exists must not park the run.
//
// A suspended run is woken by the approval.decided event, and for an approval a
// human answered BEFORE this step ran that event has already fired — so parking
// on its id waits for something that will never happen again. The gate now says
// which of the two answers it gave (workflow.StagedApprovalError.AlreadyApproved),
// and this is the run acting on it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// releasedSurface refuses a call that presents no approval_id — reporting that
// one is already approved — and serves the call that presents it. That is the
// governed surface's own behaviour for a call whose decision exists, so the run
// is judged against it rather than against a fixed per-tool answer.
type releasedSurface struct {
	*fakeSurface
	approvalID  ids.ApprovalID
	presented   []string
	retryFails  bool
	releasedOut json.RawMessage
}

func (s *releasedSurface) Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "send_email" {
		return s.fakeSurface.Invoke(ctx, name, args)
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil, err
	}
	presented, _ := m["approval_id"].(string)
	s.presented = append(s.presented, presented)
	if presented == "" {
		return nil, &workflow.StagedApprovalError{ApprovalID: s.approvalID, AlreadyApproved: true}
	}
	if presented != s.approvalID.String() {
		return nil, fmt.Errorf("approval was staged by a different passport: %w", apperrors.ErrApprovalTokenInvalid)
	}
	if s.retryFails {
		return nil, fmt.Errorf("target changed since approval: %w", apperrors.ErrVersionSkew)
	}
	return s.releasedOut, nil
}

func releasedRun(t *testing.T, retryFails bool) (*releasedSurface, Result) {
	t.Helper()
	surface := &releasedSurface{
		fakeSurface: &fakeSurface{results: map[string]json.RawMessage{}},
		approvalID:  ids.New[ids.ApprovalKind](),
		retryFails:  retryFails,
		releasedOut: json.RawMessage(`{"status":"sent"}`),
	}
	out, err := New(surface, &scriptedBrain{texts: []string{
		`{"tool":"send_email","args":{"to":"a@b.c"}}`,
		`{"final":{"summary":"done"}}`,
	}}).Run(context.Background(), Job{Goal: "follow up", Tools: []string{"send_email"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return surface, out
}

// The run spends the decision in place and carries on, instead of suspending on
// an id nothing will resume.
func TestARunSpendsAnApprovalThatWasAlreadyGrantedInsteadOfSuspending(t *testing.T) {
	surface, out := releasedRun(t, false)
	if out.Pending != nil {
		t.Fatalf("the run suspended on an approval already granted: %+v", out.Pending)
	}
	if out.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want the run to finish", out.Outcome)
	}
	// Presented on the retry and not before: the first call is the one the gate
	// answers with the id, and the second is the one that spends it.
	want := []string{"", surface.approvalID.String()}
	if len(surface.presented) != 2 || surface.presented[0] != want[0] || surface.presented[1] != want[1] {
		t.Fatalf("approval_id presented as %q, want %q", surface.presented, want)
	}
	var executed bool
	for _, step := range out.Steps {
		if step.Tool == "send_email" && step.Admission == "executed" {
			executed = true
		}
	}
	if !executed {
		t.Fatalf("no executed send_email step in %+v — the released call never landed", out.Steps)
	}
}

// If the release does not hold after all — the decision lapsed, the target moved
// — that is a refusal the model can re-plan around. Parking would be worse: the
// id is spent or dead, so no decision event can ever wake the run.
func TestARunTreatsAFailedReleaseAsARefusalRatherThanASuspension(t *testing.T) {
	_, out := releasedRun(t, true)
	if out.Pending != nil {
		t.Fatalf("the run suspended after its release failed: %+v", out.Pending)
	}
	var refused bool
	for _, step := range out.Steps {
		if step.Tool == "send_email" && strings.Contains(step.Observation, "target changed") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("the failed release was not fed back as an observation: %+v", out.Steps)
	}
}

// The allowlist still binds. An approval authorizes an ACTION, never a verb this
// run's catalog entry does not name — the same posture Resume takes.
func TestASpentApprovalCannotReachAVerbTheRunIsNotAllowed(t *testing.T) {
	surface := &releasedSurface{
		fakeSurface: &fakeSurface{results: map[string]json.RawMessage{}},
		approvalID:  ids.New[ids.ApprovalKind](),
		releasedOut: json.RawMessage(`{"status":"sent"}`),
	}
	out, err := New(surface, &scriptedBrain{texts: []string{
		`{"tool":"send_email","args":{"to":"a@b.c"}}`,
		`{"final":{"summary":"refused"}}`,
	}}).Run(context.Background(), Job{Goal: "follow up", Tools: []string{"read_record"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Pending != nil {
		t.Fatalf("a call outside the allowlist suspended: %+v", out.Pending)
	}
	if len(surface.presented) != 0 {
		t.Fatalf("the surface was reached %d times for a verb outside the allowlist", len(surface.presented))
	}
}

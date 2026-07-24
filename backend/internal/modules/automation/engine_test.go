// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fakeApprovals is a DB-free stand-in for the Approvals seam (seams.go):
// it records every StageRequest it receives and returns a fixed id (or a
// fixed error), so a test can assert ApplyActions actually calls Stage
// instead of dead-ending on a bare sentinel.
type fakeApprovals struct {
	id    ids.ApprovalID
	err   error
	calls []StageRequest
}

func (f *fakeApprovals) Stage(_ context.Context, in StageRequest) (ids.ApprovalID, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return ids.ApprovalID{}, f.err
	}
	return f.id, nil
}

// TestApplyActionsStagesAConfirmationRequiredActionInsteadOfDeadEnding is the AUTO-T05
// regression: before the Approvals seam was wired in, a 🟡 action returned
// a bare apperrors.ErrRequiresApproval with no approval row ever created —
// runOne parked the run with nothing in detail for MarkRunBlocked to find
// (engine_blocked.go), so it stayed parked forever. ApplyActions must
// call Stage and hand back the real id via StagedApprovalError so runOne
// can record it (rundetail.go's stagedApprovalDetail).
func TestApplyActionsStagesAConfirmationRequiredActionInsteadOfDeadEnding(t *testing.T) {
	fake := &fakeApprovals{id: ids.New[ids.ApprovalKind]()}
	target := datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()}
	action := workflow.Action{
		Kind:   workflow.ActionSendEmail,
		Target: target,
		Args:   json.RawMessage(`{"template":"follow_up"}`),
	}

	// provider is nil and never dereferenced: a 🟡 action stages instead
	// of writing, so reaching the end of this call with no panic already
	// proves the write side of the switch was never touched.
	_, err := ApplyActions(context.Background(), Executors{Approvals: fake}, workflow.Effect{Actions: []workflow.Action{action}})

	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("ApplyActions err = %v, want a *workflow.StagedApprovalError", err)
	}
	if staged.ApprovalID != fake.id {
		t.Errorf("StagedApprovalError.ApprovalID = %v, want the id Stage returned (%v)", staged.ApprovalID, fake.id)
	}
	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Error("a staged 🟡 action must still unwrap to apperrors.ErrRequiresApproval — runOne's dispatch return depends on it")
	}

	if len(fake.calls) != 1 {
		t.Fatalf("Stage called %d times, want exactly 1", len(fake.calls))
	}
	got := fake.calls[0]
	if got.Kind != string(workflow.ActionSendEmail) {
		t.Errorf("StageRequest.Kind = %q, want %q", got.Kind, workflow.ActionSendEmail)
	}
	if got.TargetType != string(datasource.EntityDeal) || got.TargetID != target.ID {
		t.Errorf("StageRequest target = (%q, %v), want (%q, %v)", got.TargetType, got.TargetID, datasource.EntityDeal, target.ID)
	}
	if got.DiffHash == "" {
		t.Error("StageRequest.DiffHash is empty — a fabricated/placeholder hash would let two unrelated actions collide")
	}
	if len(got.ProposedChange) == 0 {
		t.Error("StageRequest.ProposedChange is empty — the approver would see nothing to decide on")
	}
}

// TestApplyActionsRequestApprovalActionAlsoStages proves the request_approval
// catalog action (executor ActionEmitFlowEvent, tierConfirmationRequired per
// catalog_actions.go) reaches the same staging path as advance_deal and
// send_email — it is confirm-first by its own nature, not merely an
// unimplemented executor.
func TestApplyActionsRequestApprovalActionAlsoStages(t *testing.T) {
	fake := &fakeApprovals{id: ids.New[ids.ApprovalKind]()}
	action := workflow.Action{
		Kind:   workflow.ActionEmitFlowEvent,
		Target: datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()},
		Args:   json.RawMessage(`{}`),
	}

	_, err := ApplyActions(context.Background(), Executors{Approvals: fake}, workflow.Effect{Actions: []workflow.Action{action}})

	var staged *workflow.StagedApprovalError
	if !errors.As(err, &staged) {
		t.Fatalf("ApplyActions err = %v, want a *workflow.StagedApprovalError", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("Stage called %d times, want exactly 1", len(fake.calls))
	}
}

// TestApplyActionsNeverSwallowsAStageFailure proves a failing Stage call
// surfaces honestly rather than silently re-creating the dead end: if the
// approvals seam itself errors, ApplyActions must return that error, never
// a nil or a StagedApprovalError with no real row behind it.
func TestApplyActionsNeverSwallowsAStageFailure(t *testing.T) {
	stageErr := errors.New("approvals: database is down")
	fake := &fakeApprovals{err: stageErr}
	action := workflow.Action{
		Kind:   workflow.ActionAdvanceDeal,
		Target: datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()},
		Args:   json.RawMessage(`{"to_stage_id":"11111111-1111-7111-8111-111111111111"}`),
	}

	_, err := ApplyActions(context.Background(), Executors{Approvals: fake}, workflow.Effect{Actions: []workflow.Action{action}})

	if !errors.Is(err, stageErr) {
		t.Fatalf("ApplyActions err = %v, want it to wrap the Stage failure %v", err, stageErr)
	}
	var staged *workflow.StagedApprovalError
	if errors.As(err, &staged) {
		t.Error("a failed Stage call must not be reported as a successful staging")
	}
}

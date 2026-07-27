// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// update_record's own specs. The egress governance that used to live here
// moved to the datasource seam (compose/dispatcher.go): the tool is not the
// only route to an incumbent write, so a per-tool gate could not be the
// control. What remains here is the tool's own contract.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// noConflicts is the ownership answer a record with no human audit history
// produces: nothing is human-owned, so nothing is withheld.
type noConflicts struct{}

func (noConflicts) HumanOwnedConflicts(context.Context, string, ids.UUID, json.RawMessage) ([]string, error) {
	return nil, nil
}

func personFixture(t *testing.T, id ids.UUID) *fakeSoR {
	t.Helper()
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: id}
	return &fakeSoR{records: map[datasource.EntityRef]datasource.Record{
		ref: nativeRecord(datasource.Record{
			Ref: ref, Fields: json.RawMessage(`{"full_name":"Ada Lovelace"}`), Version: 3,
		}),
	}}
}

func updateArgs(t *testing.T, id ids.UUID) json.RawMessage {
	t.Helper()
	in, err := json.Marshal(map[string]any{
		"record_type": "person",
		"id":          id,
		"fields":      map[string]any{"job_title": "Analyst"},
	})
	if err != nil {
		t.Fatalf("marshaling args: %v", err)
	}
	return in
}

// A patch no human has touched applies in the same call — the per-field
// split's whole point is that a machine's own fields are not held hostage.
func TestUpdateRecordAppliesAPatchWithNoHumanOwnedField(t *testing.T) {
	id := ids.NewV7()
	p := personFixture(t, id)
	approvals := &recordingApprovals{}
	tool := updateRecord{p: p, ownership: noConflicts{}, staging: approvals}

	if _, err := tool.Handle(context.Background(), updateArgs(t, id)); err != nil {
		t.Fatalf("Handle err = %v, want nil", err)
	}
	if len(p.updates) != 1 {
		t.Errorf("provider.Update called %d time(s), want 1", len(p.updates))
	}
	if len(approvals.staged) != 0 {
		t.Errorf("staged %d approval(s) for a conflict-free patch, want 0", len(approvals.staged))
	}
}

// A released call performs exactly the effect the human approved, without
// re-asking the precedence question.
func TestUpdateRecordAppliesAReleasedCall(t *testing.T) {
	id := ids.NewV7()
	p := personFixture(t, id)
	tool := updateRecord{p: p, ownership: noConflicts{}, staging: &recordingApprovals{}}

	ctx := WithApprovalRedeemed(context.Background())
	if _, err := tool.Handle(ctx, updateArgs(t, id)); err != nil {
		t.Fatalf("Handle err = %v, want nil", err)
	}
	if len(p.updates) != 1 {
		t.Errorf("provider.Update called %d time(s) after release, want 1", len(p.updates))
	}
}

// A composition with no approvals engine cannot stage a human-owned
// overwrite, so it refuses rather than applying one.
func TestUpdateRecordRefusesAConflictItCannotStage(t *testing.T) {
	id := ids.NewV7()
	p := personFixture(t, id)
	tool := updateRecord{p: p, ownership: fixedOwnership{conflicts: []string{"job_title"}}, staging: nil}

	_, err := tool.Handle(context.Background(), updateArgs(t, id))

	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Fatalf("Handle err = %v, want ErrRequiresApproval", err)
	}
	if len(p.updates) != 0 {
		t.Errorf("provider.Update called %d time(s), want 0", len(p.updates))
	}
}

// A 🟡 tool must not mint an approval for a record whose authority lives
// elsewhere: the human would be asked to release a change that redemption can
// never verify, because the pin and the decidability probe both read our own
// tables. Refusing keeps every staging tool agreeing with the datasource seam,
// which refuses the same write for the same reason.
func TestStagingIsRefusedForANonAuthoritativeTarget(t *testing.T) {
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}
	mirrored := datasource.Record{Ref: ref, Fields: json.RawMessage(`{}`)}

	err := refuseStagingElsewhere(mirrored)

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
	}
	if err := refuseStagingElsewhere(nativeRecord(mirrored)); err != nil {
		t.Errorf("a locally-authoritative record must stage: %v", err)
	}
}

// update_record stages through stageConflicts, not StageInfo, so the derived
// walk in stagingfitness_test.go cannot see it. This is that site's pin: a
// patch WITH a human-owned conflict is what reaches stageConflicts, and for a
// target held elsewhere it must refuse rather than mint an approval no human
// can release.
func TestUpdateRecordRefusesStagingForATargetHeldElsewhere(t *testing.T) {
	id := ids.NewV7()
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: id}
	// Deliberately NOT nativeRecord: this record's authority lives elsewhere.
	p := &fakeSoR{records: map[datasource.EntityRef]datasource.Record{
		ref: {Ref: ref, Fields: json.RawMessage(`{"job_title":"Analyst"}`), Version: 3},
	}}
	approvals := &recordingApprovals{}
	tool := updateRecord{
		p:         p,
		ownership: fixedOwnership{conflicts: []string{"job_title"}},
		staging:   approvals,
	}

	_, err := tool.Handle(context.Background(), updateArgs(t, id))

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("Handle err = %v, want ErrUnsupportedBySoR", err)
	}
	if len(approvals.staged) != 0 {
		t.Errorf("staged %d approval(s) against an externally-held record, want 0", len(approvals.staged))
	}
	if len(p.updates) != 0 {
		t.Errorf("provider.Update called %d time(s), want 0", len(p.updates))
	}
}

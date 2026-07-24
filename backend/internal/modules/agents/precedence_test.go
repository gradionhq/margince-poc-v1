// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The per-field human-edit-precedence split (interfaces.md §2.1) at the
// unit level: how a patch partitions, and — the ADR-0036 binding — that
// a staged sub-patch's diff_hash is exactly what the dispatch layer will
// compute when the agent replays the staged fields with approval_id, so
// a human approving the split releases that field write and nothing else.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fixedOwnership answers the ownership probe from a fixed conflict list.
type fixedOwnership struct {
	conflicts []string
	err       error
}

func (f fixedOwnership) HumanOwnedConflicts(context.Context, string, ids.UUID, json.RawMessage) ([]string, error) {
	return f.conflicts, f.err
}

func TestSplitHumanOwnedPartitionsThePatch(t *testing.T) {
	ctx := context.Background()
	id := ids.NewV7()
	patch := json.RawMessage(`{"title":"CTO","full_name":"Greta Machine","email":"g@example.com"}`)

	split, err := SplitHumanOwned(ctx, fixedOwnership{conflicts: []string{"full_name"}}, "person", id, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(split.Conflicts) != 1 || split.Conflicts[0] != "full_name" {
		t.Fatalf("conflicts = %v, want [full_name]", split.Conflicts)
	}
	var staged, autoExec map[string]string
	if err := json.Unmarshal(split.Staged, &staged); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(split.AutoExecute, &autoExec); err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 || staged["full_name"] != "Greta Machine" {
		t.Fatalf("staged sub-patch = %v, want exactly the human-owned field", staged)
	}
	if len(autoExec) != 2 || autoExec["title"] != "CTO" || autoExec["email"] != "g@example.com" {
		t.Fatalf("auto-execute remainder = %v, want the two agent-safe fields", autoExec)
	}
}

func TestSplitHumanOwnedEdges(t *testing.T) {
	ctx := context.Background()
	id := ids.NewV7()
	patch := json.RawMessage(`{"full_name":"Greta Machine"}`)

	// No conflicts: the whole patch is auto-execute, nothing staged.
	split, err := SplitHumanOwned(ctx, fixedOwnership{}, "person", id, patch)
	if err != nil || split.Staged != nil || string(split.AutoExecute) != string(patch) {
		t.Fatalf("conflict-free split = %+v (err %v), want the untouched patch auto-execute", split, err)
	}

	// Every field human-owned: no auto-execute remainder.
	split, err = SplitHumanOwned(ctx, fixedOwnership{conflicts: []string{"full_name"}}, "person", id, patch)
	if err != nil || split.AutoExecute != nil {
		t.Fatalf("all-conflict split = %+v (err %v), want no auto-execute remainder", split, err)
	}

	// A nil resolver cannot answer the precedence question — fail closed.
	if _, err := SplitHumanOwned(ctx, nil, "person", id, patch); err == nil {
		t.Fatal("nil ownership resolver must refuse, not admit the overwrite")
	}

	// A conflict outside the patch is a resolver defect, not a split.
	if _, err := SplitHumanOwned(ctx, fixedOwnership{conflicts: []string{"phantom"}}, "person", id, patch); err == nil {
		t.Fatal("a conflict the patch does not carry must be refused")
	}

	// A probe failure propagates — never degraded into "no conflicts".
	probeErr := errors.New("audit trail unreachable")
	if _, err := SplitHumanOwned(ctx, fixedOwnership{err: probeErr}, "person", id, patch); !errors.Is(err, probeErr) {
		t.Fatalf("probe failure → %v, want it propagated", err)
	}
}

// recordingApprovals captures staging and redemption for assertions.
type recordingApprovals struct {
	staged    []StageRequest
	redeemed  []string // "id tool hash"
	redeemErr error
}

func (r *recordingApprovals) Stage(_ context.Context, in StageRequest) (ids.ApprovalID, error) {
	r.staged = append(r.staged, in)
	return ids.New[ids.ApprovalKind](), nil
}

func (r *recordingApprovals) Redeem(_ context.Context, id ids.ApprovalID, tool, hash string) error {
	if r.redeemErr != nil {
		return r.redeemErr
	}
	r.redeemed = append(r.redeemed, fmt.Sprintf("%s %s %s", id, tool, hash))
	return nil
}

// fixedProvider serves one record and captures updates; only the calls
// the update tool makes are implemented — anything else is out of the
// test's contract.
type fixedProvider struct {
	datasource.SystemOfRecordProvider
	record  datasource.Record
	updates []datasource.UpdateInput
}

func (f *fixedProvider) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	return f.record, nil
}

func (f *fixedProvider) Update(_ context.Context, in datasource.UpdateInput) (datasource.EntityRef, error) {
	f.updates = append(f.updates, in)
	return in.Ref, nil
}

// patchBytes asserts a captured UpdateInput.Patch back to the raw JSON
// the update tool sends through the provider seam.
//
//craft:ignore naked-any UpdateInput.Patch is any at the datasource seam; this helper narrows it back
func patchBytes(t *testing.T, patch any) json.RawMessage {
	t.Helper()
	raw, ok := patch.(json.RawMessage)
	if !ok {
		t.Fatalf("captured patch is %T, want json.RawMessage", patch)
	}
	return raw
}

func agentCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeWrite),
	})
}

// splitRegistry wires update_record over fakes: a person whose full_name
// is human-owned, an approvals recorder, and an auto-execute admission gate.
func splitRegistry(conflicts []string, approvals *recordingApprovals, provider *fixedProvider) *Registry {
	r := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}))
	r.Register(updateRecord{p: provider, ownership: fixedOwnership{conflicts: conflicts}, staging: r.approvals})
	return r
}

// replayPlusApprovalID rebuilds the advertised replay call with the
// approval reference attached — what a compliant agent re-sends.
func replayPlusApprovalID(t *testing.T, replay json.RawMessage, approvalID ids.ApprovalID) json.RawMessage {
	t.Helper()
	var call map[string]json.RawMessage
	if err := json.Unmarshal(replay, &call); err != nil {
		t.Fatal(err)
	}
	call["approval_id"] = json.RawMessage(fmt.Sprintf("%q", approvalID))
	out, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertRedeemedHash proves the redemption consumed an approval bound to
// exactly the staged sub-patch hash.
func assertRedeemedHash(t *testing.T, approvals *recordingApprovals, diffHash string) {
	t.Helper()
	if len(approvals.redeemed) != 1 {
		t.Fatalf("redeemed = %v, want exactly one consumed approval", approvals.redeemed)
	}
	want := fmt.Sprintf("update_record %s", diffHash)
	if got := approvals.redeemed[0]; !strings.HasSuffix(got, want) {
		t.Fatalf("redemption %q not bound to the staged sub-patch hash %q", got, diffHash)
	}
}

func TestUpdateRecordMixedPatchSplitsAndBindsTheSubPatch(t *testing.T) {
	target := ids.NewV7()
	provider := &fixedProvider{record: datasource.Record{
		Ref:     datasource.EntityRef{Type: datasource.EntityPerson, ID: target},
		Fields:  json.RawMessage(`{"full_name":"Greta Human","title":"CTO"}`),
		Version: 7,
	}}
	approvals := &recordingApprovals{}
	r := splitRegistry([]string{"full_name"}, approvals, provider)

	call := fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"full_name":"Greta Machine","title":"CTO"}}`, target)
	out, err := r.Invoke(agentCtx(), "update_record", json.RawMessage(call))
	if err != nil {
		t.Fatalf("mixed patch must succeed for its auto-execute half: %v", err)
	}

	// The auto-execute half applied exactly the non-conflicting field.
	if len(provider.updates) != 1 {
		t.Fatalf("updates = %d, want exactly the auto-execute remainder applied once", len(provider.updates))
	}
	var applied map[string]string
	if err := json.Unmarshal(patchBytes(t, provider.updates[0].Patch), &applied); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied["title"] != "CTO" {
		t.Fatalf("applied patch = %v, want only the agent-safe field", applied)
	}

	// Exactly the human-owned field was staged, pinned to the version the
	// approving human will see.
	if len(approvals.staged) != 1 {
		t.Fatalf("staged = %d, want exactly one approval", len(approvals.staged))
	}
	staged := approvals.staged[0]
	if staged.Tool != "update_record" || staged.TargetType != "person" || staged.TargetID != target ||
		staged.TargetVersion == nil || *staged.TargetVersion != 7 {
		t.Fatalf("staged binding = %+v, want update_record on the person pinned to the post-write version 7", staged)
	}

	// The result names the split: the record, the withheld fields, the
	// approval, and the exact replay call.
	var result struct {
		Version        int64 `json:"version"`
		StagedApproval struct {
			ApprovalID ids.ApprovalID  `json:"approval_id"`
			Fields     []string        `json:"fields"`
			Replay     json.RawMessage `json:"replay"`
		} `json:"staged_approval"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.StagedApproval.Fields) != 1 || result.StagedApproval.Fields[0] != "full_name" {
		t.Fatalf("result staged fields = %v", result.StagedApproval.Fields)
	}

	// THE ADR-0036 BINDING: replaying the advertised call with approval_id
	// redeems against exactly the staged diff_hash — the sub-patch is an
	// authority object on its own terms.
	withApproval := replayPlusApprovalID(t, result.StagedApproval.Replay, result.StagedApproval.ApprovalID)
	if _, err := r.Invoke(agentCtx(), "update_record", withApproval); err != nil {
		t.Fatalf("replaying the staged sub-patch with approval_id → %v", err)
	}
	assertRedeemedHash(t, approvals, staged.DiffHash)
	// The redeemed call applied the full (sub-)patch without re-splitting.
	if len(provider.updates) != 2 {
		t.Fatalf("updates after redemption = %d, want the released field written once", len(provider.updates))
	}
	var released map[string]string
	if err := json.Unmarshal(patchBytes(t, provider.updates[1].Patch), &released); err != nil {
		t.Fatal(err)
	}
	if len(released) != 1 || released["full_name"] != "Greta Machine" {
		t.Fatalf("released patch = %v, want exactly the approved field", released)
	}
}

func TestUpdateRecordAllHumanOwnedStagesTheWholeCall(t *testing.T) {
	target := ids.NewV7()
	provider := &fixedProvider{record: datasource.Record{
		Ref:     datasource.EntityRef{Type: datasource.EntityPerson, ID: target},
		Fields:  json.RawMessage(`{"full_name":"Greta Human"}`),
		Version: 3,
	}}
	approvals := &recordingApprovals{}
	r := splitRegistry([]string{"full_name"}, approvals, provider)

	call := fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"full_name":"Greta Machine"}}`, target)
	_, err := r.Invoke(agentCtx(), "update_record", json.RawMessage(call))
	var stagedErr *workflow.StagedApprovalError
	if !errors.As(err, &stagedErr) || !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Fatalf("all-human patch → %v, want a staged ErrRequiresApproval", err)
	}
	if len(provider.updates) != 0 {
		t.Fatal("nothing may apply when every field is human-owned")
	}
	if len(approvals.staged) != 1 || approvals.staged[0].TargetVersion == nil || *approvals.staged[0].TargetVersion != 3 {
		t.Fatalf("staged = %+v, want one approval pinned to the current version", approvals.staged)
	}

	// The approved retry is the IDENTICAL call plus approval_id.
	retry := fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"full_name":"Greta Machine"},"approval_id":%q}`, target, stagedErr.ApprovalID)
	if _, err := r.Invoke(agentCtx(), "update_record", json.RawMessage(retry)); err != nil {
		t.Fatalf("identical retry with approval_id → %v", err)
	}
	assertRedeemedHash(t, approvals, approvals.staged[0].DiffHash)
	if len(provider.updates) != 1 {
		t.Fatalf("updates after redemption = %d, want exactly one", len(provider.updates))
	}
}

func TestAutoExecuteCallWithApprovalIDValidatesInsteadOfIgnoring(t *testing.T) {
	target := ids.NewV7()
	provider := &fixedProvider{record: datasource.Record{
		Ref: datasource.EntityRef{Type: datasource.EntityPerson, ID: target},
	}}
	approvals := &recordingApprovals{redeemErr: fmt.Errorf("already redeemed: %w", apperrors.ErrApprovalTokenInvalid)}
	r := splitRegistry(nil, approvals, provider)

	call := fmt.Sprintf(`{"record_type":"person","id":%q,"fields":{"title":"CTO"},"approval_id":%q}`, target, ids.NewV7())
	if _, err := r.Invoke(agentCtx(), "update_record", json.RawMessage(call)); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("asserted-but-invalid approval_id → %v, want the redemption failure, not a silent auto-execute run", err)
	}
	if len(provider.updates) != 0 {
		t.Fatal("a failed redemption must have zero side effects")
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Modify-then-approve (ADR-0036 §4, B-EP07.8): the human's edited
// payload replaces the staged change under a fresh diff_hash, the audit
// row records BOTH sides of the delta, and the old hash stops opening
// anything — an agent replaying its original call cannot ride a human
// edit past the gate.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestModifyThenApproveRebindsTheAuthority(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	pipeline, open, _ := DealFixture(t, e)
	svc := approvals.NewService(e.Pool)

	var effectPayload json.RawMessage
	var effectHash string
	svc.WithEffect("advance_deal", func(_ context.Context, _ ids.ApprovalID, change json.RawMessage, hash string) error {
		effectPayload, effectHash = change, hash
		return nil
	})

	deal := e.SeedDeal(t, "Mine", pipeline, open, &e.Rep1)
	original, originalHash, err := diffhash.Canonical(json.RawMessage(`{"stage": "proposal", "note": "agent version"}`))
	if err != nil {
		t.Fatal(err)
	}
	approvalID, err := svc.Stage(e.AgentCtx(), approvals.StageInput{
		Kind: "advance_deal", ProposedChange: original, DiffHash: originalHash,
		TargetType: "deal", TargetID: deal, Summary: "edit test staging",
	})
	if err != nil {
		t.Fatal(err)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	edited := json.RawMessage(`{"stage": "proposal", "note": "human version"}`)
	decided, err := svc.DecideEdited(rep, approvalID, edited)
	if err != nil {
		t.Fatal(err)
	}
	_, editedHash, err := diffhash.Canonical(edited)
	if err != nil {
		t.Fatal(err)
	}
	if decided.DiffHash != editedHash || decided.DiffHash == originalHash {
		t.Fatalf("decision must rebind to the edited hash: got %s", decided.DiffHash)
	}
	// jsonb re-renders the stored bytes, so the assertion is on content.
	var storedChange map[string]any
	if err := json.Unmarshal(decided.ProposedChange, &storedChange); err != nil {
		t.Fatal(err)
	}
	if storedChange["note"] != "human version" {
		t.Fatalf("proposed_change must be the human's version: %s", decided.ProposedChange)
	}

	// The follow-on effect executed the HUMAN-edited change (jsonb
	// re-renders the stored bytes, so the payload assertion is on content).
	var effectChange map[string]any
	if err := json.Unmarshal(effectPayload, &effectChange); err != nil {
		t.Fatal(err)
	}
	if effectChange["note"] != "human version" || effectHash != editedHash {
		t.Fatalf("effect ran %s under %s, want the edited payload", effectPayload, effectHash)
	}

	assertEditAuditCarriesBothSides(t, owner, approvalID, originalHash, editedHash)

	// No-bypass: the agent's original call no longer matches the
	// authority; only the edited call redeems — the gate re-admits and
	// re-tiers that call like any other.
	if _, _, err := svc.Redeem(e.AgentCtx(), approvalID, "advance_deal", originalHash); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("redeeming the pre-edit call → %v, want ErrApprovalTokenInvalid", err)
	}
	if _, _, err := svc.Redeem(e.AgentCtx(), approvalID, "advance_deal", editedHash); err != nil {
		t.Fatalf("redeeming the edited call → %v, want ok", err)
	}
}

// assertEditAuditCarriesBothSides checks the approve audit row against
// ADR-0036 §4: it must keep the agent's original proposal AND the
// human's edited version, each under its own diff hash.
func assertEditAuditCarriesBothSides(t *testing.T, owner *pgx.Conn, approvalID ids.ApprovalID, originalHash, editedHash string) {
	t.Helper()
	// The audit row carries both the original proposal and the delta.
	// (jsonb reorders keys, so the assertion is on content, not bytes.)
	var evidenceRaw []byte
	if err := owner.QueryRow(context.Background(),
		`SELECT evidence FROM audit_log WHERE entity_type = 'approval' AND entity_id = $1 AND action = 'approve'`,
		approvalID).Scan(&evidenceRaw); err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		OriginalChange   map[string]any `json:"original_change"`
		EditedChange     map[string]any `json:"edited_change"`
		OriginalDiffHash string         `json:"original_diff_hash"`
		EditedDiffHash   string         `json:"edited_diff_hash"`
	}
	if err := json.Unmarshal(evidenceRaw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.OriginalChange["note"] != "agent version" || evidence.OriginalDiffHash != originalHash {
		t.Fatalf("audit must keep the agent's original proposal: %+v", evidence)
	}
	if evidence.EditedChange["note"] != "human version" || evidence.EditedDiffHash != editedHash {
		t.Fatalf("audit must keep the human's edited version: %+v", evidence)
	}
}

// Modify-then-approve must not become a row-scope escape. The follow-on effect
// resolves the record it writes from an id INSIDE the payload — not from
// approval.target_entity_id — and several executors run it under a system
// principal, which makes the stores' own RBAC and row-scope gates no-ops. So an
// approver who swaps an id would write to a record their scope hides, while the
// decide-time visibility probe and the version pin both still pass against the
// untouched original target. The edit is pinned to the records the staging
// named; nothing else about it is constrained.
func TestModifyThenApproveCannotRetargetTheEffect(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	svc := approvals.NewService(e.Pool)

	var effectRan bool
	svc.WithEffect("advance_deal", func(_ context.Context, _ ids.ApprovalID, _ json.RawMessage, _ string) error {
		effectRan = true
		return nil
	})

	mine := e.SeedDeal(t, "Mine", pipeline, open, &e.Rep1)
	hidden := e.SeedDeal(t, "Another team's book", pipeline, open, &e.Rep2)
	staged, stagedHash, err := diffhash.Canonical(
		json.RawMessage(`{"deal_id":"` + mine.String() + `","note":"agent version"}`))
	if err != nil {
		t.Fatal(err)
	}
	approvalID, err := svc.Stage(e.AgentCtx(), approvals.StageInput{
		Kind: "advance_deal", ProposedChange: staged, DiffHash: stagedHash,
		TargetType: "deal", TargetID: mine, Summary: "retarget test staging",
	})
	if err != nil {
		t.Fatal(err)
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	retarget := json.RawMessage(`{"deal_id":"` + hidden.String() + `","note":"agent version"}`)
	var retargeted *approvals.RetargetedEditError
	if _, err := svc.DecideEdited(rep, approvalID, retarget); !errors.As(err, &retargeted) {
		t.Fatalf("edit repointing deal_id at a hidden record → %v, want RetargetedEditError", err)
	}
	if effectRan {
		t.Fatal("the effect executed on a refused edit — the refusal must land before anything runs")
	}

	// Nothing was decided, and the ORIGINAL payload is intact: a refused edit
	// must not half-apply itself.
	after, err := svc.Get(rep, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "pending" || after.DiffHash != stagedHash {
		t.Fatalf("after a refused retarget the approval is %s/%s, want pending on the staged hash",
			after.Status, after.DiffHash)
	}

	// The positive control: an edit that corrects the CONTENT still works, so
	// this test cannot pass by modify-then-approve being broken outright.
	corrected := json.RawMessage(`{"deal_id":"` + mine.String() + `","note":"human version"}`)
	if _, err := svc.DecideEdited(rep, approvalID, corrected); err != nil {
		t.Fatalf("editing the note → %v, want ok — pinning the record must not freeze the payload", err)
	}
	if !effectRan {
		t.Fatal("the accepted edit never reached the effect")
	}
}

// An edit that cannot canonicalize is refused as a validation error and
// decides nothing: the staging stays pending and decidable.
func TestMalformedEditLeavesTheStagingPending(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	svc := approvals.NewService(e.Pool)

	deal := e.SeedDeal(t, "Mine", pipeline, open, &e.Rep1)
	approvalID, _ := stageAdvance(t, svc, e, deal)

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	var invalid *approvals.InvalidEditError
	if _, err := svc.DecideEdited(rep, approvalID, json.RawMessage(`[1,2]`)); !errors.As(err, &invalid) {
		t.Fatalf("array edit → %v, want InvalidEditError", err)
	}
	if _, err := svc.DecideEdited(rep, approvalID, nil); !errors.As(err, &invalid) {
		t.Fatalf("empty edit → %v, want InvalidEditError", err)
	}
	// Still pending: a plain approve goes through.
	if _, err := svc.Decide(rep, approvalID, true, nil); err != nil {
		t.Fatalf("staging must remain decidable after a refused edit: %v", err)
	}
}

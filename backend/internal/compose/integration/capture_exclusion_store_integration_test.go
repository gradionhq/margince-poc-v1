// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The RC-2 exclusion-rule store (capture.md CAP-WIRE-2 / CAP-DDL-3): create
// is idempotent on (user, kind, value); list is scoped to the calling human;
// delete is idempotent; and managing rules is human-only (an agent must not
// widen or narrow a human's personal-mail boundary).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func repCtx(e *searchEnv, user ids.UUID) context.Context {
	return principal.WithActor(
		principal.WithWorkspaceID(context.Background(), e.WS),
		principal.Principal{Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user},
	)
}

func TestExclusionStoreCreateIsIdempotentListIsScoped(t *testing.T) {
	e := setupSearch(t)
	store := capture.NewExclusions(e.Pool)

	rep1 := repCtx(e, e.Rep1)
	first, err := store.Create(rep1, "sender_domain", "Personal-Family.Example")
	if err != nil {
		t.Fatal(err)
	}
	// Re-add the same rule (different casing) → same row, no duplicate.
	again, err := store.Create(rep1, "sender_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID {
		t.Fatalf("idempotent re-add minted a new row: %v != %v", first.ID, again.ID)
	}
	if again.Value != "personal-family.example" {
		t.Errorf("value not normalized to lowercase: %q", again.Value)
	}

	// A different user's rule is invisible to Rep1's list (per-user scope).
	if _, err := store.Create(repCtx(e, e.Rep3), "label", "Private"); err != nil {
		t.Fatal(err)
	}
	rules, err := store.List(rep1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Kind != "sender_domain" {
		t.Fatalf("list not scoped to the caller: %+v", rules)
	}
}

func TestExclusionStoreDeleteIsIdempotentAndScoped(t *testing.T) {
	e := setupSearch(t)
	store := capture.NewExclusions(e.Pool)
	rep1 := repCtx(e, e.Rep1)

	rule, err := store.Create(rep1, "recipient_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
	// Rep3 cannot delete Rep1's rule (scoped to the owner) — a no-op.
	if err := store.Delete(repCtx(e, e.Rep3), rule.ID); err != nil {
		t.Fatal(err)
	}
	if rules, err := store.List(rep1); err != nil || len(rules) != 1 {
		t.Fatalf("another user's delete removed the rule: %+v %v", rules, err)
	}
	// The owner deletes it; a second delete is an idempotent no-op.
	if err := store.Delete(rep1, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(rep1, rule.ID); err != nil {
		t.Fatalf("second delete errored, want idempotent no-op: %v", err)
	}
	if rules, err := store.List(rep1); err != nil || len(rules) != 0 {
		t.Fatalf("delete left the rule behind: %+v %v", rules, err)
	}
}

// Adding or removing an exclusion rule MOVES the user's personal-mail boundary,
// and every move is attributed: the rule's creation, its removal (the row is
// archived rather than erased, so what was excluded survives), and the re-add
// that puts it back. A re-add of a rule that is already live moved nothing and
// is not a mutation.
//
// The order matters as much as the count. A trail whose last entry says
// "archive" for a boundary that is currently suppressing capture tells an
// auditor the opposite of the truth.
func TestExclusionStoreAuditsEveryBoundaryMove(t *testing.T) {
	e := setupSearch(t)
	store := capture.NewExclusions(e.Pool)
	rep1 := repCtx(e, e.Rep1)

	rule, err := store.Create(rep1, "sender_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
	assertExclusionTrail(t, e, rule.ID, "create")

	// A re-add of a LIVE rule returns the same row and changes nothing.
	again, err := store.Create(rep1, "sender_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != rule.ID {
		t.Fatalf("idempotent re-add minted a new row %v, want %v", again.ID, rule.ID)
	}
	assertExclusionTrail(t, e, rule.ID, "create")

	if err := store.Delete(rep1, rule.ID); err != nil {
		t.Fatal(err)
	}
	if rules, err := store.List(rep1); err != nil || len(rules) != 0 {
		t.Fatalf("the archived rule is still listed: %+v %v", rules, err)
	}
	var archivedAt *time.Time
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT archived_at FROM capture_exclusion_rule WHERE id = $1`, rule.ID).Scan(&archivedAt)
	}); err != nil {
		t.Fatalf("the rule row did not survive the delete: %v", err)
	}
	if archivedAt == nil {
		t.Fatal("the rule row survived but archived_at is NULL — the delete did not archive it")
	}
	assertExclusionTrail(t, e, rule.ID, "create", "archive")

	// A second delete matches no live row: still a no-op, and no audit row
	// claiming a removal that did not happen.
	if err := store.Delete(rep1, rule.ID); err != nil {
		t.Fatalf("second delete errored, want idempotent no-op: %v", err)
	}
	assertExclusionTrail(t, e, rule.ID, "create", "archive")

	// Re-adding the same rule resurrects the archived row rather than minting a
	// second one, and the trail must end saying the boundary is BACK.
	back, err := store.Create(rep1, "sender_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != rule.ID {
		t.Fatalf("re-add minted a new row %v, want the resurrected %v", back.ID, rule.ID)
	}
	if rules, err := store.List(rep1); err != nil || len(rules) != 1 {
		t.Fatalf("the resurrected rule is not listed: %+v %v", rules, err)
	}
	assertExclusionTrail(t, e, rule.ID, "create", "archive", "restore")
}

// assertExclusionTrail pins one rule's audit trail to the exact sequence of
// verbs, and every entry to the human who acted.
func assertExclusionTrail(t *testing.T, e *searchEnv, ruleID ids.UUID, want ...string) {
	t.Helper()
	audits := exclusionRuleAudits(t, e, ruleID)
	got := make([]string, 0, len(audits))
	for _, a := range audits {
		got = append(got, a.Action)
	}
	if len(got) != len(want) {
		t.Fatalf("audit trail = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("audit trail = %v, want %v", got, want)
		}
		if actor := "human:" + e.Rep1.String(); audits[i].ActorID != actor {
			t.Errorf("%s audited actor %q, want %q", got[i], audits[i].ActorID, actor)
		}
	}
}

// exclusionRuleAudits reads the audit trail for one exclusion rule.
func exclusionRuleAudits(t *testing.T, e *searchEnv, ruleID ids.UUID) []lifecycleAudit {
	t.Helper()
	var out []lifecycleAudit
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT action, actor_id, after FROM audit_log
			 WHERE entity_type = 'capture_exclusion_rule' AND entity_id = $1
			 ORDER BY occurred_at, id`, ruleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a lifecycleAudit
			if err := rows.Scan(&a.Action, &a.ActorID, &a.After); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the exclusion-rule audit trail: %v", err)
	}
	return out
}

func TestExclusionStoreIsHumanOnly(t *testing.T) {
	e := setupSearch(t)
	store := capture.NewExclusions(e.Pool)
	agentCtx := principal.WithActor(
		principal.WithWorkspaceID(context.Background(), e.WS),
		principal.Principal{Type: principal.PrincipalAgent, ID: "agent:x", UserID: e.Rep1},
	)
	if _, err := store.Create(agentCtx, "sender_domain", "personal-family.example"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent create → %v, want ErrPermissionDenied", err)
	}
	if _, err := store.List(agentCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent list → %v, want ErrPermissionDenied", err)
	}
	if err := store.Delete(agentCtx, ids.NewV7()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("agent delete → %v, want ErrPermissionDenied", err)
	}
}

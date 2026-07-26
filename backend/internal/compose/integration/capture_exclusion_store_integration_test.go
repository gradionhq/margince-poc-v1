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

// Removing an exclusion rule is a mutation of the user's own personal-mail
// boundary: the row is archived rather than erased, so the trail of what was
// excluded and when survives the removal, and the removal itself is audited.
func TestExclusionStoreDeleteArchivesTheRuleAndAuditsIt(t *testing.T) {
	e := setupSearch(t)
	store := capture.NewExclusions(e.Pool)
	rep1 := repCtx(e, e.Rep1)

	rule, err := store.Create(rep1, "sender_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
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

	audits := exclusionRuleAudits(t, e, rule.ID)
	if len(audits) != 1 {
		t.Fatalf("delete wrote %d audit rows, want exactly 1: %+v", len(audits), audits)
	}
	if audits[0].Action != "archive" {
		t.Errorf("delete audited as %q, want %q", audits[0].Action, "archive")
	}
	if want := "human:" + e.Rep1.String(); audits[0].ActorID != want {
		t.Errorf("delete audited actor %q, want %q", audits[0].ActorID, want)
	}

	// A second delete matches no live row: still a no-op, and no audit row
	// claiming a removal that did not happen.
	if err := store.Delete(rep1, rule.ID); err != nil {
		t.Fatalf("second delete errored, want idempotent no-op: %v", err)
	}
	if audits := exclusionRuleAudits(t, e, rule.ID); len(audits) != 1 {
		t.Fatalf("a no-op delete wrote an audit row: %+v", audits)
	}

	// Re-adding the same rule resurrects the archived row rather than
	// minting a second one.
	again, err := store.Create(rep1, "sender_domain", "personal-family.example")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != rule.ID {
		t.Fatalf("re-add minted a new row %v, want the resurrected %v", again.ID, rule.ID)
	}
	if rules, err := store.List(rep1); err != nil || len(rules) != 1 {
		t.Fatalf("the resurrected rule is not listed: %+v %v", rules, err)
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

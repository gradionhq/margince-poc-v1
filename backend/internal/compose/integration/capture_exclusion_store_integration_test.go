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

	"github.com/gradionhq/margince/backend/internal/modules/capture"
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

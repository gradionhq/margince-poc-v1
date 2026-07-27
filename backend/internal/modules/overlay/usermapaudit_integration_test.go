// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func countMappingAudits(ctx context.Context, t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE entity_type = 'mirror_user_map' AND action = $1`,
			action).Scan(&n)
	}); err != nil {
		t.Fatalf("counting %s audits: %v", action, err)
	}
	return n
}

func TestFirstMappingWritesACreateAudit(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "email"); err != nil {
		t.Fatalf("mapping: %v", err)
	}
	if got := countMappingAudits(ctx, t, pool, "create"); got != 1 {
		t.Fatalf("want 1 create audit, got %d", got)
	}
}

// The sweep re-runs every tick. An unchanged re-seed must write NOTHING, or
// the audit log fills with one row per owner per tick and stops being
// readable evidence.
func TestIdempotentReseedWritesNoAudit(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	for range 3 {
		if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "email"); err != nil {
			t.Fatalf("mapping: %v", err)
		}
	}
	if got := countMappingAudits(ctx, t, pool, "create"); got != 1 {
		t.Fatalf("want exactly 1 create audit across 3 identical seeds, got %d", got)
	}
	if got := countMappingAudits(ctx, t, pool, "update"); got != 0 {
		t.Fatalf("an unchanged re-seed must write no update audit, got %d", got)
	}
}

// Pinning the same owner but flipping email -> manual changes governance
// materially: the row becomes immune to revalidation and to the sweep. A
// change predicate that compares only the owner id would silently miss it.
func TestMatchSourceFlipAloneWritesAnUpdateAudit(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, stubOwnerEmails{"owner-1": "rep@acme.test"})
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "email"); err != nil {
		t.Fatalf("seeding by email: %v", err)
	}
	if err := store.UpsertUserMap(ctx, rep, "hubspot", "owner-1", "manual"); err != nil {
		t.Fatalf("pinning manually: %v", err)
	}
	if got := countMappingAudits(ctx, t, pool, "update"); got != 1 {
		t.Fatalf("an email->manual flip on the same owner must audit, got %d update audits", got)
	}
}

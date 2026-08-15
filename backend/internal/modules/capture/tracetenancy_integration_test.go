// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// Two workspaces, because one cannot fail.
//
// There is no RLS behind this table since 0217: the store's own predicates ARE
// the isolation, and a fixture with a single tenant cannot tell a query that
// spells them from one that forgot. The join to the disposition ledger is the
// case that matters — it keys on an ADDRESS, and an address is not unique
// across tenants, so an unscoped join answers with another installation's
// verdict about the same person.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// secondWorkspace seeds a neighbour tenant and returns a handle bound to it.
func secondWorkspace(t *testing.T, ctx context.Context) (context.Context, *database.DB) {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`, ws, "neighbour-"+ws.String()); err != nil {
		t.Fatalf("seeding the neighbour workspace: %v", err)
	}
	return principal.WithWorkspaceID(ctx, ws), database.BindTo(pool, ids.From[ids.WorkspaceKind](ws))
}

func TestAnotherWorkspacesVerdictNeverReachesThisOne(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	mine := memberContext(ctx, ws, me)
	const shared = "info@vendor.test" // an address both tenants correspond with

	// Mine: a deferred message from that sender, with NO answer yet.
	seedDeferredMessage(mine, t, db, me, "tenancy-mine", shared, false)

	// The neighbour: the same address, judged and resolved.
	neighbourCtx, neighbourDB := secondWorkspace(t, context.Background())
	neighbour := ids.NewV7()
	seedDeferredMessage(neighbourCtx, t, neighbourDB, neighbour, "tenancy-theirs", shared, true)

	window, err := store.ListMine(mine, nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 — only this workspace's row", len(window.Entries))
	}
	// The neighbour's ledger row must not answer for my sender. Unscoped, the
	// join matches on the address alone and reports THEIR verdict as mine.
	if got := window.Entries[0].Resolution; got != nil {
		t.Errorf("resolution = %+v, want none — that answer belongs to another workspace", got)
	}
	if window.Funnel["deferred"] != 1 {
		t.Errorf("funnel[deferred] = %d, want 1 — the funnel counts this tenant's rows only", window.Funnel["deferred"])
	}
}

// One sender with several resolved rows must not multiply their messages.
// The ledger keeps a row per address per state, so a plain join fans out and the
// same message appears once per historical disposition — which would also break
// the page's own LIMIT and its cursor.
func TestASendersHistoryDoesNotMultiplyTheirMessages(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	mine := memberContext(ctx, ws, me)
	const sender = "twice@judged.test"

	seedDeferredMessage(mine, t, db, me, "fanout-1", sender, true)
	// A second, older resolved row for the same address — a sender judged noise
	// and later judged real.
	if err := db.Tx(mine, func(tx pgx.Tx) error {
		_, err := tx.Exec(mine, `
			INSERT INTO capture_pending_counterparty
			       (workspace_id, email, domain, activity_id, owner_id, status, kind, resolved_at)
			SELECT workspace_id, email, domain, activity_id, owner_id, 'noise', 'spam', now() - interval '2 hours'
			  FROM capture_pending_counterparty WHERE email = $1`, sender)
		return err
	}); err != nil {
		t.Fatalf("seeding a second disposition: %v", err)
	}

	window, err := store.ListMine(mine, nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 — a sender's history must not multiply their messages", len(window.Entries))
	}
	// Newest first, and an open question ahead of a resolved one: what the
	// sender IS now, not what they were.
	if got := window.Entries[0].Resolution; got == nil || got.Status != "real" {
		t.Errorf("resolution = %+v, want the current answer", got)
	}
}

// The contract advertises a cursor and a limit; nothing exercised them.
func TestASecondPageContinuesUnderTheSameScope(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	mine := memberContext(ctx, ws, me)
	for _, id := range []string{"page-1", "page-2", "page-3"} {
		seedTrace(mine, t, db, me, id, 0)
	}

	one := 1
	first, err := store.ListMine(mine, nil, &one)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Entries) != 1 || first.Next == "" {
		t.Fatalf("first page = %d entries, next=%q — want one entry and a cursor", len(first.Entries), first.Next)
	}
	// The funnel describes the WINDOW, so it does not shrink with the page.
	if first.Funnel["captured"] != 3 {
		t.Errorf("funnel[captured] = %d on a one-row page, want 3 — the funnel counts the window", first.Funnel["captured"])
	}

	second, err := store.ListMine(mine, &first.Next, &one)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Entries) != 1 {
		t.Fatalf("second page = %d entries, want 1", len(second.Entries))
	}
	if second.Entries[0].ID == first.Entries[0].ID {
		t.Error("the second page repeated the first page's row — the cursor did not advance")
	}
}

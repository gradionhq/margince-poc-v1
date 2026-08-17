// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The trace window's JOIN to the disposition ledger, and its page.
//
// The join keys on an ADDRESS, and one address can carry several ledger rows —
// a sender judged noise and later judged real. A plain join fans those out and
// reports one message once per historical verdict, which also breaks the page's
// own LIMIT and its cursor. That is what these hold.

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
			       (email, domain, activity_id, owner_id, status, kind, resolved_at)
			SELECT email, domain, activity_id, owner_id, 'noise', 'spam', now() - interval '2 hours'
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

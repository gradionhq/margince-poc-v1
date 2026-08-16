// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// One sender's verdict covers every message they sent.
//
// The ledger keeps one open question per ADDRESS and records the first activity
// that raised it. A resolution joined by activity id would therefore answer only
// that first message, and a stranger's second and later mail would read "waiting
// on a verdict" forever — after the verdict had landed.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// seedDeferredMessage writes a MAIL activity from one sender, a trace row for
// it, and (on the first call for that address) the ledger's open question.
func seedDeferredMessage(ctx context.Context, t *testing.T, db *database.DB,
	owner ids.UUID, sourceID, sender string, withLedger bool,
) {
	t.Helper()
	seedDeferredRecord(ctx, t, db, owner, sourceID, sender, "", withLedger)
}

// seedDeferredRecord is seedDeferredMessage with the transport named: empty
// lands a mail record, a provider lands a message on that channel.
func seedDeferredRecord(ctx context.Context, t *testing.T, db *database.DB,
	owner ids.UUID, sourceID, sender, channelProvider string, withLedger bool,
) {
	t.Helper()
	activityID := ids.NewV7()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		// The ledger's owner is a foreign key: a dangling one would make this a
		// test about referential integrity instead of about the join.
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, workspace_id, email, display_name, status)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, $2, 'Member', 'active')
			ON CONFLICT (id) DO NOTHING`,
			owner, "member-"+owner.String()+"@example.test"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, channel_provider, occurred_at, source_system,
			                      source_id, source, captured_by, counterparty_email)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        CASE WHEN $4 = '' THEN 'note' ELSE 'message' END, NULLIF($4, ''),
			        now(), 'gmail', $2, 'gmail', 'connector:gmail', $3)`,
			activityID, sourceID, sender, channelProvider); err != nil {
			return err
		}
		if withLedger {
			if _, err := tx.Exec(ctx, `
				INSERT INTO capture_pending_counterparty
				       (workspace_id, email, domain, activity_id, owner_id, status, kind, resolved_at)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
				        $1, 'client.io', $2, $3, 'real', 'person', now())`,
				sender, activityID, owner); err != nil {
				return err
			}
		}
		return capture.Trace(ctx, tx, capture.TraceEntry{
			Stage:  pipelinetrace.StageTierLadder,
			UserID: owner, Connector: "gmail", SourceSystem: "gmail", SourceID: sourceID,
			Outcome: capture.TraceDeferred, ActivityID: activityID,
		}, false)
	}); err != nil {
		t.Fatalf("seeding a deferred message: %v", err)
	}
}

func TestAVerdictReachesEveryMessageFromThatSender(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	const sender = "stranger@client.io"

	// The first message raised the question; the ledger points at it. The
	// second and third are the same stranger writing again.
	seedDeferredMessage(memberCtx, t, db, me, "s-1", sender, true)
	seedDeferredMessage(memberCtx, t, db, me, "s-2", sender, false)
	seedDeferredMessage(memberCtx, t, db, me, "s-3", sender, false)

	window, err := store.ListMine(memberCtx, nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(window.Entries))
	}
	for _, entry := range window.Entries {
		if entry.Resolution == nil {
			t.Errorf("a message from a resolved sender still reads unresolved — one sender's answer covers all their mail")
			continue
		}
		if entry.Resolution.Status != "real" {
			t.Errorf("resolution = %q, want the ledger's answer", entry.Resolution.Status)
		}
	}
}

// The disposition ledger is the MAIL ladder's, keyed on an address. A direct
// message may carry that same address to be matched on, and it reaches the
// member's trace through the same activity column — so without a guard the
// member is told their captured, linked and answered conversation is waiting on
// a verdict that belongs to another medium and can never resolve for it.
//
// The mail row is the control: it must still carry the verdict, or the guard
// would be suppressing the ledger rather than scoping it.
func TestOnlyAMailRowCarriesTheMailVerdict(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	const sender = "both.media@client.io"

	seedDeferredRecord(memberCtx, t, db, me, "m-1", sender, "", true)
	seedDeferredRecord(memberCtx, t, db, me, "c-1", sender, "telegram", false)

	window, err := store.ListMine(memberCtx, nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}

	if len(window.Entries) != 2 {
		t.Fatalf("entries = %d, want the mail row and the channel row", len(window.Entries))
	}
	resolved := 0
	for _, entry := range window.Entries {
		if entry.Resolution != nil {
			resolved++
		}
	}
	if resolved != 1 {
		t.Errorf("%d of 2 rows carry the mail verdict, want exactly the mail one — a channel record has no ladder verdict to report, which is not the same as having one that is pending", resolved)
	}
}

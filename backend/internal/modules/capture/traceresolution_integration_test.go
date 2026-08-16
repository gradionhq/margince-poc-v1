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
	seedTracedRecord(ctx, t, db, owner, sourceID, sender, channelProvider,
		capture.TraceDeferred, withLedger)
}

// seedTracedRecord writes one activity, its trace row, and optionally the
// ledger's question about its sender.
//
// The OUTCOME is a parameter because it is what the read keys on, and the two
// channel shapes differ by exactly it: a record named by a channel identity
// takes decideChannelCounterparty, which opens no ledger question and so traces
// `captured`, while a record named by an address alone runs the mail ladder and
// can defer. Seeding a channel row as `deferred` would be a shape the sink
// cannot produce, which is how the guard this replaced came to refuse a verdict
// that had already landed.
func seedTracedRecord(ctx context.Context, t *testing.T, db *database.DB,
	owner ids.UUID, sourceID, sender, channelProvider string,
	outcome capture.TraceOutcome, withLedger bool,
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
		// The connector names the TRANSPORT, which is what the sink writes: a
		// channel record answers with its provider, mail with its source system.
		transport := "gmail"
		if channelProvider != "" {
			transport = channelProvider
		}
		return capture.Trace(ctx, tx, capture.TraceEntry{
			Stage:  pipelinetrace.StageTierLadder,
			UserID: owner, Connector: transport, SourceSystem: "gmail", SourceID: sourceID,
			Outcome: outcome, ActivityID: activityID,
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

// A channel record inherits no verdict it did not raise.
//
// The disposition ledger is the ladder's, keyed on an address. A direct message
// names its human by a channel identity and may carry that human's address only
// as corroboration; it opens no ledger question, so reporting one would tell a
// member their captured, linked and answered conversation is waiting on a
// verdict that can never resolve for it.
//
// The mail row is the control: it must still carry the verdict, or the guard
// would be suppressing the ledger rather than scoping it.
func TestACapturedChannelRecordInheritsNoMailVerdict(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	const sender = "both.media@client.io"

	seedDeferredRecord(memberCtx, t, db, me, "m-1", sender, "", true)
	seedTracedRecord(memberCtx, t, db, me, "c-1", sender, "telegram", capture.TraceCaptured, false)

	entries := entriesByConnector(memberCtx, t, store, 2)
	if entries["gmail"].Resolution == nil {
		t.Errorf("the mail row lost its verdict — the guard scopes the ledger, it does not suppress it")
	}
	if got := entries["telegram"].Resolution; got != nil {
		t.Errorf("a captured direct message reports %q, want no verdict — it opened no question", got.Status)
	}
}

// A channel message whose sender is named by an ADDRESS runs the mail ladder
// like any mail: the question it defers is its own, and the answer is its own
// to report.
//
// This is the case a guard on activity.channel_provider could not express.
// kind='message' forces that column non-null for every channel record, so
// guarding on it refused the address-named ones too — and since nothing ever
// clears a trace's verdict, they read "waiting on a verdict" for the whole
// 24-hour window after their verdict had landed.
func TestAnAddressNamedChannelMessageCarriesItsOwnVerdict(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	const sender = "mentioned@client.io"

	// The first mention raised the question; the second is the same sender
	// again, joining it.
	seedDeferredRecord(memberCtx, t, db, me, "x-1", sender, "telegram", true)
	seedDeferredRecord(memberCtx, t, db, me, "x-2", sender, "telegram", false)

	for _, entry := range traceEntries(memberCtx, t, store, 2) {
		if entry.Resolution == nil {
			t.Errorf("a mentioned sender's message still reads unresolved — the ladder deferred it, so the ladder's answer is its answer")
			continue
		}
		if entry.Resolution.Status != "real" {
			t.Errorf("resolution = %q, want the ledger's answer", entry.Resolution.Status)
		}
	}
}

// traceEntries reads the member's window and insists on the row count the test
// seeded, so a later assertion is about the rows it means.
func traceEntries(ctx context.Context, t *testing.T, store *capture.TraceStore, want int) []capture.TraceRow {
	t.Helper()
	window, err := store.ListMine(ctx, nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != want {
		t.Fatalf("entries = %d, want %d", len(window.Entries), want)
	}
	return window.Entries
}

// entriesByConnector keys the window by the transport each row names, which is
// how a test tells two seeded rows apart: the read returns no source id, and
// the connector is the field the seed varies.
func entriesByConnector(ctx context.Context, t *testing.T, store *capture.TraceStore, want int) map[string]capture.TraceRow {
	t.Helper()
	out := make(map[string]capture.TraceRow, want)
	for _, entry := range traceEntries(ctx, t, store, want) {
		out[entry.Connector] = entry
	}
	if len(out) != want {
		t.Fatalf("connectors = %d over %d rows, want one per row", len(out), want)
	}
	return out
}

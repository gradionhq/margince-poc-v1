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

// seededRecord names one message to seed, as the sink would have written it.
//
// The OUTCOME is part of the shape because it is what the read keys on, and the
// two channel shapes differ by exactly it: a record named by a channel identity
// takes decideChannelCounterparty, which opens no ledger question and so traces
// `captured`, while a record named by an address alone runs the mail ladder and
// can defer. So a channel row seeded `deferred` is a mention and one seeded
// `captured` is a direct message — the sink produces no other pairing, which
// TestChannelRecordSkipsEveryMailDomainGate holds through the real writer.
type seededRecord struct {
	// SourceID is the message's natural key half, and the test's handle on it.
	SourceID string
	Sender   string
	// Transport is the channel provider, or empty for mail. It decides the
	// activity's kind, its source system and the trace's connector together —
	// a telegram-carried message with gmail's source system is a row no
	// connector writes, and this seed exists to write rows they do.
	Transport string
	Outcome   capture.TraceOutcome
	// Ledger writes the disposition this message's sender raised. Only the
	// first message from an address carries one: the ledger keeps one open
	// question per address, and later messages join it.
	Ledger bool
	// Verdict is the status that disposition settled at, defaulting to `real`.
	// A T2 suppression records its own answer rather than a judged one, so the
	// two ladder outcomes that reach the join do not share a status.
	Verdict string
}

// verdict is the disposition status to seed, defaulting to a judged sender.
func (r seededRecord) verdict() string {
	if r.Verdict == "" {
		return "real"
	}
	return r.Verdict
}

// sourceSystem is the system the connector for this transport would name.
func (r seededRecord) sourceSystem() string {
	if r.Transport == "" {
		return "gmail"
	}
	return r.Transport
}

// seedDeferredMessage writes a MAIL activity from one sender, a trace row for
// it, and (on the first call for that address) the ledger's open question.
func seedDeferredMessage(ctx context.Context, t *testing.T, db *database.DB,
	owner ids.UUID, sourceID, sender string, withLedger bool,
) {
	t.Helper()
	seedRecord(ctx, t, db, owner, seededRecord{
		SourceID: sourceID, Sender: sender,
		Outcome: capture.TraceDeferred, Ledger: withLedger,
	})
}

// seedRecord writes one activity, its trace row, and optionally the ledger's
// question about its sender.
func seedRecord(ctx context.Context, t *testing.T, db *database.DB,
	owner ids.UUID, rec seededRecord,
) {
	t.Helper()
	sourceID, sender, channelProvider := rec.SourceID, rec.Sender, rec.Transport
	system := rec.sourceSystem()
	activityID := ids.NewV7()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		// The ledger's owner is a foreign key: a dangling one would make this a
		// test about referential integrity instead of about the join.
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, email, display_name, status)
			VALUES ($1, $2, 'Member', 'active')
			ON CONFLICT (id) DO NOTHING`, owner, "member-"+owner.String()+"@example.test"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, kind, channel_provider, occurred_at, source_system, source_id, source, captured_by, counterparty_email)
			VALUES ($1,
			        CASE WHEN $4 = '' THEN 'note' ELSE 'message' END, NULLIF($4, ''),
			        now(), $5, $2, $5, 'connector:' || $5, $3)`,
			activityID, sourceID, sender, channelProvider, system); err != nil {
			return err
		}
		if rec.Ledger {
			if _, err := tx.Exec(ctx, `
				INSERT INTO capture_pending_counterparty
				       (email, domain, activity_id, owner_id, status, kind, resolved_at)
				VALUES ($1, 'client.io', $2, $3, $4, 'person', now())`,
				sender, activityID, owner, rec.verdict()); err != nil {
				return err
			}
		}
		// The connector names the TRANSPORT, which is what the sink writes: a
		// channel record answers with its provider, mail with its source system.
		return capture.Trace(ctx, tx, capture.TraceEntry{
			Stage:  pipelinetrace.StageTierLadder,
			UserID: owner, Connector: system, SourceSystem: system, SourceID: sourceID,
			Outcome: rec.Outcome, ActivityID: activityID,
		}, false)
	}); err != nil {
		t.Fatalf("seeding a traced record: %v", err)
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

	seedRecord(memberCtx, t, db, me, seededRecord{
		SourceID: "m-1", Sender: sender, Outcome: capture.TraceDeferred, Ledger: true,
	})
	seedRecord(memberCtx, t, db, me, seededRecord{
		SourceID: "c-1", Sender: sender, Transport: "telegram", Outcome: capture.TraceCaptured,
	})

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
// The transport cannot express this. kind='message' forces channel_provider
// non-null for every channel record, so it says only "this arrived on a
// channel" — never which ladder decided the record. A read that keys on it
// leaves these messages claiming to wait for an answer they already have, for
// as long as the window keeps them.
func TestAnAddressNamedChannelMessageCarriesItsOwnVerdict(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	const sender = "mentioned@client.io"

	// The first mention raised the question; the second is the same sender
	// again, joining it.
	seedRecord(memberCtx, t, db, me, seededRecord{
		SourceID: "x-1", Sender: sender, Transport: "telegram",
		Outcome: capture.TraceDeferred, Ledger: true,
	})
	seedRecord(memberCtx, t, db, me, seededRecord{
		SourceID: "x-2", Sender: sender, Transport: "telegram",
		Outcome: capture.TraceDeferred,
	})

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

// The OTHER ladder outcome that records a disposition reports it too.
//
// `deferred` and `suppressed` are both in LadderDispositionOutcomes, and the
// join admits both. Only one of them being exercised would leave half the
// predicate resting on the other half's test — and the two arms differ in more
// than a string: a T2 suppression settles at `suppressed` rather than at a
// judged verdict, so a read that reached only the judged statuses would look
// correct here and report nothing in production.
func TestAnAddressNamedChannelSuppressionCarriesItsOwnDisposition(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	const sender = "noreply@sendgrid.test"

	seedRecord(memberCtx, t, db, me, seededRecord{
		SourceID: "s-1", Sender: sender, Transport: "telegram",
		Outcome: capture.TraceSuppressed, Ledger: true, Verdict: "suppressed",
	})

	entries := traceEntries(memberCtx, t, store, 1)
	if entries[0].Resolution == nil {
		t.Fatal("a suppressed mention reports no disposition — the registry recorded one against this address")
	}
	if got := entries[0].Resolution.Status; got != "suppressed" {
		t.Errorf("resolution = %q, want the suppression the registry recorded", got)
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

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The replay pass over stored originals (ADR-0078 / ACT-DDL-3).
//
// Its whole correctness argument is TERMINATION. The two-end backfill needs no
// state because its predicate shrinks as it runs; this one cannot borrow that,
// because most messages have no CCs and the result therefore cannot say
// whether an original was ever re-read. The marker is what makes the
// run-until-zero loop finish, so every test here is about the marker.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// replayCtx binds the workspace and the system principal the pass runs under.
func replayCtx(e *integration.Env) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:participant_backfill",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}

// seedReplayableMail records an activity and the stored original behind it, the
// way capture would have.
func seedReplayableMail(t *testing.T, e *integration.Env, sourceID, raw string) ids.UUID {
	t.Helper()
	owner := integration.OwnerConn(t)
	id := integration.SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, direction, source_system, source_id, source, captured_by)
		VALUES ($1, $2, 'email', 'Q3 terms', '2026-08-01T09:00:00Z', 'inbound',
		        'gmail', '`+sourceID+`', 'gmail:`+sourceID+`', 'connector:gmail')`, e.WS)
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO raw_capture (workspace_id, source_system, source_id, payload)
		VALUES ($1, 'gmail', $2, to_jsonb($3::text))`, e.WS, sourceID, raw); err != nil {
		t.Fatalf("seeding the stored original: %v", err)
	}
	return id
}

// replayOutcome reads back what the pass recorded about one activity.
func replayOutcome(t *testing.T, activity ids.UUID) (string, bool) {
	t.Helper()
	var outcome string
	err := integration.OwnerConn(t).QueryRow(context.Background(),
		`SELECT outcome FROM activity_participant_replay WHERE activity_id = $1`, activity).Scan(&outcome)
	if err != nil {
		return "", false
	}
	return outcome, true
}

const ccMessage = "From: bob@target.example\r\n" +
	"To: owner@myco.example\r\n" +
	"Cc: sam@target.example\r\n" +
	"Subject: Q3 terms\r\n" +
	"Message-ID: <m1@target.example>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nBody.\r\n"

// Without a connection there is no mailbox to parse against, and guessing one
// would file the owner as a participant of their own conversation. The pass
// records that verdict rather than skipping, or it would re-select the same
// row on every run and the loop would never finish.
func TestReplayRecordsNoOwnerWhenTheMailboxIsUnknown(t *testing.T) {
	e := integration.Setup(t)
	activity := seedReplayableMail(t, e, "msg-no-owner", ccMessage)

	settled, err := replayParticipantsBatch(replayCtx(e), e.Pool, 10, slog.Default())
	if err != nil {
		t.Fatalf("replayParticipantsBatch: %v", err)
	}
	if settled == 0 {
		t.Fatal("the pass settled nothing, so the same row is selected forever")
	}
	outcome, found := replayOutcome(t, activity)
	if !found {
		t.Fatal("no marker was written; the run-until-zero loop cannot terminate")
	}
	if outcome != replayNoOwner {
		t.Errorf("outcome = %q, want %q", outcome, replayNoOwner)
	}
}

// The pass is idempotent by construction: a settled activity is never selected
// again, so a second run finds nothing to do.
func TestReplaySettlesEachActivityExactlyOnce(t *testing.T) {
	e := integration.Setup(t)
	seedReplayableMail(t, e, "msg-once", ccMessage)
	ctx := replayCtx(e)

	first, err := replayParticipantsBatch(ctx, e.Pool, 10, slog.Default())
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first != 1 {
		t.Fatalf("first pass settled %d, want 1", first)
	}
	second, err := replayParticipantsBatch(ctx, e.Pool, 10, slog.Default())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second != 0 {
		t.Errorf("second pass settled %d — the marker did not stop the re-selection", second)
	}
}

// A payload this parser cannot decompose is a VERDICT, not a fault. Failing
// the batch on one years-old original would stop the pass reaching every
// message after it.
func TestReplayRecordsUnreadableRatherThanFailingTheBatch(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	bad := seedReplayableMail(t, e, "msg-unreadable", "not an RFC822 message at all")
	good := seedReplayableMail(t, e, "msg-readable", ccMessage)

	// A connection makes the mailbox known, so the parse is actually attempted.
	integration.SeedRow(t, owner, `INSERT INTO capture_connection
		(id, workspace_id, provider, user_id, scopes, status, auth, account_label)
		VALUES ($1, $2, 'gmail', '`+e.Rep1.String()+`', '{}', 'connected', ''::bytea,
		        'owner@myco.example')`, e.WS)

	settled, err := replayParticipantsBatch(replayCtx(e), e.Pool, 10, slog.Default())
	if err != nil {
		t.Fatalf("replayParticipantsBatch: %v", err)
	}
	if settled != 2 {
		t.Errorf("settled = %d, want both activities — one bad payload must not stop the pass", settled)
	}
	if _, found := replayOutcome(t, bad); !found {
		t.Error("the unreadable original was left unmarked and will be re-parsed forever")
	}
	outcome, found := replayOutcome(t, good)
	if !found {
		t.Fatal("the readable original after the bad one was never reached")
	}
	if outcome != replayWroteParticipants {
		t.Errorf("outcome = %q, want %q — the message carries a Cc", outcome, replayWroteParticipants)
	}

	// And the recovered party is actually on the row.
	var parties int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM activity_participant WHERE activity_id = $1 AND address = 'sam@target.example'`,
		good).Scan(&parties); err != nil {
		t.Fatalf("counting the recovered party: %v", err)
	}
	if parties != 1 {
		t.Errorf("the Cc was recorded %d times, want once", parties)
	}
}

// The batch limit bounds one transaction. A pass that ignored it would hold a
// write transaction over an entire mailbox's history.
func TestReplayHonorsItsBatchLimit(t *testing.T) {
	e := integration.Setup(t)
	for _, id := range []string{"msg-a", "msg-b", "msg-c"} {
		seedReplayableMail(t, e, id, ccMessage)
	}
	settled, err := replayParticipantsBatch(replayCtx(e), e.Pool, 2, slog.Default())
	if err != nil {
		t.Fatalf("replayParticipantsBatch: %v", err)
	}
	if settled != 2 {
		t.Errorf("settled = %d with a limit of 2", settled)
	}
}

// A non-positive limit is a caller error, and answering it with "nothing to do"
// would make a broken loop look like a finished one.
func TestReplayRefusesANonPositiveLimit(t *testing.T) {
	e := integration.Setup(t)
	if _, err := replayParticipantsBatch(replayCtx(e), e.Pool, 0, slog.Default()); err == nil {
		t.Error("a zero batch limit was accepted; a caller looping on it would spin forever")
	}
}

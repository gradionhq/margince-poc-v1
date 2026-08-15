// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The trace write, proven against the real table rather than a mock's
// bookkeeping — because everything worth asserting here is a property of the
// schema: the expression index that makes a replay free, the CHECKs that bound
// what a remote party can store, and the NULLs that carry the access-control
// axis.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// traceWorkspace binds a workspace the trace writes run in.
func traceWorkspace(t *testing.T) (context.Context, *database.DB) {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`, ws, "trace-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	ctx = principal.WithWorkspaceID(ctx, ws)
	return ctx, database.BindTo(pool, ids.From[ids.WorkspaceKind](ws))
}

// writeTrace runs one Trace on its own transaction and fails the test on error.
func writeTrace(ctx context.Context, t *testing.T, db *database.DB, in capture.TraceEntry, payloads bool) {
	t.Helper()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return capture.Trace(ctx, tx, in, payloads)
	}); err != nil {
		t.Fatalf("Trace(%s): %v", in.Outcome, err)
	}
}

// traceRows counts the rows recorded for one source id.
func traceRows(ctx context.Context, t *testing.T, db *database.DB, sourceID string) int {
	t.Helper()
	var n int
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM capture_trace WHERE source_id = $1`, sourceID).Scan(&n)
	}); err != nil {
		t.Fatalf("counting traces: %v", err)
	}
	return n
}

func mailTrace(sourceID string, outcome capture.TraceOutcome) capture.TraceEntry {
	return capture.TraceEntry{
		UserID: ids.NewV7(), Connector: "gmail", SourceSystem: "gmail",
		SourceID: sourceID, Outcome: outcome,
	}
}

// A re-walked region replays the same decision. The funnel must count messages,
// not polls — which is what the expression index buys, and which only holds if
// the ON CONFLICT target spells that same expression.
func TestAReplayedDecisionRecordsOneRow(t *testing.T) {
	ctx, db := traceWorkspace(t)
	entry := mailTrace("m-replay", capture.TraceInternal)
	entry.Reason = "internal_only"

	writeTrace(ctx, t, db, entry, false)
	writeTrace(ctx, t, db, entry, false)

	if got := traceRows(ctx, t, db, "m-replay"); got != 1 {
		t.Errorf("rows after a replayed decision = %d, want 1", got)
	}
}

// The same provider message reaching two connected mailboxes is two members'
// business, and each must see their own. A natural key without user_id lets the
// first row swallow the second, and the second member's own view then omits
// their own message — the single promise this table makes.
func TestTwoMembersEachKeepTheirOwnRowForOneMessage(t *testing.T) {
	ctx, db := traceWorkspace(t)
	first, second := mailTrace("m-shared", capture.TraceCaptured), mailTrace("m-shared", capture.TraceCaptured)

	writeTrace(ctx, t, db, first, false)
	writeTrace(ctx, t, db, second, false)

	if got := traceRows(ctx, t, db, "m-shared"); got != 2 {
		t.Errorf("rows for one message in two mailboxes = %d, want 2 (one per member)", got)
	}
}

// A workspace-owned connection has no member, and NULL is what the workspace
// read selects on. Two of them must still dedupe, which a bare unique index
// over a nullable column would not do.
func TestWorkspaceOwnedRowsCarryNoMemberAndStillDedupe(t *testing.T) {
	ctx, db := traceWorkspace(t)
	entry := capture.TraceEntry{
		Connector: "telegram", SourceSystem: "telegram", SourceID: "chat-1:42",
		Outcome: capture.TraceCaptured, ChannelIdentity: true,
	}

	writeTrace(ctx, t, db, entry, false)
	writeTrace(ctx, t, db, entry, false)

	var rows int
	var userID *ids.UUID
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM capture_trace WHERE connector = 'telegram'`).Scan(&rows); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT user_id FROM capture_trace WHERE connector = 'telegram'`).Scan(&userID)
	}); err != nil {
		t.Fatalf("reading the workspace row: %v", err)
	}
	if rows != 1 {
		t.Errorf("workspace-owned rows for one message = %d, want 1 — a NULL never equals a NULL, so the index must COALESCE", rows)
	}
	if userID != nil {
		t.Errorf("user_id = %v, want NULL — NULL is what makes this row a manager's to read", userID)
	}
}

// A channel record's source id is the customer's account id. It must not be
// stored, and dedupe must still work without it.
func TestAChannelAccountIdIsHashedNeverStored(t *testing.T) {
	ctx, db := traceWorkspace(t)
	const accountID = "chat-77:9001"
	writeTrace(ctx, t, db, capture.TraceEntry{
		Connector: "telegram", SourceSystem: "telegram", SourceID: accountID,
		Outcome: capture.TraceCaptured, ChannelIdentity: true,
	}, false)

	var stored string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT source_id FROM capture_trace WHERE connector = 'telegram'`).Scan(&stored)
	}); err != nil {
		t.Fatalf("reading the stored key: %v", err)
	}
	if strings.Contains(stored, accountID) {
		t.Errorf("stored source_id = %q, want the account id absent — an erasure inside the window cannot reach it here", stored)
	}
	if !strings.HasPrefix(stored, "sha256:") {
		t.Errorf("stored source_id = %q, want a sha256: digest", stored)
	}
}

// Mail keeps its message id: ADR-0082 permits it, and it is what makes a
// support question answerable.
func TestAMailMessageIdIsKept(t *testing.T) {
	ctx, db := traceWorkspace(t)
	writeTrace(ctx, t, db, mailTrace("<abc@mail.example>", capture.TraceCaptured), false)

	var stored string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT source_id FROM capture_trace WHERE connector = 'gmail'`).Scan(&stored)
	}); err != nil {
		t.Fatalf("reading the stored key: %v", err)
	}
	if stored != "<abc@mail.example>" {
		t.Errorf("stored source_id = %q, want the message id kept verbatim", stored)
	}
}

// The default posture stores no content at all. Not masked, not redacted —
// never written, because a column that is never populated cannot leak.
func TestWithPayloadsOffNoContentIsStored(t *testing.T) {
	ctx, db := traceWorkspace(t)
	entry := mailTrace("m-nocontent", capture.TraceInternal)
	entry.Counterparty, entry.Subject = "colleague@acme.com", "Meeting recap"
	writeTrace(ctx, t, db, entry, false)

	var counterparty, subject *string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT counterparty, subject FROM capture_trace WHERE source_id = 'm-nocontent'`).
			Scan(&counterparty, &subject)
	}); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if counterparty != nil || subject != nil {
		t.Errorf("stored counterparty=%v subject=%v, want both NULL by default", counterparty, subject)
	}
}

// With the operator's posture on, content is stored — bounded, and normalized
// so the erasure predicate is index-backed equality rather than a scan.
func TestWithPayloadsOnContentIsBoundedAndNormalized(t *testing.T) {
	ctx, db := traceWorkspace(t)
	entry := mailTrace("m-content", capture.TraceInternal)
	entry.Counterparty = "  Colleague@ACME.com  "
	entry.Subject = strings.Repeat("é", capture.MaxCapturedSubjectChars+40)
	writeTrace(ctx, t, db, entry, true)

	var counterparty, subject string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT counterparty, subject FROM capture_trace WHERE source_id = 'm-content'`).
			Scan(&counterparty, &subject)
	}); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if counterparty != "colleague@acme.com" {
		t.Errorf("stored counterparty = %q, want it folded and trimmed", counterparty)
	}
	if got := len([]rune(subject)); got != capture.MaxCapturedSubjectChars {
		t.Errorf("stored subject = %d runes, want it clamped to %d — a remote party does not choose how much this stores",
			got, capture.MaxCapturedSubjectChars)
	}
}

// An entry with no natural key can never be read back or deduped. It is a
// programming error at a call site, and it fails loudly rather than writing a
// row nothing can find.
func TestATraceWithoutANaturalKeyIsRefused(t *testing.T) {
	ctx, db := traceWorkspace(t)
	err := db.Tx(ctx, func(tx pgx.Tx) error {
		return capture.Trace(ctx, tx, capture.TraceEntry{
			Connector: "gmail", SourceSystem: "gmail", Outcome: capture.TraceCaptured,
		}, false)
	})
	if err == nil {
		t.Fatal("Trace with no source id returned nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "natural key") {
		t.Errorf("error = %q, want it to name the missing natural key", err)
	}
}

// Deletion sticks at the WRITE, not only in the erasure sweep. recordDisposition
// already refuses to re-materialize an erased address in the ledger; a
// diagnostic table in payload mode is exactly where it would otherwise come
// back, so the trace asks the same list.
func TestAnErasedAddressIsNeverWrittenEvenWithPayloadsOn(t *testing.T) {
	ctx, db := traceWorkspace(t)
	const erased = "gone@client.io"
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		// Seeded through storekit's own hashing rule, not a literal: writer and
		// reader must normalize identically or a stray space resurrects an
		// erased subject, which is the bug this table exists to prevent.
		// The columns the erasure engine itself writes (erasuretimeline.go): the
		// table carries no tenant since the privacy sweep dropped it, so a
		// fixture spelling one is asserting a schema that no longer exists.
		_, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (kind, value_hash)
			VALUES ('email', $1)`, storekit.SuppressionHash(erased))
		return err
	}); err != nil {
		t.Fatalf("seeding the suppression list: %v", err)
	}

	entry := mailTrace("m-erased", capture.TraceCaptured)
	entry.Counterparty, entry.Subject = erased, "Please delete my data"
	writeTrace(ctx, t, db, entry, true)

	var counterparty, subject *string
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT counterparty, subject FROM capture_trace WHERE source_id = 'm-erased'`).
			Scan(&counterparty, &subject)
	}); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	// The decision is still traced — the member is owed the answer that their
	// message was handled — but with no trace of who.
	if counterparty != nil {
		t.Errorf("stored counterparty = %q for an erased subject, want NULL", *counterparty)
	}
	if subject != nil {
		t.Errorf("stored subject = %q for an erased subject, want NULL", *subject)
	}
}

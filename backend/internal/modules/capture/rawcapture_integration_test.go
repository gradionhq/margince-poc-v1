// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// InsertRawCaptureTx over a real migrated Postgres: the two invariants under
// test are both transactional (a redelivery's UPSERT and a rollback's
// visibility), neither of which a mock connection can prove — a mock only
// proves the mock's own bookkeeping. setupCaptureDB is the shared fixture
// registry_disconnect_integration_test.go already establishes for this
// package; this file only adds its own minimal workspace bootstrap.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// bootstrapRawCaptureWorkspace seeds a fresh workspace and returns a context
// carrying it, so this file's two tests never collide with each other or
// with the other fixtures in this package.
func bootstrapRawCaptureWorkspace(t *testing.T) context.Context {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	ctx := context.Background()

	wsUUID := ids.NewV7()
	slug := "raw-capture-" + wsUUID.String()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		wsUUID, slug); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	return principal.WithWorkspaceID(ctx, wsUUID)
}

// A redelivered update_id must refresh the stored original, never duplicate
// it: raw_capture_source_unique's ON CONFLICT arm is DO UPDATE, matching the
// design's "a replay refreshes, never duplicates" rule. Collapsing this into
// DO NOTHING would silently keep serving a stale provider payload forever.
func TestInsertRawCaptureTxIsIdempotentOnUpdateID(t *testing.T) {
	ctx := bootstrapRawCaptureWorkspace(t)
	_, pool := setupCaptureDB(t)

	var firstID, secondID ids.UUID
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		firstID, err = capture.InsertRawCaptureTx(ctx, tx, capture.RawRecord{
			SourceSystem: "telegram",
			SourceID:     "update-1",
			Payload:      []byte(`{"update_id":1,"message":{"text":"first delivery"}}`),
		})
		return err
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		secondID, err = capture.InsertRawCaptureTx(ctx, tx, capture.RawRecord{
			SourceSystem: "telegram",
			SourceID:     "update-1",
			Payload:      []byte(`{"update_id":1,"message":{"text":"redelivered"}}`),
		})
		return err
	}); err != nil {
		t.Fatalf("redelivery insert: %v", err)
	}

	if secondID != firstID {
		t.Fatalf("redelivery minted a new row id %v, want the original %v refreshed in place", secondID, firstID)
	}

	var count int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND source_id = 'update-1'`,
		).Scan(&count)
	}); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1 — a redelivery must refresh, not duplicate", count)
	}

	// jsonb round-trips through Postgres's own key ordering, so the
	// assertion checks content rather than a byte-exact literal.
	var text string
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT payload->'message'->>'text' FROM raw_capture WHERE source_system = 'telegram' AND source_id = 'update-1'`,
		).Scan(&text)
	}); err != nil {
		t.Fatalf("reading back the payload: %v", err)
	}
	if text != "redelivered" {
		t.Fatalf("payload message text = %q, want %q — the stored original was not refreshed", text, "redelivered")
	}
}

// InsertRawCaptureTx must join the caller's transaction rather than opening
// its own: this is the entire reason the seam exists (design §6.2's webhook
// commits the raw row and its enqueue together or not at all). If it opened
// its own transaction, rolling back the caller's would still leave the raw
// row behind.
func TestInsertRawCaptureTxJoinsTheCallersTransaction(t *testing.T) {
	ctx := bootstrapRawCaptureWorkspace(t)
	_, pool := setupCaptureDB(t)

	rollbackErr := &intentionalRollback{}
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := capture.InsertRawCaptureTx(ctx, tx, capture.RawRecord{
			SourceSystem: "telegram",
			SourceID:     "update-rollback",
			Payload:      []byte(`{"update_id":2}`),
		}); err != nil {
			return err
		}
		// Force the caller's transaction to roll back AFTER the insert has
		// run, so a row that survived would prove the function opened its
		// own transaction instead of joining this one.
		return rollbackErr
	})
	if err != rollbackErr {
		t.Fatalf("WithWorkspaceTx returned %v, want the forced rollback error", err)
	}

	var count int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND source_id = 'update-rollback'`,
		).Scan(&count)
	}); err != nil {
		t.Fatalf("reading back after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("row count = %d, want 0 — the insert survived the caller's rollback, so it ran on its own transaction", count)
	}
}

// intentionalRollback is a distinct error identity, so the test can confirm
// WithWorkspaceTx propagated exactly the forced failure rather than some
// other fault masking the rollback.
type intentionalRollback struct{}

func (*intentionalRollback) Error() string {
	return "intentional rollback for the transaction-join test"
}

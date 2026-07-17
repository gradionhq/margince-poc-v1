// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migrations

// 0078's data transforms proven directly (CAP-DDL-2): a database that held
// improvised connector_connection rows migrates to capture_connection with the
// status vocabulary remapped (active→connected, revoked→disconnected,
// error→error) and the bytea cursor re-typed to jsonb without losing its
// (already-JSON) contents — plus the new provider/status CHECKs and the
// per-user unique key.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
)

func TestCaptureConnectionReconcile(t *testing.T) {
	ownerDSN, _ := dsns(t)
	conn := connect(t, ownerDSN)
	resetSchema(t, conn)
	ctx := context.Background()

	core := rewindToBeforeReconcile(t, conn)
	ws := seedWorkspace(t, conn, "pre-reconcile")
	var userID string
	if err := conn.QueryRow(ctx, `
		INSERT INTO app_user (workspace_id, email, display_name)
		VALUES ($1, 'rep@pre-reconcile.test', 'Rep') RETURNING id`, ws).Scan(&userID); err != nil {
		t.Fatalf("seeding app_user: %v", err)
	}

	// Old-shape rows across all three legacy statuses; the gmail cursor is the
	// connector's real JSON watermark stored as bytea.
	gmail := seedLegacyConnection(t, conn, ws, userID, "gmail", "active", []byte(`{"history_id":"42"}`))
	imap := seedLegacyConnection(t, conn, ws, userID, "imap", "revoked", nil)
	gcal := seedLegacyConnection(t, conn, ws, userID, "gcal", "error", nil)

	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("re-applying 0078 over old-shape rows: %v", err)
	}

	// Status remapped, and the gmail cursor survived the bytea→jsonb re-type.
	assertCaptureStatus(t, conn, ws, gmail, "connected")
	assertCaptureStatus(t, conn, ws, imap, "disconnected")
	assertCaptureStatus(t, conn, ws, gcal, "error")
	assertGmailCursorRoundTripped(t, conn, ws, gmail, imap)
	assertReconciledChecksAndUnique(t, conn, ws, gmail, userID)
}

// rewindToBeforeReconcile migrates fully, then reverses back to just before
// 0078 so seeded rows land in the OLD (connector_connection) schema — exactly
// like a production database that improvised the table before the spec ratified
// it. Returns the loaded core namespace for the re-apply.
func rewindToBeforeReconcile(t *testing.T, conn *pgx.Conn) dbmigrate.Namespace {
	t.Helper()
	ctx := context.Background()
	core, err := Core()
	if err != nil {
		t.Fatalf("loading core: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, conn, core); err != nil {
		t.Fatalf("up: %v", err)
	}
	idx := -1
	for i, m := range core.Migrations {
		if m.Version == "0078" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("core migrations contain no 0078 — the capture_connection reconcile migration is missing")
	}
	if _, err := dbmigrate.Down(ctx, conn, core, len(core.Migrations)-idx); err != nil {
		t.Fatalf("down to pre-0078: %v", err)
	}
	return core
}

func seedLegacyConnection(t *testing.T, conn *pgx.Conn, ws, userID, connector, status string, cursor []byte) string {
	t.Helper()
	var id string
	if err := withGUC(t, conn, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO connector_connection (workspace_id, connector, granted_by, scopes, status, cursor)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5)
			RETURNING id`,
			connector, userID, []string{"read"}, status, cursor).Scan(&id)
	}); err != nil {
		t.Fatalf("seeding old-shape %s row: %v", connector, err)
	}
	return id
}

func assertCaptureStatus(t *testing.T, conn *pgx.Conn, ws, id, want string) {
	t.Helper()
	var status string
	if err := withGUC(t, conn, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status FROM capture_connection WHERE id = $1`, id).Scan(&status)
	}); err != nil {
		t.Fatalf("reading status of %s: %v", id, err)
	}
	if status != want {
		t.Errorf("status of %s = %q, want %q", id, status, want)
	}
}

func assertGmailCursorRoundTripped(t *testing.T, conn *pgx.Conn, ws, gmailID, imapID string) {
	t.Helper()
	var historyID, imapCursor *string
	if err := withGUC(t, conn, ws, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT sync_cursor->>'history_id' FROM capture_connection WHERE id = $1`, gmailID).Scan(&historyID); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT sync_cursor::text FROM capture_connection WHERE id = $1`, imapID).Scan(&imapCursor)
	}); err != nil {
		t.Fatalf("reading migrated cursors: %v", err)
	}
	if historyID == nil || *historyID != "42" {
		t.Errorf("gmail sync_cursor history_id = %v, want '42' (jsonb round-trip lost the watermark)", historyID)
	}
	if imapCursor != nil {
		t.Errorf("imap sync_cursor = %v, want NULL (a null bytea cursor must stay null)", imapCursor)
	}
}

func assertReconciledChecksAndUnique(t *testing.T, conn *pgx.Conn, ws, gmailID, userID string) {
	t.Helper()
	exec := func(sql string, args ...any) error {
		return withGUC(t, conn, ws, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql, args...)
			return err
		})
	}
	if err := exec(`UPDATE capture_connection SET status = 'bogus' WHERE id = $1`, gmailID); err == nil {
		t.Error("status outside the CAP-DDL-2 set was accepted; the CHECK must reject it")
	}
	if err := exec(`UPDATE capture_connection SET provider = 'salesforce' WHERE id = $1`, gmailID); err == nil {
		t.Error("a non-capture provider was accepted; the provider CHECK must reject it")
	}
	if err := exec(`
		INSERT INTO capture_connection (workspace_id, user_id, provider, scopes)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'gmail', '{}')`, userID); err == nil {
		t.Error("a duplicate (workspace, user, provider) row was accepted; the UNIQUE must reject it")
	}
}

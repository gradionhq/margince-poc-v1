// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// getRaw itself (and its backing SQL) now lives in mirrorstore.go: it has
// a genuine production caller (reconcile.go's Reconcile), not just this
// file's fixtures, so it no longer belongs test-only.

// TestIngestHonorsStalenessAndTombstone drives the three in-SQL guards
// design.md §4.4/§4.9 puts INSIDE the upsert statement rather than as an
// app-level read-compare-write (which two concurrent sweeps could
// race): a newer incumbent read updates the mirror; an
// older one is silently ignored, never clobbering a fresher row; and an
// erased (tombstoned) external_id is never re-created by a later ingest,
// however fresh its timestamp. Reads use the package-internal getRaw,
// which bypasses the mirror_visibility deny-join — this test seeds no
// visibility rows, so a visibility-joined read would find nothing for
// reasons unrelated to what this test is proving.
func TestIngestHonorsStalenessAndTombstone(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	const objectClass = "contact"
	const externalID = "100214862042"

	baseline := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:          map[string]any{"firstname": "Christian"},
		ModifiedAt:      baseline,
		OwnerExternalID: "1197833249",
	}); err != nil {
		t.Fatalf("initial ingest: %v", err)
	}

	row, err := store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after initial ingest: %v", err)
	}
	if row.Fields["firstname"] != "Christian" || !row.UpdatedAtBaseline.Equal(baseline) {
		t.Fatalf("initial ingest did not land: %+v", row)
	}

	// (a) A NEWER version updates the row.
	newer := baseline.Add(24 * time.Hour)
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:          map[string]any{"firstname": "Christoph"},
		ModifiedAt:      newer,
		OwnerExternalID: "1197833249",
	}); err != nil {
		t.Fatalf("newer ingest: %v", err)
	}
	row, err = store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after newer ingest: %v", err)
	}
	if row.Fields["firstname"] != "Christoph" || !row.UpdatedAtBaseline.Equal(newer) {
		t.Fatalf("a newer updated_at_baseline must win: got %+v", row)
	}

	// (b) An OLDER version is ignored — no clobbering a fresher row with
	// a stale poller page racing behind a fresher read of the same record.
	older := baseline.Add(-24 * time.Hour)
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:          map[string]any{"firstname": "Stale"},
		ModifiedAt:      older,
		OwnerExternalID: "1197833249",
	}); err != nil {
		t.Fatalf("older ingest: %v", err)
	}
	row, err = store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after older ingest: %v", err)
	}
	if row.Fields["firstname"] != "Christoph" || !row.UpdatedAtBaseline.Equal(newer) {
		t.Fatalf("an older updated_at_baseline must be ignored, not clobber the fresher row: got %+v", row)
	}

	// (c) A tombstoned external_id is NOT (re)created by ingest, however
	// fresh the incoming version claims to be.
	const tombstoned = "999888777666"
	if err := seedTombstone(ctx, pool, objectClass, tombstoned); err != nil {
		t.Fatalf("seeding the tombstone fixture: %v", err)
	}

	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: tombstoned,
		Fields:     map[string]any{"firstname": "Resurrected"},
		ModifiedAt: newer.Add(time.Hour),
	}); err != nil {
		t.Fatalf("ingest of a tombstoned id: %v", err)
	}
	if _, err := store.getRaw(ctx, objectClass, tombstoned); err == nil {
		t.Fatal("a tombstoned external_id must not be (re)created by ingest, but getRaw found a row")
	}
}

// seedTombstone inserts the fixture the tombstone-guard test asserts
// against, through the same tenant-scoped transaction helper the store
// itself uses — the fixture must be workspace-visible to the guard's own
// NOT EXISTS check, which reads app.workspace_id off the GUC.
func seedTombstone(ctx context.Context, pool *pgxpool.Pool, objectClass, externalID string) error {
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO overlay_tombstone (workspace_id, object_class, external_id)
			VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2)`,
			objectClass, externalID)
		return err
	})
}

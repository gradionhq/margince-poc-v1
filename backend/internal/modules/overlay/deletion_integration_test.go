// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestPurgeRecordRemovesMirrorAssociationAndVisibility proves the
// per-record removal a deletion sweep performs when the incumbent
// reports a record deleted (branch-1b deletion feed): the mirror row,
// every association edge that names it on EITHER endpoint, and its
// visibility projection all go in one transaction — so a HubSpot-side
// deletion stops being readable rather than lingering until disconnect.
// It also pins idempotency: purging an already-absent record is a no-op
// that reports existed=false, never an error (the sweep re-runs freely).
func TestPurgeRecordRemovesMirrorAssociationAndVisibility(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})
	const objectClass = "person"
	const externalID = "555001"

	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"full_name": "Ada Byron"},
		ModifiedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seeding the mirror row: %v", err)
	}
	// One edge FROM the record and one edge TO it, so the purge is proven
	// to clear both endpoints, not just the from-side.
	if err := store.UpsertAssoc(ctx, Assoc{
		FromType: objectClass, FromID: externalID, ToType: "organization", ToID: "900",
		TypeID: 1, Category: "HUBSPOT_DEFINED", Direction: "from",
	}); err != nil {
		t.Fatalf("seeding the from-edge: %v", err)
	}
	if err := store.UpsertAssoc(ctx, Assoc{
		FromType: "deal", FromID: "700", ToType: objectClass, ToID: externalID,
		TypeID: 2, Category: "HUBSPOT_DEFINED", Direction: "to",
	}); err != nil {
		t.Fatalf("seeding the to-edge: %v", err)
	}
	seedVisibilityRow(ctx, t, pool, objectClass, externalID)

	del := Deletion{ObjectClass: objectClass, ExternalID: externalID, DeletedAt: time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)}
	existed, err := store.PurgeRecord(ctx, del)
	if err != nil {
		t.Fatalf("PurgeRecord: %v", err)
	}
	if !existed {
		t.Fatal("PurgeRecord reported existed=false for a mirrored record, want true")
	}

	if _, err := store.getRaw(ctx, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("the mirror row survived the purge: getRaw err = %v, want ErrNotFound", err)
	}
	if n := countRowsTouching(ctx, t, pool, objectClass, externalID); n != 0 {
		t.Fatalf("association/visibility rows survived the purge: %d remain", n)
	}

	// A second purge of the now-absent record is a clean no-op.
	existed, err = store.PurgeRecord(ctx, del)
	if err != nil {
		t.Fatalf("PurgeRecord (idempotent second call): %v", err)
	}
	if existed {
		t.Fatal("PurgeRecord reported existed=true for an already-purged record, want false")
	}
}

// TestReconcileDeletionsPurgesMirroredRecordAndEmits proves the deletion
// sweep end to end against real Postgres: an incumbent-reported deletion
// of a record the mirror holds removes the mirror row, its association
// edges, and its visibility projection, and emits exactly one
// mirror.deleted event_outbox row (scoped to that record), metering one
// poller-lane spend.
func TestReconcileDeletionsPurgesMirroredRecordAndEmits(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})
	const objectClass = "person"
	const externalID = "777001"
	deletedAt := time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)

	if err := ms.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"full_name": "Grace Hopper"},
		ModifiedAt: deletedAt.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seeding the mirror row: %v", err)
	}
	if err := ms.UpsertAssoc(ctx, Assoc{
		FromType: objectClass, FromID: externalID, ToType: "organization", ToID: "900",
		TypeID: 1, Category: "HUBSPOT_DEFINED", Direction: "from",
	}); err != nil {
		t.Fatalf("seeding the edge: %v", err)
	}
	seedVisibilityRow(ctx, t, pool, objectClass, externalID)

	inc := &sweptRecords{deletions: []Deletion{{
		ObjectClass: objectClass, ExternalID: externalID, DeletedAt: deletedAt,
	}}}
	meter := NewMeterWithClock(testMeterConfig(), time.Now)

	if err := ReconcileDeletions(ctx, inc, ms, meter, objectClass); err != nil {
		t.Fatalf("ReconcileDeletions: %v", err)
	}

	if _, err := ms.getRaw(ctx, objectClass, externalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("the mirror row survived the deletion sweep: getRaw err = %v, want ErrNotFound", err)
	}
	if n := countRowsTouching(ctx, t, pool, objectClass, externalID); n != 0 {
		t.Fatalf("association/visibility rows survived the deletion sweep: %d remain", n)
	}
	n, err := countMirrorDeletedEvents(ctx, pool, ws.String(), objectClass, externalID)
	if err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("mirror.deleted outbox rows = %d, want exactly 1", n)
	}
	if snap := meter.Snapshot(ctx); snap.Consumed != 1 {
		t.Fatalf("meter consumed = %d, want 1 (one poller-lane spend for the one deletion)", snap.Consumed)
	}
	// The process-wide deletion counter advanced — it is the value
	// /metrics' margince_overlay_mirror_deleted_total reports. It is a
	// monotonic global (other tests may also increment it), so a >0 check
	// after this purge is the safe, non-flaky assertion.
	if MirrorDeletedTotal() == 0 {
		t.Fatal("MirrorDeletedTotal did not advance after a purge that emitted mirror.deleted")
	}
}

// TestPurgeRecordRejectsANonNumericExternalID pins the numeric-id
// precondition: mirror.deleted's entity ref bridges the incumbent external
// id to a UUID (externalIDToUUID), which only HubSpot's numeric object ids
// satisfy. A non-numeric id is a data defect that must surface as an error
// BEFORE any row is deleted — never a partial purge with the event silently
// dropped.
func TestPurgeRecordRejectsANonNumericExternalID(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	if _, err := store.PurgeRecord(ctx, Deletion{
		ObjectClass: "person", ExternalID: "not-a-number",
		DeletedAt: time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC),
	}); err == nil {
		t.Fatal("PurgeRecord: want an error for a non-numeric external id, got nil")
	}
}

// TestReconcileDeletionsForUnmirroredRecordIsANoOp proves the other half:
// a deletion of a record the mirror never held (never visible to this
// workspace, or already purged) removes nothing, emits no mirror.deleted,
// and returns no error — so the full-scan deletion feed is safe to re-run.
func TestReconcileDeletionsForUnmirroredRecordIsANoOp(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})
	const objectClass = "person"
	const externalID = "777404"
	deletedAt := time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)

	inc := &sweptRecords{deletions: []Deletion{{
		ObjectClass: objectClass, ExternalID: externalID, DeletedAt: deletedAt,
	}}}
	meter := NewMeterWithClock(testMeterConfig(), time.Now)

	if err := ReconcileDeletions(ctx, inc, ms, meter, objectClass); err != nil {
		t.Fatalf("ReconcileDeletions: %v", err)
	}
	n, err := countMirrorDeletedEvents(ctx, pool, ws.String(), objectClass, externalID)
	if err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if n != 0 {
		t.Fatalf("mirror.deleted outbox rows = %d, want 0 — a deletion of an unmirrored record is not an event", n)
	}
}

// countMirrorDeletedEvents counts event_outbox rows carrying mirror.deleted
// for ws and the (objectClass, externalID) record — event_outbox is a
// global, RLS-free infra table (the same caveat countMirrorConflictEvents
// documents), so the workspace filter lives in the query, not a GUC. The
// object_class is part of the match so the count can't be satisfied by an
// unrelated mirror.deleted row that happens to share the external id.
func countMirrorDeletedEvents(ctx context.Context, pool *pgxpool.Pool, ws, objectClass, externalID string) (int, error) {
	var count int
	err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'mirror.deleted'
		   AND envelope->>'workspace_id' = $1
		   AND envelope->'payload'->>'object_class' = $2
		   AND envelope->'payload'->>'external_id' = $3`,
		ws, objectClass, externalID,
	).Scan(&count)
	return count, err
}

// seedVisibilityRow inserts one mirror_visibility grant for the record
// through the tenant-scoped helper the store itself uses, so the row is
// workspace-visible to the purge under the RLS GUC.
func seedVisibilityRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, objectClass, externalID string) {
	t.Helper()
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO mirror_visibility
				(workspace_id, incumbent, mirror_user_id, object_class, external_id, can_see)
			VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, 'hubspot', $1, $2, $3, true)`,
			ids.NewV7(), objectClass, externalID)
		return err
	}); err != nil {
		t.Fatalf("seeding the visibility row: %v", err)
	}
}

// countRowsTouching returns how many overlay_association + mirror_visibility
// rows still name (objectClass, externalID) — the surface a per-record
// purge must leave at zero.
func countRowsTouching(ctx context.Context, t *testing.T, pool *pgxpool.Pool, objectClass, externalID string) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM overlay_association
				  WHERE (from_type = $1 AND from_id = $2) OR (to_type = $1 AND to_id = $2))
			  + (SELECT count(*) FROM mirror_visibility
				  WHERE object_class = $1 AND external_id = $2)`,
			objectClass, externalID).Scan(&n)
	}); err != nil {
		t.Fatalf("counting residual rows: %v", err)
	}
	return n
}

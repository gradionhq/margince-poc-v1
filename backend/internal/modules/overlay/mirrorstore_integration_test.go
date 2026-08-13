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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
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

// TestIngestAcceptsAReprojectionAtTheSameBaseline pins the one case the
// staleness guard must NOT refuse. A re-projection re-fetches a record the
// incumbent has not touched, so the baseline does not advance. The guard
// exists to stop an older read clobbering a newer one — but a re-projection
// is not older data, it is the same data projected by a declaration that has
// since changed, and refusing it would leave the row holding a payload the
// current mapping would never produce, with no way to converge.
func TestIngestAcceptsAReprojectionAtTheSameBaseline(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	const objectClass = "person"
	const externalID = "1"

	baseline := time.Date(2026, 5, 13, 6, 44, 38, 0, time.UTC)
	first := Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:                map[string]any{"first_name": "Ada"},
		ModifiedAt:            baseline,
		ProjectionFingerprint: "fingerprint-one",
	}
	if err := store.Ingest(ctx, first); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	second := first
	second.Fields = map[string]any{"first_name": "Ada", "title": "CTO"}
	second.ProjectionFingerprint = "fingerprint-two"
	if err := store.Ingest(ctx, second); err != nil {
		t.Fatalf("re-projection ingest: %v", err)
	}

	row, err := store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after the re-projection: %v", err)
	}
	if row.ProjectionFingerprint != "fingerprint-two" {
		t.Errorf("fingerprint = %q, want the re-projection's", row.ProjectionFingerprint)
	}
	if row.Fields["title"] != "CTO" {
		t.Errorf("fields = %v, want the re-projected payload; the staleness guard refused a same-baseline re-projection", row.Fields)
	}
}

// TestIngestAcceptsAReprojectionOverAnUnfingerprintedRow covers the rows the
// fingerprint column arrived after: they record no declaration at all, which
// is exactly the state a re-projection must be able to leave. The guard
// compares with IS DISTINCT FROM rather than <> for this row and no other —
// under <> the comparison answers NULL, which is not true, and the write the
// row most needs is the one silently refused.
func TestIngestAcceptsAReprojectionOverAnUnfingerprintedRow(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	const objectClass = "person"
	const externalID = "2"

	baseline := time.Date(2026, 5, 13, 6, 44, 38, 0, time.UTC)
	unfingerprinted := Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"first_name": "Grace"},
		ModifiedAt: baseline,
	}
	if err := store.Ingest(ctx, unfingerprinted); err != nil {
		t.Fatalf("ingest of an unfingerprinted record: %v", err)
	}
	row, err := store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back the unfingerprinted row: %v", err)
	}
	if row.ProjectionFingerprint != "" {
		t.Fatalf("fingerprint = %q, want none recorded for a record that declared none", row.ProjectionFingerprint)
	}

	reprojected := unfingerprinted
	reprojected.Fields = map[string]any{"first_name": "Grace", "title": "Rear Admiral"}
	reprojected.ProjectionFingerprint = "fingerprint-one"
	if err := store.Ingest(ctx, reprojected); err != nil {
		t.Fatalf("re-projection ingest: %v", err)
	}

	row, err = store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after the re-projection: %v", err)
	}
	if row.ProjectionFingerprint != "fingerprint-one" {
		t.Errorf("fingerprint = %q, want the re-projection's", row.ProjectionFingerprint)
	}
	if row.Fields["title"] != "Rear Admiral" {
		t.Errorf("fields = %v, want the re-projected payload; a row recording no declaration was refused a re-projection", row.Fields)
	}
}

// TestIngestStillRefusesAnOlderReadAtTheSameFingerprint holds the guard to its
// original job. Admitting a re-projection widens what the ON CONFLICT clause
// accepts, and the widening is bounded by the declaration having changed: with
// the declaration unchanged, a stale poller page racing a fresher read of the
// same record must still lose.
func TestIngestStillRefusesAnOlderReadAtTheSameFingerprint(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	const objectClass = "person"
	const externalID = "3"
	const fingerprint = "fingerprint-one"

	newer := time.Date(2026, 5, 13, 6, 44, 38, 0, time.UTC)
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:                map[string]any{"first_name": "Katherine"},
		ModifiedAt:            newer,
		ProjectionFingerprint: fingerprint,
	}); err != nil {
		t.Fatalf("newer ingest: %v", err)
	}

	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:                map[string]any{"first_name": "Stale"},
		ModifiedAt:            newer.Add(-24 * time.Hour),
		ProjectionFingerprint: fingerprint,
	}); err != nil {
		t.Fatalf("older ingest: %v", err)
	}

	row, err := store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after the older ingest: %v", err)
	}
	if row.Fields["first_name"] != "Katherine" || !row.UpdatedAtBaseline.Equal(newer) {
		t.Fatalf("an older read at the same fingerprint must not clobber the fresher row: got %+v", row)
	}
}

// A stale page carries a stale projection too, so a differing fingerprint is
// no reason to admit an older read. Letting one through would turn the
// re-projection allowance into a hole in the staleness guard, and the sweep's
// own laggard pages would overwrite fresher rows — which is precisely what a
// declaration change makes likely, since it puts every row's fingerprint out
// of date at once.
func TestIngestRefusesAnOlderReadEvenAtADifferentFingerprint(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	store := NewMirrorStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), noOwnerEmails{})
	const objectClass = "person"
	const externalID = "4"

	newer := time.Date(2026, 5, 13, 6, 44, 38, 0, time.UTC)
	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:                map[string]any{"first_name": "Katherine"},
		ModifiedAt:            newer,
		ProjectionFingerprint: "fingerprint-one",
	}); err != nil {
		t.Fatalf("newer ingest: %v", err)
	}

	if err := store.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:                map[string]any{"first_name": "Stale"},
		ModifiedAt:            newer.Add(-24 * time.Hour),
		ProjectionFingerprint: "fingerprint-two",
	}); err != nil {
		t.Fatalf("older ingest at a different fingerprint: %v", err)
	}

	row, err := store.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back after the older ingest: %v", err)
	}
	if row.Fields["first_name"] != "Katherine" || !row.UpdatedAtBaseline.Equal(newer) {
		t.Fatalf("an older read must not clobber a fresher row whatever its fingerprint says: got %+v", row)
	}
	if row.ProjectionFingerprint != "fingerprint-one" {
		t.Errorf("fingerprint = %q, want the fresher row's — a refused write must leave the column alone", row.ProjectionFingerprint)
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

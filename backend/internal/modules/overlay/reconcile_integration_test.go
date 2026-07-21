// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// Reconcile's real-Postgres proof: mirror.conflict (events catalog Task
// 2 entry) only exists as a durable event_outbox row, and the
// no-clobber-dirty guard Reconcile relies on lives entirely in
// mirrorstore.go's ingestSQL — neither has a fake-DB substitute in this
// package, so this test needs the real, migrated Postgres
// testWorkspaceCtx wires (the same harness freshness_integration_test.go
// and backfill_integration_test.go use).

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// sweptRecords is a minimal in-memory Incumbent exercising ONLY Modified
// — the one method Reconcile drives — seeded directly with Records
// rather than paged like overlay/fake.Adapter, since this test's own
// scope is Reconcile's divergence/conflict decision, not paging
// mechanics (already proven for Backfill's sibling loop by
// backfill_test.go and TestBackfillCursorPersistsAcrossRestartInPostgres
// above). objectClass is deliberately the SAME string for both the
// Modified call and each seeded Record's ObjectClass — the
// incumbent/canonical translation this seam demands in production
// (hubspot.IncumbentClassFor) is proven separately by
// freshness_integration_test.go's stubIncumbent; conflating the two here
// would only add unrelated noise to what this file is proving.
type sweptRecords struct {
	records []Record
}

var _ Incumbent = (*sweptRecords)(nil)

func (s *sweptRecords) Name() string { return "test-swept" }

func (s *sweptRecords) Backfill(context.Context, string, string) (Page, error) {
	return Page{}, errUnusedFixtureMethod("Backfill")
}

func (s *sweptRecords) Modified(_ context.Context, objectClass string, since time.Time, _ string) (Page, error) {
	var matched []Record
	for _, rec := range s.records {
		if rec.ObjectClass == objectClass && !rec.ModifiedAt.Before(since) {
			matched = append(matched, rec)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ModifiedAt.Before(matched[j].ModifiedAt) })
	return Page{Records: matched}, nil
}

func (s *sweptRecords) Get(context.Context, string, string) (Record, error) {
	return Record{}, errUnusedFixtureMethod("Get")
}

func (s *sweptRecords) Associations(context.Context, string, string, string) ([]Assoc, error) {
	return nil, errUnusedFixtureMethod("Associations")
}

func (s *sweptRecords) OwnerEmail(context.Context, string) (string, error) {
	return "", errUnusedFixtureMethod("OwnerEmail")
}

func (s *sweptRecords) Owners(context.Context) ([]OwnerRef, error) { return nil, nil }

func errUnusedFixtureMethod(name string) error {
	return &fixtureMethodError{name: name}
}

type fixtureMethodError struct{ name string }

func (e *fixtureMethodError) Error() string {
	return "sweptRecords: " + e.name + " is not fixtured by this test"
}

// countMirrorConflictEvents counts event_outbox rows carrying
// mirror.conflict for ws and externalID — event_outbox is a global,
// RLS-free infra table (the same honest caveat
// TestFreshnessReaderShedDegradesToMirrorAndEmitsBudgetDegraded's own
// query documents), so no workspace GUC is needed to read it, only to
// filter by workspace in the query itself.
func countMirrorConflictEvents(ctx context.Context, pool *pgxpool.Pool, ws, externalID string) (int, error) {
	var count int
	err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM event_outbox
		 WHERE envelope->>'type' = 'mirror.conflict'
		   AND envelope->>'workspace_id' = $1
		   AND envelope->'payload'->>'external_id' = $2`,
		ws, externalID,
	).Scan(&count)
	return count, err
}

// setSyncState flips a mirror row's sync_state directly — simulating a
// branch-2 dirty write with no branch-2 write path built yet, the same
// approach mirrorstore_integration_test.go's own staleness/dirty test
// takes at its "(c)" step.
func setSyncState(ctx context.Context, pool *pgxpool.Pool, objectClass, externalID, state string) error {
	return database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE overlay_mirror SET sync_state = $1 WHERE object_class = $2 AND external_id = $3`,
			state, objectClass, externalID)
		return err
	})
}

// TestReconcileOverwritesDivergedNonDirtyRowAndEmitsConflict proves the
// design.md §4.4/§4.9 "incumbent-wins" path end to end: a mirror row
// that already existed with sync_state='fresh' (never touched by a
// local write) diverges from an incumbent sweep result with a strictly
// newer ModifiedAt — Reconcile overwrites the mirror with the incumbent
// value AND emits exactly one mirror.conflict event_outbox row (the
// events catalog's registration for OVA-EVT-1), and the returned
// watermark advances to the swept record's ModifiedAt.
func TestReconcileOverwritesDivergedNonDirtyRowAndEmitsConflict(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})

	const objectClass = "organization"
	const externalID = "61655665850"
	oldBaseline := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	newBaseline := oldBaseline.Add(time.Hour)

	if err := ms.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"display_name": "Old Name"},
		ModifiedAt: oldBaseline,
	}); err != nil {
		t.Fatalf("seeding the pre-existing mirror row: %v", err)
	}

	inc := &sweptRecords{records: []Record{{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"display_name": "New Name"},
		ModifiedAt: newBaseline,
	}}}
	meter := NewMeterWithClock(testMeterConfig(), time.Now)

	watermark, err := Reconcile(ctx, inc, ms, meter, objectClass, oldBaseline.Add(-time.Second))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !watermark.Equal(newBaseline) {
		t.Fatalf("watermark = %v, want %v (the swept record's ModifiedAt)", watermark, newBaseline)
	}

	row, err := ms.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back the mirror row: %v", err)
	}
	if row.Fields["display_name"] != "New Name" {
		t.Fatalf("mirror row fields = %+v, want the incumbent-wins overwrite (New Name)", row.Fields)
	}
	if !row.UpdatedAtBaseline.Equal(newBaseline) {
		t.Fatalf("mirror row baseline = %v, want %v", row.UpdatedAtBaseline, newBaseline)
	}

	eventCount, err := countMirrorConflictEvents(ctx, pool, ws.String(), externalID)
	if err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("mirror.conflict outbox rows = %d, want exactly 1", eventCount)
	}

	if snap := meter.Snapshot(ctx); snap.Consumed != 1 {
		t.Fatalf("meter consumed = %d, want 1 (one poller-lane spend for the one swept record)", snap.Consumed)
	}
}

// TestReconcileNeverClobbersADirtyRow proves the other half of
// design.md §4.4's "sync_state-aware" rule: a row flagged pending_sync
// (an un-drained local write, branch 2) is left untouched by an
// incoming incumbent change — Ingest's own no-clobber-dirty guard holds
// it, Reconcile adds no override, and no mirror.conflict fires for a
// row the sweep never actually changed.
func TestReconcileNeverClobbersADirtyRow(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	ms := NewMirrorStore(pool, noOwnerEmails{})

	const objectClass = "organization"
	const externalID = "61655665851"
	oldBaseline := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	newBaseline := oldBaseline.Add(time.Hour)

	if err := ms.Ingest(ctx, Record{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"display_name": "Dirty Local Edit"},
		ModifiedAt: oldBaseline,
	}); err != nil {
		t.Fatalf("seeding the pre-existing mirror row: %v", err)
	}
	if err := setSyncState(ctx, pool, objectClass, externalID, syncStatePendingSync); err != nil {
		t.Fatalf("flipping the row to pending_sync: %v", err)
	}

	inc := &sweptRecords{records: []Record{{
		ObjectClass: objectClass, ExternalID: externalID,
		Fields:     map[string]any{"display_name": "Incumbent Overwrite Attempt"},
		ModifiedAt: newBaseline,
	}}}
	meter := NewMeterWithClock(testMeterConfig(), time.Now)

	if _, err := Reconcile(ctx, inc, ms, meter, objectClass, oldBaseline.Add(-time.Second)); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	row, err := ms.getRaw(ctx, objectClass, externalID)
	if err != nil {
		t.Fatalf("reading back the mirror row: %v", err)
	}
	if row.Fields["display_name"] != "Dirty Local Edit" {
		t.Fatalf("mirror row fields = %+v, want the dirty row protected (Dirty Local Edit)", row.Fields)
	}
	if row.SyncState != syncStatePendingSync {
		t.Fatalf("sync_state = %q, want %q (unchanged)", row.SyncState, syncStatePendingSync)
	}

	eventCount, err := countMirrorConflictEvents(ctx, pool, ws.String(), externalID)
	if err != nil {
		t.Fatalf("querying event_outbox: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("mirror.conflict outbox rows = %d, want 0 — a protected dirty row is not a conflict Reconcile ever landed", eventCount)
	}
}

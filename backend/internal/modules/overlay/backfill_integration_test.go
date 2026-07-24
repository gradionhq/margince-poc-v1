// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// pagingCompanies is a minimal in-memory overlay.Incumbent used only by
// this integration test, seeded records paged pageSize at a time via an
// index cursor — the same shape overlay/fake.Adapter provides. It is
// redeclared here rather than imported: overlay/fake imports this
// package, so importing it FROM an "overlay" (internal) test file would
// be a self-import cycle (overlay -> fake -> overlay). backfill_test.go's
// unit tests live in the external overlay_test package precisely to use
// the real fake package; this integration test stays in the internal
// package instead, to reuse testWorkspaceCtx/noOwnerEmails (also
// internal, in testsupport_integration.go) against a real Postgres.
type pagingCompanies struct {
	records  []Record
	pageSize int
}

var _ Incumbent = (*pagingCompanies)(nil)

func (p *pagingCompanies) Name() string { return "test-paging" }

func (p *pagingCompanies) Backfill(_ context.Context, _, cursor string) (Page, error) {
	start := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d", &start); err != nil {
			return Page{}, fmt.Errorf("test: invalid cursor %q: %w", cursor, err)
		}
	}
	end := start + p.pageSize
	if end > len(p.records) {
		end = len(p.records)
	}
	page := Page{Records: append([]Record(nil), p.records[start:end]...)}
	if end < len(p.records) {
		page.NextCursor = fmt.Sprint(end)
	}
	return page, nil
}

func (p *pagingCompanies) Deletions(_ context.Context, _ string, _ time.Time, _ string) (DeletionPage, error) {
	return DeletionPage{}, nil
}

func (p *pagingCompanies) Modified(_ context.Context, _ string, _ time.Time, _ string) (Page, error) {
	return Page{}, fmt.Errorf("test: Modified is not used by this fixture")
}

func (p *pagingCompanies) Get(_ context.Context, _, _ string) (Record, error) {
	return Record{}, fmt.Errorf("test: Get is not used by this fixture")
}

func (p *pagingCompanies) Associations(_ context.Context, _, _, _ string) ([]Assoc, error) {
	return nil, nil
}

func (p *pagingCompanies) OwnerEmail(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("test: OwnerEmail is not used by this fixture")
}

func (p *pagingCompanies) Owners(context.Context) ([]OwnerRef, error) { return nil, nil }
func (p *pagingCompanies) Create(context.Context, string, map[string]any) (WriteResult, error) {
	return WriteResult{}, fmt.Errorf("pagingCompanies: Create is not fixtured")
}

func (p *pagingCompanies) Update(context.Context, string, string, map[string]any, time.Time) (WriteResult, error) {
	return WriteResult{}, fmt.Errorf("pagingCompanies: Update is not fixtured")
}

func (p *pagingCompanies) Archive(context.Context, string, string, time.Time) error {
	return fmt.Errorf("pagingCompanies: Archive is not fixtured")
}

// crashingMirrorSink wraps a REAL *MirrorStore, forcing the failAfter'th
// Ingest attempt to fail while delegating every other call — including
// UpsertAssoc, LoadBackfillCursor, and SaveBackfillCursor — straight
// through to Postgres. This is the seam the test below uses to simulate
// a process crash without faking the storage layer itself: the cursor
// round-trip this test exists to prove stays entirely real.
type crashingMirrorSink struct {
	*MirrorStore
	attempts  int
	failAfter int
}

var _ MirrorSink = (*crashingMirrorSink)(nil)

func (s *crashingMirrorSink) Ingest(ctx context.Context, rec Record) error {
	s.attempts++
	if s.failAfter != 0 && s.attempts == s.failAfter {
		return fmt.Errorf("test: forced ingest failure at attempt %d", s.attempts)
	}
	return s.MirrorStore.Ingest(ctx, rec)
}

// countMirrorRows counts overlay_mirror rows for objectClass directly —
// a small local escape hatch so this test can assert on row count
// without going through MirrorStore's visibility-gated List/Get (which
// this test seeds no mirror_visibility rows for).
func countMirrorRows(ctx context.Context, pool *pgxpool.Pool, objectClass string) (int, error) {
	var count int
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM overlay_mirror WHERE object_class = $1`, objectClass).Scan(&count)
	})
	return count, err
}

// TestBackfillCursorPersistsAcrossRestartInPostgres proves the
// resumability contract against the REAL persisted cursor, not the
// in-memory fake sink backfill_test.go's unit tests exercise:
// SaveBackfillCursor/LoadBackfillCursor round-trip through the actual
// overlay_backfill_cursor table — its ON CONFLICT upsert and the
// NULLIF(current_setting(...)) workspace-GUC path (same pattern
// mirrorstore.go's ingestSQL uses) — and a "restart" (a fresh MirrorSink
// wrapping the SAME real *MirrorStore, so nothing in-memory survives
// except what Postgres actually persisted) resumes correctly and
// converges in overlay_mirror itself, with no duplicate rows, after a
// mid-page crash.
func TestBackfillCursorPersistsAcrossRestartInPostgres(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	store := NewMirrorStore(pool, noOwnerEmails{})

	const n = 250
	records := make([]Record, n)
	for i := 0; i < n; i++ {
		records[i] = Record{
			ExternalID:  fmt.Sprint(i),
			ObjectClass: "organization",
			Fields:      map[string]any{"display_name": fmt.Sprintf("Org %d", i)},
			ModifiedAt:  time.Now(),
		}
	}
	inc := &pagingCompanies{records: records, pageSize: 100}

	// Crash mid-page-2 (attempt 150 = page 2's 50th record) — the same
	// worst case backfill_test.go's unit test proves against the fake
	// sink, but here Ingest/SaveBackfillCursor/LoadBackfillCursor are all
	// the real *MirrorStore hitting real Postgres.
	crashing := &crashingMirrorSink{MirrorStore: store, failAfter: 150}
	if err := Backfill(ctx, inc, crashing, "companies"); err == nil {
		t.Fatal("want an error from the forced mid-backfill failure, got nil")
	}

	count, err := countMirrorRows(ctx, pool, "organization")
	if err != nil {
		t.Fatalf("counting mirror rows after the crash: %v", err)
	}
	if count != 149 {
		t.Fatalf("after the crash: %d rows in overlay_mirror, want 149 (page 1's 100 + page 2's first 49)", count)
	}

	cursor, done, err := store.LoadBackfillCursor(ctx, "companies")
	if err != nil {
		t.Fatalf("LoadBackfillCursor after the crash: %v", err)
	}
	if cursor != "100" || done {
		t.Fatalf("persisted cursor after the crash = (%q, done=%v), want (\"100\", false)", cursor, done)
	}

	// Restart: a brand-new MirrorSink wrapping the same real store.
	restarted := &crashingMirrorSink{MirrorStore: store}
	if err := Backfill(ctx, inc, restarted, "companies"); err != nil {
		t.Fatalf("the resumed Backfill returned an error: %v", err)
	}

	count, err = countMirrorRows(ctx, pool, "organization")
	if err != nil {
		t.Fatalf("counting mirror rows after resuming: %v", err)
	}
	if count != n {
		t.Fatalf("after resuming: %d rows in overlay_mirror, want exactly %d — any more would mean a duplicate row", count, n)
	}

	cursor, done, err = store.LoadBackfillCursor(ctx, "companies")
	if err != nil {
		t.Fatalf("LoadBackfillCursor after resuming: %v", err)
	}
	if cursor != "" || !done {
		t.Fatalf("persisted cursor after resuming = (%q, done=%v), want (\"\", true)", cursor, done)
	}
}

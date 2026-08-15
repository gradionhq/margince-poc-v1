// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The activity link is the one field on this surface that points OUT of it, so
// it is the one that can disclose something the trace itself does not hold: a
// row id the reader may not read. The trace row is theirs either way — it
// describes their own message — but the row it points at can move out of their
// scope afterwards.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedTracedActivity writes an activity LINKED to a person somebody else owns,
// and a trace row pointing at it.
//
// The link is what puts it outside an own-scoped reader's reach. An activity
// has no owner of its own: it inherits the sensitivity of what it attaches to,
// and one attached to nothing is a workspace-shared note every seat may read —
// so an unlinked fixture would prove the opposite of what it looks like.
func seedTracedActivity(ctx context.Context, t *testing.T, db *database.DB, owner ids.UUID, sourceID string) {
	t.Helper()
	activityID, personID := ids.NewV7(), ids.NewV7()
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, occurred_at, source_system, source_id, source, captured_by)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'note', now(), 'gmail', $2, 'gmail', 'connector:gmail')`,
			activityID, sourceID); err != nil {
			return err
		}
		// Owned by a stranger — a real app_user, because person.owner_id is a
		// foreign key and a scope test that seeded a dangling owner would be
		// proving something about referential integrity instead.
		stranger := ids.NewV7()
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_user (id, workspace_id, email, display_name, status)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $2, 'Somebody Else', 'active')`,
			stranger, "stranger-"+stranger.String()+"@example.test"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'Somebody Else', $2, 'manual', 'human:test')`,
			personID, stranger); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'person', $2)`,
			activityID, personID); err != nil {
			return err
		}
		return capture.Trace(ctx, tx, capture.TraceEntry{
			UserID: owner, Connector: "gmail", SourceSystem: "gmail", SourceID: sourceID,
			Outcome: capture.TraceCaptured, ActivityID: activityID,
		}, false)
	}); err != nil {
		t.Fatalf("seeding a traced activity: %v", err)
	}
}

func TestAnActivityTheReaderCannotSeeIsListedWithoutItsLink(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	memberCtx := memberContext(ctx, ws, me)
	seedTracedActivity(memberCtx, t, db, me, "linked-1")

	window, err := store.ListMine(memberCtx, nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 — the row is the reader's own whatever it points at", len(window.Entries))
	}
	// The entry still lists. Returning the id would make this surface an
	// existence oracle over rows the timeline itself would refuse.
	if window.Entries[0].ActivityID != nil {
		t.Errorf("activity_id = %v, want it dropped for a row outside the reader's scope", window.Entries[0].ActivityID)
	}
}

func TestAnActivityTheReaderCanSeeKeepsItsLink(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	// Same rows, a reader whose scope covers the workspace: the link survives,
	// which is what proves the test above is measuring the probe and not a
	// query that simply never returns a link.
	seedTracedActivity(memberContext(ctx, ws, me), t, db, me, "linked-2")

	window, err := store.ListMine(managerContext(ctx, ws, me), nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(window.Entries))
	}
	if window.Entries[0].ActivityID == nil {
		t.Error("activity_id = nil for a reader who may read it, want the link kept")
	}
}

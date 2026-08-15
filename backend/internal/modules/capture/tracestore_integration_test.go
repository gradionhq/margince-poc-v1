// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// Reading the trace, against the real schema. The assertions that matter here
// are about who CANNOT see what: a member's capture traffic is personal data,
// and the workspace read exists for shared bot bindings, so the two must not
// reach each other by any route — including the funnel's arithmetic.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// memberContext binds a human with no object grants at all — the ordinary seat
// reading their own capture activity.
func memberContext(ctx context.Context, ws, member ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + member.String(),
		UserID:      member,
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}, RowScope: principal.RowScopeOwn},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// managerContext binds a human holding capture_trace read — the shared-channel
// operator view.
func managerContext(ctx context.Context, ws, member ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:" + member.String(),
		UserID: member,
		Permissions: principal.Permissions{
			RoleKeys: []string{"manager"},
			Objects:  map[string]principal.ObjectGrant{"capture_trace": {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// seedTrace writes one row as the pipeline would, at a chosen age.
func seedTrace(ctx context.Context, t *testing.T, db *database.DB, owner ids.UUID, sourceID string, age time.Duration) {
	t.Helper()
	entry := capture.TraceEntry{
		UserID: owner, Connector: "gmail", SourceSystem: "gmail",
		SourceID: sourceID, Outcome: capture.TraceCaptured,
	}
	if err := db.Tx(ctx, func(tx pgx.Tx) error {
		if err := capture.Trace(ctx, tx, entry, false); err != nil {
			return err
		}
		if age == 0 {
			return nil
		}
		// Aged by UPDATE rather than by a clock the test controls: occurred_at
		// defaults in SQL, and a fixture that inserted its own timestamp would
		// stop proving that the default is the column the window reads.
		_, err := tx.Exec(ctx,
			`UPDATE capture_trace SET occurred_at = now() - $2::interval WHERE source_id = $1`,
			sourceID, age.String())
		return err
	}); err != nil {
		t.Fatalf("seeding a trace row: %v", err)
	}
}

// traceReadWorkspace binds a workspace and returns it with a store.
func traceReadWorkspace(t *testing.T) (context.Context, ids.UUID, *database.DB, *capture.TraceStore) {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`, ws, "tracread-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	db := database.BindTo(pool, ids.From[ids.WorkspaceKind](ws))
	return ctx, ws, db, capture.NewTraceStore(db)
}

// A member reads their own traffic and nobody else's — not a colleague's, and
// not the workspace's shared channels either.
func TestListMineIsOnlyTheCallersOwnRows(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me, colleague := ids.NewV7(), ids.NewV7()
	seedTrace(memberContext(ctx, ws, me), t, db, me, "mine-1", 0)
	seedTrace(memberContext(ctx, ws, colleague), t, db, colleague, "theirs-1", 0)
	seedTrace(memberContext(ctx, ws, me), t, db, ids.Nil, "shared-1", 0)

	window, err := store.ListMine(memberContext(ctx, ws, me), nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 (only the caller's own)", len(window.Entries))
	}
	// The funnel is the assertion that matters as much as the list: a count
	// that included a colleague's row would disclose by arithmetic what the
	// list withheld.
	if got := window.Funnel["captured"]; got != 1 {
		t.Errorf("funnel[captured] = %d, want 1 — the funnel and the list must describe the same rows", got)
	}
}

// The workspace read exists for shared bot bindings. It must not become a route
// to a colleague's mailbox for anyone, however privileged.
func TestListWorkspaceNeverReturnsAMembersOwnRows(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	member, manager := ids.NewV7(), ids.NewV7()
	seedTrace(memberContext(ctx, ws, member), t, db, member, "personal-1", 0)
	seedTrace(memberContext(ctx, ws, member), t, db, ids.Nil, "shared-2", 0)

	window, err := store.ListWorkspace(managerContext(ctx, ws, manager), nil, nil)
	if err != nil {
		t.Fatalf("ListWorkspace: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 (the shared-channel row only)", len(window.Entries))
	}
	if got := window.Funnel["captured"]; got != 1 {
		t.Errorf("funnel[captured] = %d, want 1 — a personal row must not be counted here either", got)
	}
}

// A seat without the grant gets a refusal, not an empty page: an empty page
// reads as "your workspace has no shared channels".
func TestListWorkspaceRefusesASeatWithoutTheGrant(t *testing.T) {
	ctx, ws, _, store := traceReadWorkspace(t)
	_, err := store.ListWorkspace(memberContext(ctx, ws, ids.NewV7()), nil, nil)
	if err == nil {
		t.Fatal("ListWorkspace with no grant returned nil, want a refusal")
	}
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("error = %v, want permission denied", err)
	}
}

// The window is 24 hours and the sweep is what enforces it durably; the read
// must not show what the sweep has not reached yet.
func TestTheWindowEndsAtTwentyFourHours(t *testing.T) {
	ctx, ws, db, store := traceReadWorkspace(t)
	me := ids.NewV7()
	seedTrace(memberContext(ctx, ws, me), t, db, me, "fresh", time.Hour)
	seedTrace(memberContext(ctx, ws, me), t, db, me, "stale", 25*time.Hour)

	window, err := store.ListMine(memberContext(ctx, ws, me), nil, nil)
	if err != nil {
		t.Fatalf("ListMine: %v", err)
	}
	if len(window.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 — a row older than the window is not in it", len(window.Entries))
	}
	if got := window.Funnel["captured"]; got != 1 {
		t.Errorf("funnel[captured] = %d, want 1 — the funnel reads the same window as the list", got)
	}
}

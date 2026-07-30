// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migration

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// testWorkspaceCtx mints a fresh workspace + one human app_user over the
// real integration Postgres and binds the actor context every store call
// needs (the overlay module's testsupport_integration.go pattern). It
// fails loudly rather than skipping — a silently skipped gate looks
// exactly like a passing one.
func testWorkspaceCtx(t *testing.T, grants map[string]principal.ObjectGrant) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting the owner DSN: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Migration', $2, 'EUR')`,
		ws, "migration-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	user := ids.New[ids.UserKind]()
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Migration Test User')`,
		user, ws, "migration-user-"+user.String()+"@migration.test"); err != nil {
		t.Fatalf("seeding app_user: %v", err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening the app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	opCtx := principal.WithWorkspaceID(context.Background(), ws)
	opCtx = principal.WithCorrelationID(opCtx, ids.NewV7())
	opCtx = principal.WithActor(opCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user.UUID,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects:  grants,
			RowScope: principal.RowScopeAll,
		},
	})
	return opCtx, pool
}

func adminImportRunGrant() map[string]principal.ObjectGrant {
	return map[string]principal.ObjectGrant{
		importRunObject: {Create: true, Read: true, Update: true, Delete: true},
	}
}

func TestRunStoreLifecycleWithAuditAndResume(t *testing.T) {
	ctx, pool := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(pool)

	run, err := s.Create(ctx, CreateRunInput{Connector: ConnectorMirror, SourceRef: "snap-test", Source: "overlay:flip"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if run.Status != StatusRunning || run.Checkpoint != 0 {
		t.Fatalf("created run = %+v, want running at checkpoint 0", run)
	}

	if err := s.advanceCheckpoint(ctx, run.ID, 3); err != nil {
		t.Fatalf("advanceCheckpoint: %v", err)
	}
	// The cursor never moves backwards — a stale writer is refused.
	if err := s.advanceCheckpoint(ctx, run.ID, 2); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("backwards checkpoint err = %v, want ErrConflict", err)
	}

	if err := s.failRun(ctx, run.ID, errors.New("incumbent went away")); err != nil {
		t.Fatalf("failRun: %v", err)
	}
	got, err := s.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusFailed || got.Error == "" || got.Checkpoint != 3 {
		t.Fatalf("failed run = %+v, want failed with cause and cursor intact (resumable, not a dead end)", got)
	}

	if err := s.Resume(ctx, run.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	rep := Report{Imported: 7, Objects: []ObjectReport{{Object: "person", Created: 7}}}
	if err := s.complete(ctx, run.ID, rep); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, err = s.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("Get after complete: %v", err)
	}
	if got.Status != StatusComplete || got.Report == nil || got.Report.Imported != 7 {
		t.Fatalf("completed run = %+v, want complete with the report persisted", got)
	}

	// Completion is terminal — a second transition is refused.
	if err := s.complete(ctx, run.ID, rep); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("double-complete err = %v, want ErrConflict", err)
	}

	// Every gate audited: create + fail + resume + complete.
	var audits int
	err = database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE entity_type = 'import_run' AND entity_id = $1`,
			run.ID).Scan(&audits)
	})
	if err != nil {
		t.Fatalf("counting audits: %v", err)
	}
	if audits != 4 {
		t.Fatalf("audit rows = %d, want 4 (create, fail, resume, complete)", audits)
	}
}

func TestRunStoreRefusesUngrantedRole(t *testing.T) {
	ctx, pool := testWorkspaceCtx(t, map[string]principal.ObjectGrant{
		importRunObject: {Read: false}, // a rep: no import_run grant at all
	})
	s := NewRunStore(pool)

	if _, err := s.Create(ctx, CreateRunInput{Connector: ConnectorMirror, SourceRef: "x", Source: "t"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("ungranted Create err = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.Latest(ctx, ConnectorMirror); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("ungranted Latest err = %v, want ErrPermissionDenied", err)
	}
}

func TestRunStoreForeignWorkspaceReadsNotFound(t *testing.T) {
	ctxA, pool := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(pool)
	run, err := s.Create(ctxA, CreateRunInput{Connector: ConnectorMirror, SourceRef: "x", Source: "t"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ctxB, _ := testWorkspaceCtx(t, adminImportRunGrant())
	if _, err := NewRunStore(pool).Get(ctxB, run.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("foreign-workspace Get err = %v, want ErrNotFound (existence-hiding)", err)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migration

import (
	"context"
	"errors"
	"os"
	"strings"
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
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
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

	// The crash records what the attempt had already landed, not just its
	// cause: the resumed leg reports only its own work, so this is the
	// only place the pre-crash dispositions are kept.
	partial := Report{Imported: 3, Objects: []ObjectReport{{Object: "person", Created: 3}}}
	if err := s.failRun(ctx, run.ID, partial, errors.New("incumbent went away")); err != nil {
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
	if got.Status != StatusComplete || got.Report == nil {
		t.Fatalf("completed run = %+v, want complete with the report persisted", got)
	}
	// 3 + 7, through a real JSON round-trip: the operator of a resumed
	// cutover is told what the run imported in total, not what its last
	// leg managed. Storing 7 here would read as four lost records.
	if got.Report.Imported != 10 {
		t.Errorf("recorded imported = %d, want 10 — the pre-crash 3 folded into the resumed 7", got.Report.Imported)
	}
	if len(got.Report.Objects) != 1 || got.Report.Objects[0].Created != 10 {
		t.Errorf("recorded objects = %+v, want one person entry crediting all 10", got.Report.Objects)
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

func TestIdentityMapIsIdempotentAndTenantFenced(t *testing.T) {
	ctxA, pool := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(pool)
	run, err := s.Create(ctxA, CreateRunInput{Connector: ConnectorMirror, SourceRef: "snap-a", Source: "overlay:flip"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	native := ids.NewV7()
	if err := s.RecordIdentity(ctxA, run.ID, "hubspot", "person", "p-1", native); err != nil {
		t.Fatalf("RecordIdentity: %v", err)
	}
	// A resumed run replays its last page: re-recording the same tuple
	// converges instead of failing.
	if err := s.RecordIdentity(ctxA, run.ID, "hubspot", "person", "p-1", native); err != nil {
		t.Fatalf("re-recording the same identity: %v", err)
	}
	got, found, err := s.LookupIdentity(ctxA, "hubspot", "person", "p-1")
	if err != nil || !found || got != native {
		t.Fatalf("LookupIdentity = (%v, %v, %v), want the recorded native id", got, found, err)
	}
	// The identity is namespaced by source system and object: a
	// same-id record of another class is a different row.
	if _, found, err := s.LookupIdentity(ctxA, "hubspot", "deal", "p-1"); err != nil || found {
		t.Fatalf("a same-id DEAL resolved to the person's identity (found=%v, err=%v)", found, err)
	}

	// Another workspace neither sees that identity nor may reference the
	// run: the composite FK rejects a cross-workspace run at the
	// database, not merely through RLS visibility.
	ctxB, _ := testWorkspaceCtx(t, adminImportRunGrant())
	if _, found, err := s.LookupIdentity(ctxB, "hubspot", "person", "p-1"); err != nil || found {
		t.Fatalf("workspace B resolved workspace A's identity (found=%v, err=%v)", found, err)
	}
	// Rejected AND existence-hiding: a bare constraint error would tell
	// workspace B that A's run id is real, which is the thing row scope
	// is supposed to withhold.
	err = s.RecordIdentity(ctxB, run.ID, "hubspot", "person", "p-9", ids.NewV7())
	if err == nil {
		t.Fatal("recording an identity against ANOTHER workspace's run must be rejected by the database")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("cross-workspace RecordIdentity err = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "import_record_map") || strings.Contains(err.Error(), "_on_update_import_run") {
		t.Errorf("err %q names the database shape it was rejected by", err)
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

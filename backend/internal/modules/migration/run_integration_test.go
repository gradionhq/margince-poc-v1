// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package migration

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// testWorkspaceCtx mints a fresh workspace + one human app_user over the
// real integration Postgres and binds the actor context every store call
// needs (the overlay module's testsupport_integration.go pattern). It
// fails loudly rather than skipping — a silently skipped gate looks
// exactly like a passing one.
var (
	migrationResetMu  sync.Mutex
	migrationResetFor = map[string]bool{}
)

func testWorkspaceCtx(t *testing.T, grants map[string]principal.ObjectGrant) (context.Context, *database.DB) {
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
	// Every test in this package seeds its own workspace into ONE database, and
	// what used to keep their rows apart was deny-on-unset RLS. With tenant
	// isolation retired (ADR-0091 §8 phase A) the separation has to be real:
	// reset before seeding, as compose/integration's harness does.
	//
	// Once per TEST, not per call. The tenant-fence tests here ask this helper
	// twice — once for workspace A, once for B — and a reset on the second call
	// would delete A before the cross-workspace assertions ran, leaving them
	// asserting nothing.
	migrationResetMu.Lock()
	if !migrationResetFor[t.Name()] {
		if err := testdb.Reset(ctx, owner); err != nil {
			migrationResetMu.Unlock()
			t.Fatal(err)
		}
		migrationResetFor[t.Name()] = true
		t.Cleanup(func() {
			migrationResetMu.Lock()
			defer migrationResetMu.Unlock()
			delete(migrationResetFor, t.Name())
		})
	}
	migrationResetMu.Unlock()

	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		ws, "migration-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	user := ids.New[ids.UserKind]()
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, email, display_name) VALUES ($1, $2, 'Migration Test User')`, user, "migration-user-"+user.String()+"@migration.test"); err != nil {
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
	// The handle as well as the ctx: a store writes the workspace its handle
	// binds, so a second tenant needs one of its own.
	return opCtx, database.BindTo(pool, ids.From[ids.WorkspaceKind](ws))
}

func adminImportRunGrant() map[string]principal.ObjectGrant {
	return map[string]principal.ObjectGrant{
		importRunObject: {Create: true, Read: true, Update: true, Delete: true},
	}
}

func TestRunStoreLifecycleWithAuditAndResume(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(db)

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
	err = db.Tx(ctx, func(tx pgx.Tx) error {
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

func TestIdentityMapIsIdempotentAndRefusesAnUnknownRun(t *testing.T) {
	ctxA, dbA := testWorkspaceCtx(t, adminImportRunGrant())
	s := NewRunStore(dbA)
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

	// A run id that names no run is refused, and refused as not-found. The
	// statement resolves its run rather than trusting the argument, so this is
	// the path a caller with a stale or invented id takes — and it must not come
	// back as a foreign-key error, which would answer with the name of a table
	// the caller has no business hearing about.
	err = s.RecordIdentity(ctxA, RunID(ids.NewV7()), "hubspot", "person", "p-9", ids.NewV7())
	if err == nil {
		t.Fatal("recording an identity against a run that does not exist must be refused")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("RecordIdentity against an unknown run = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "import_record_map") || strings.Contains(err.Error(), "_on_update_import_run") {
		t.Errorf("err %q names the database shape it was rejected by", err)
	}
}

// A run id that names no run reads as not-found rather than as a scan error on
// zero rows — the read is keyed, so this is the only answer it can honestly give.
func TestRunStoreReadOfAnUnknownRunIsNotFound(t *testing.T) {
	ctx, db := testWorkspaceCtx(t, adminImportRunGrant())
	if _, err := NewRunStore(db).Get(ctx, RunID(ids.NewV7())); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("Get of an unknown run = %v, want ErrNotFound", err)
	}
}

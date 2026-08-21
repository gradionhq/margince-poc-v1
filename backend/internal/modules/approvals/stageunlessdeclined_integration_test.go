// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// StageUnlessDeclined's ordering, over a real Postgres — the one property that
// cannot be shown without two live transactions.
//
// The refusal itself is arithmetic: read the prior offers, skip if any is
// rejected. What needs proving is that the read happens LATE ENOUGH to see an
// offer a competing pass is still writing. `FOR UPDATE` cannot give that on its
// own: it locks the rows it finds, and it finds nothing when the competing pass
// has not committed yet — so the identity lock has to be taken first.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type stagingEnv struct {
	svc   *Service
	pool  *pgxpool.Pool
	owner *pgx.Conn
	ws    ids.UUID
	rep   ids.UUID
}

func setupStaging(t *testing.T) *stagingEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	// To head before anything else touches this database: testdb.Pool refuses
	// until EnsureSchema has run, and EnsureSchema still REBUILDS whenever it
	// cannot prove the database is a fresh lane clone — so a seed written
	// before it would be dropped rather than reset.
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatal(err)
	}

	e := &stagingEnv{owner: owner, ws: ids.NewV7(), rep: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		e.ws, "st-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, email, display_name) VALUES ($1, $2, 'Rep')`, e.rep, "rep-"+e.rep.String()+"@st.test"); err != nil {
		t.Fatal(err)
	}
	pool, err := testdb.Pool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Registered where the pool is handed out, before the test adds any cleanup
	// of its own, so it runs last and sees a package that has genuinely stopped.
	// The pool outlives the test now, so a goroutine still holding a connection
	// would go on writing into the database the NEXT test just reset.
	t.Cleanup(func() { testdb.AssertPoolsQuiesced(t) })
	e.pool, e.svc = pool, NewService(database.BindTo(pool, ids.From[ids.WorkspaceKind](e.ws)))
	return e
}

func (e *stagingEnv) as() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.rep.String(), UserID: e.rep,
		Permissions: principal.Permissions{RoleKeys: []string{"admin"}},
	})
}

// A staging that starts while a competing pass is mid-write must not read the
// world as it was before that write. If it did, it would see no prior offers,
// find no PENDING row to join once the other pass committed, and recreate an
// offer a human had already refused.
//
// The competing pass is played by a held transaction: it takes the identity lock
// this staging must also take, writes the rejected offer, and commits. The
// staging is therefore blocked at the lock — by Postgres, not by a timer — until
// that write is visible, and must then refuse.
//
// Without the identity lock taken FIRST, this staging reads past the block: its
// `FOR UPDATE` finds no rows to lock, and it stages. That is the defect.
func TestStageUnlessDeclinedWaitsForACompetingPassBeforeReading(t *testing.T) {
	e := setupStaging(t)
	ctx := e.as()
	// A real organization: the staging path resolves its target's version, so a
	// target that does not exist would fail the run for a reason that has nothing
	// to do with the ordering under test.
	target := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Gitex', 'gmail:seed', 'connector:gmail')`, target); err != nil {
		t.Fatal(err)
	}
	in := StageInput{
		Kind:           "org_name_promotion",
		ProposedChange: []byte(`{"proposed_name":"Gitex Global"}`),
		DiffHash:       "deterministic-hash-for-this-proposal",
		TargetType:     "organization",
		TargetID:       target,
		Summary:        "Rename Gitex to Gitex Global?",
		JoinPending:    true,
	}

	// The competing pass: hold the identity lock, so the staging below cannot
	// read until this commits.
	blocker, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Released whatever happens below. Every failure before the commit leaves this
	// transaction holding a pooled connection, and the pool's own Close waits for
	// it — so a test that means to fail loudly would instead hang until the
	// package timeout kills it, reporting nothing about what went wrong.
	t.Cleanup(func() {
		err := blocker.Rollback(context.Background())
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("releasing the competing transaction: %v", err)
		}
	})
	if _, err := blocker.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if err := lockProposalIdentity(ctx, blocker, e.ws, in); err != nil {
		t.Fatalf("taking the identity lock: %v", err)
	}

	staged := make(chan bool, 1)
	errs := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, ok, err := e.svc.StageUnlessDeclined(e.as(), in)
		errs <- err
		staged <- ok
	}()

	// Wait for the staging to be BLOCKED on that lock rather than merely slow.
	// Busy-read of pg_locks: no clock, no sleep — and it gives up the moment the
	// staging finishes, so a run that never blocked fails saying that rather than
	// spinning. A green run means the ordering really was exercised.
	waitForLockWaiter(t, e, done)

	// Now the competing pass finishes: the offer exists and has been refused.
	if _, err := blocker.Exec(ctx, `
		INSERT INTO approval (kind, status, proposed_change, diff_hash,
		                      target_entity_type, target_entity_id, summary, proposed_by,
		                      decided_by, decided_at, expires_at)
		VALUES ($1, 'rejected', $2, $3, $4, $5, $6, $7, $8, now(), now() + interval '1 day')`,
		in.Kind, in.ProposedChange, in.DiffHash, in.TargetType, in.TargetID,
		in.Summary, "human:"+e.rep.String(), e.rep); err != nil {
		t.Fatalf("writing the refused offer: %v", err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("committing the competing pass: %v", err)
	}

	if err := <-errs; err != nil {
		t.Fatalf("StageUnlessDeclined: %v", err)
	}
	if <-staged {
		t.Fatal("staged an offer a human had already refused — the read ran before the competing write was visible")
	}
	var offers int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM approval WHERE target_entity_id = $1`,
		target).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 1 {
		t.Fatalf("%d offers, want only the refused one", offers)
	}
}

// waitForLockWaiter returns once the staging goroutine is provably BLOCKED on an
// advisory lock in this database. It is the step that makes the test mean what
// its name says, so it has to fail fast and honestly in every way it can miss.
//
// It watches the goroutine as well as the lock. A staging that finishes without
// ever blocking — an early error, a changed lock order — leaves the lock
// condition unsatisfiable forever, and probing on regardless would burn a package
// timeout that names nothing instead of reporting what went wrong. The moment the
// goroutine is done, this stops and says so.
//
// The query is scoped to the CURRENT DATABASE, because pg_locks is cluster-wide
// and the parallel lane runs a dozen packages against one server: an unrelated
// package's waiter would otherwise satisfy this, and the run would sail past the
// ordering it claims to exercise and pass having proved nothing.
func waitForLockWaiter(t *testing.T, e *stagingEnv, done <-chan struct{}) {
	t.Helper()
	waitForBlockedBackend(t, done,
		"the staging finished without ever blocking on the identity lock — it read the world before the competing pass wrote to it, which is the defect this test exists to catch",
		fmt.Sprintf("no backend waited on an advisory lock in this database within %s — the staging never reached the lock, so this run proved nothing", probeBudget),
		func(ctx context.Context) (bool, error) {
			// pg_locks reads the lock manager directly rather than the
			// statistics snapshot, so it answers live even from inside a
			// transaction. That is why this probe needs no
			// pg_stat_clear_snapshot, unlike the pg_stat_activity one the bundle
			// suite uses — and the competing transaction here is held on the
			// pool, not on e.owner, so these probes are not inside it either.
			var waiting bool
			err := e.owner.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM pg_locks l
				   JOIN pg_database d ON d.oid = l.database
				  WHERE l.locktype = 'advisory' AND NOT l.granted
				    AND d.datname = current_database())`).Scan(&waiting)
			return waiting, err
		})
}

// The refusal is narrow: only a REJECTED prior offer stops a staging. Everything
// else about the same proposal behaves exactly as Stage does, which is what makes
// StageUnlessDeclined safe to use in place of it.
func TestStageUnlessDeclinedStagesWhenNothingWasRefused(t *testing.T) {
	e := setupStaging(t)
	target := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Gitex', 'gmail:seed', 'connector:gmail')`, target); err != nil {
		t.Fatal(err)
	}
	in := StageInput{
		Kind:           "org_name_promotion",
		ProposedChange: []byte(`{"proposed_name":"Gitex Global"}`),
		DiffHash:       "hash-nothing-refused",
		TargetType:     "organization",
		TargetID:       target,
		Summary:        "Rename Gitex to Gitex Global?",
		JoinPending:    true,
	}

	first, staged, err := e.svc.StageUnlessDeclined(e.as(), in)
	if err != nil {
		t.Fatalf("StageUnlessDeclined: %v", err)
	}
	if !staged || first.IsZero() {
		t.Fatal("a proposal nobody has refused must stage")
	}

	t.Run("a second pass joins the standing offer instead of adding one", func(t *testing.T) {
		again, staged, err := e.svc.StageUnlessDeclined(e.as(), in)
		if err != nil {
			t.Fatalf("StageUnlessDeclined: %v", err)
		}
		if !staged || again != first {
			t.Fatalf("second pass returned %v (staged=%v), want the standing offer %v", again, staged, first)
		}
		var offers int
		if err := e.owner.QueryRow(context.Background(),
			`SELECT count(*) FROM approval WHERE target_entity_id = $1`,
			target).Scan(&offers); err != nil {
			t.Fatal(err)
		}
		if offers != 1 {
			t.Fatalf("%d offers, want the one that was already standing", offers)
		}
	})

	t.Run("an approved offer does not block a later one", func(t *testing.T) {
		// Only a refusal is an answer of "no". An offer the human ACCEPTED says
		// nothing about whether the same proposal may be made again — and for a
		// nightly stager it usually means the work simply came round again.
		if _, err := e.owner.Exec(context.Background(),
			`UPDATE approval SET status = 'approved', decided_by = $1, decided_at = now()
			  WHERE target_entity_id = $2`, e.rep, target); err != nil {
			t.Fatal(err)
		}
		_, staged, err := e.svc.StageUnlessDeclined(e.as(), in)
		if err != nil {
			t.Fatalf("StageUnlessDeclined: %v", err)
		}
		if !staged {
			t.Fatal("an approved offer must not be read as a refusal")
		}
	})
}

// The non-joining form takes the same refusal check: a caller that does not want
// its proposal joined to a standing one still must not re-offer a refused one.
func TestStageUnlessDeclinedRefusesWithoutJoinPending(t *testing.T) {
	e := setupStaging(t)
	target := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Gitex', 'gmail:seed', 'connector:gmail')`, target); err != nil {
		t.Fatal(err)
	}
	in := StageInput{
		Kind:           "org_name_promotion",
		ProposedChange: []byte(`{"proposed_name":"Gitex Global"}`),
		DiffHash:       "hash-no-join",
		TargetType:     "organization",
		TargetID:       target,
		Summary:        "Rename Gitex to Gitex Global?",
	}

	if _, staged, err := e.svc.StageUnlessDeclined(e.as(), in); err != nil || !staged {
		t.Fatalf("first staging: staged=%v err=%v", staged, err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE approval SET status = 'rejected', decided_by = $1, decided_at = now()
		  WHERE target_entity_id = $2`, e.rep, target); err != nil {
		t.Fatal(err)
	}
	_, staged, err := e.svc.StageUnlessDeclined(e.as(), in)
	if err != nil {
		t.Fatalf("StageUnlessDeclined: %v", err)
	}
	if staged {
		t.Fatal("re-offered a refused proposal on the non-joining path")
	}
}

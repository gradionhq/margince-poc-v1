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
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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

	e := &stagingEnv{owner: owner, ws: ids.NewV7(), rep: ids.NewV7()}
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Staging', $2, 'EUR')`,
		e.ws, "st-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`,
		e.rep, e.ws, "rep-"+e.rep.String()+"@st.test"); err != nil {
		t.Fatal(err)
	}
	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool, e.svc = pool, NewService(pool)
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
		INSERT INTO organization (id, workspace_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Gitex', 'gmail:seed', 'connector:gmail')`, target, e.ws); err != nil {
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
	if _, err := blocker.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatal(err)
	}
	if err := lockProposalIdentity(ctx, blocker, e.ws, in); err != nil {
		t.Fatalf("taking the identity lock: %v", err)
	}

	staged := make(chan bool, 1)
	errs := make(chan error, 1)
	go func() {
		_, ok, err := e.svc.StageUnlessDeclined(e.as(), in)
		errs <- err
		staged <- ok
	}()

	// Wait for the staging to be BLOCKED on that lock rather than merely slow.
	// Busy-read of pg_locks: no clock, no sleep — the loop either observes the
	// waiter or fails loudly, so a green run means the ordering really was
	// exercised.
	waitForLockWaiter(t, e)

	// Now the competing pass finishes: the offer exists and has been refused.
	if _, err := blocker.Exec(ctx, `
		INSERT INTO approval (workspace_id, kind, status, proposed_change, diff_hash,
		                      target_entity_type, target_entity_id, summary, proposed_by,
		                      decided_by, decided_at, expires_at)
		VALUES ($1, $2, 'rejected', $3, $4, $5, $6, $7, $8, $9, now(), now() + interval '1 day')`,
		e.ws, in.Kind, in.ProposedChange, in.DiffHash, in.TargetType, in.TargetID,
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
		`SELECT count(*) FROM approval WHERE workspace_id = $1 AND target_entity_id = $2`,
		e.ws, target).Scan(&offers); err != nil {
		t.Fatal(err)
	}
	if offers != 1 {
		t.Fatalf("%d offers, want only the refused one", offers)
	}
}

// waitForLockWaiter blocks until some backend is WAITING on an advisory lock,
// which is what proves the staging goroutine reached the lock and stopped there.
// Bounded and loud: a run that never observes the waiter fails rather than
// quietly testing a weaker ordering than the one it claims.
func waitForLockWaiter(t *testing.T, e *stagingEnv) {
	t.Helper()
	const maxProbes = 200_000
	for probe := 0; probe < maxProbes; probe++ {
		var waiting bool
		if err := e.owner.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM pg_locks
			   WHERE locktype = 'advisory' AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
	}
	t.Fatal("no backend ever waited on the advisory lock — the staging did not reach it, so this run proved nothing")
}

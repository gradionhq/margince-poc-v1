// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integrations

// The three admission guarantees that only a real database can prove, each
// written against a defect that actually shipped:
//
//   - a caller cannot buy enrichment for a person they may not see;
//   - a run refused on its second credit pool holds NOTHING;
//   - a queued run always has a job to execute it.
//
// None of these can be checked in a unit test. The row scope is a SQL
// predicate, the reservation is several statements under row locks, and the
// hand-off is a constraint on what may be committed together.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

type runsEnv struct {
	store *Store
	ctx   context.Context
	ws    ids.UUID
	// mine is visible to the acting principal; theirs is another rep's
	// capture-private contact, which no other seat can read.
	mine   ids.PersonID
	theirs ids.PersonID
	owner  *pgx.Conn
	// enqueued counts durable hand-offs, so a test can prove one happened.
	enqueued int
	// vault and fake are the store's own instances, exposed so the execution
	// tests can seal a credential and read the egress counter.
	vault keyvault.Vault
	fake  *OfflineProvider
}

func setupRuns(t *testing.T, cfg runsConfig) *runsEnv {
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

	e := &runsEnv{ws: ids.NewV7(), owner: owner,
		mine: ids.New[ids.PersonKind](), theirs: ids.New[ids.PersonKind]()}

	if _, err := owner.Exec(ctx, `INSERT INTO workspace (id, slug) VALUES ($1, $2)`,
		e.ws, "runs-"+e.ws.String()); err != nil {
		t.Fatal(err)
	}
	actor := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO app_user (id, email, display_name, status)
		VALUES ($1, $2, 'Test Rep', 'active')`, actor, "rep-"+actor.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	// A SECOND user, on no shared team, to own the record the acting rep must
	// not reach. An unowned person will not do: the own-scope predicate is
	// `owner_id IS NULL OR owner_id = me`, so a record nobody owns is visible
	// to everybody by design — the thing that hides a record is somebody
	// ELSE owning it.
	stranger := ids.NewV7()
	if _, err := owner.Exec(ctx, `
		INSERT INTO app_user (id, email, display_name, status)
		VALUES ($1, $2, 'Another Rep', 'active')`, stranger, "other-"+stranger.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		id         ids.PersonID
		owner      *ids.UUID
		visibility string
	}{{e.mine, &actor, "workspace"}, {e.theirs, &stranger, "owner"}} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO person (id, owner_id, visibility, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'Anna Muster', 'manual', 'human:test')`,
			p.id, p.owner, p.visibility); err != nil {
			t.Fatal(err)
		}
	}

	// provider_connection is unique per provider — it is installation-owned,
	// so there is exactly one row per provider for the whole database. Upsert
	// rather than insert, so two environments in one test binary do not
	// collide on a table that is deliberately a singleton.
	var connID ids.UUID
	if err := owner.QueryRow(ctx, `
		INSERT INTO provider_connection
		       (id, provider, status, mode, preset, categories, daily_run_limit)
		VALUES ($1, 'surfe', 'connected', 'automatic_on_create', 'full',
		        ARRAY['professional_email','mobile'], NULL)
		ON CONFLICT (provider) DO UPDATE SET status = 'connected'
		RETURNING id`, ids.NewV7()).Scan(&connID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`DELETE FROM provider_connection_budget WHERE connection_id = $1`, connID); err != nil {
		t.Fatal(err)
	}
	for pool, ceiling := range cfg.ceilings {
		if _, err := owner.Exec(ctx, `
			INSERT INTO provider_connection_budget (connection_id, pool, monthly_ceiling)
			VALUES ($1, $2, $3)`, connID, pool, ceiling); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DELETE FROM provider_connection WHERE id = $1`, connID); err != nil {
			t.Errorf("cleaning the connection: %v", err)
		}
	})

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	e.fake = NewOfflineProvider(0, time.Now)
	reg, err := NewRegistry(e.fake)
	if err != nil {
		t.Fatal(err)
	}
	e.vault = keyvault.NewMemory()
	store, err := NewStore(database.BindTo(pool, ids.From[ids.WorkspaceKind](e.ws)),
		e.vault, reg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store.WithDomain(
		func(context.Context, pgx.Tx, string) (FenceVerdict, error) {
			return FenceVerdict{Allowed: true}, nil
		},
		nil,
		func(context.Context, pgx.Tx, string) (provider.PersonIdentifiers, error) {
			return provider.PersonIdentifiers{FirstName: "Anna", LastName: "Muster", CompanyName: "Example"}, nil
		},
	)
	if !cfg.withoutEnqueue {
		store.WithSubmitEnqueue(func(context.Context, pgx.Tx, string, string) error {
			e.enqueued++
			return nil
		})
	}
	e.store = store
	e.ctx = principal.WithActor(principal.WithWorkspaceID(ctx, e.ws), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + actor.String(), UserID: actor,
		Permissions: principal.Permissions{
			Objects: map[string]principal.ObjectGrant{
				"person": {Read: true},
				// What enrichment COSTS is readable by any seat that may see
				// the connection — a rep asking "are we out of credits" is
				// asking about the installation, not about a person.
				"integrations": {Read: true},
			},
			// Own-scope on purpose: this is the scope a rep has. A person is
			// workspace-readable identity, so what the gate must refuse is the
			// other rep's capture-private contact, not merely one they do not own.
			RowScope: principal.RowScopeOwn,
		},
	})
	return e
}

type runsConfig struct {
	ceilings       map[string]int
	withoutEnqueue bool
}

// The defect: QueueRun checked the role grant but never the row scope, so a
// rep could name any person id and buy data on a record outside their scope.
func TestQueueRunRefusesASubjectTheCallerCannotSee(t *testing.T) {
	e := setupRuns(t, runsConfig{})

	if _, err := e.store.QueueRun(e.ctx, provider.QueueInput{
		PersonID: e.theirs.String(), Provider: "surfe", Trigger: provider.TriggerManual,
	}); err == nil {
		t.Fatal("queued a paid run for a person outside the caller's row scope")
	}

	// Nothing was written: the refusal happens before any row exists, so there
	// is no skipped run to explain a purchase that was never authorized.
	var runs int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM provider_run WHERE person_id = $1`, e.theirs).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Errorf("%d run rows exist for an unauthorized subject", runs)
	}

	// The caller's OWN person still works, so the gate refuses the right thing.
	if _, err := e.store.QueueRun(e.ctx, provider.QueueInput{
		PersonID: e.mine.String(), Provider: "surfe", Trigger: provider.TriggerManual,
	}); err != nil {
		t.Fatalf("a visible subject was refused: %v", err)
	}
}

// The defect: pools were locked, checked and inserted in one pass, so a run
// refused on its SECOND pool committed the first pool's hold — credits held
// against work that will never happen.
func TestARunRefusedOnItsSecondPoolHoldsNothing(t *testing.T) {
	// email has room; mobile does not. Alphabetical lock order means email is
	// reserved first, so this is exactly the case that used to leak.
	e := setupRuns(t, runsConfig{ceilings: map[string]int{"email": 10, "mobile": 0}})

	run, err := e.store.QueueRun(e.ctx, provider.QueueInput{
		PersonID: e.mine.String(), Provider: "surfe", Trigger: provider.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != provider.RunSkipped || run.SkipReason != provider.SkipBudgetExhausted {
		t.Fatalf("run is %s/%s, want skipped/budget_exhausted", run.State, run.SkipReason)
	}

	var held int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM provider_run_reservation WHERE run_id = $1`, run.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Errorf("%d reservations survive on a skipped run — the hold is not all-or-nothing, and those credits are lost to the customer's ceiling for the month", held)
	}
}

// The defect: a nil enqueue callback committed a queued run with no job, which
// would occupy the live-run index forever while nothing executed it.
func TestAQueuedRunAlwaysHasAJob(t *testing.T) {
	e := setupRuns(t, runsConfig{withoutEnqueue: true})

	_, err := e.store.QueueRun(e.ctx, provider.QueueInput{
		PersonID: e.mine.String(), Provider: "surfe", Trigger: provider.TriggerManual,
	})
	if err == nil {
		t.Fatal("queued a run with no way to execute it")
	}

	var runs int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM provider_run WHERE person_id = $1 AND state = 'queued'`,
		e.mine).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Errorf("%d inert queued runs committed; each one blocks its subject forever", runs)
	}

	// With the hand-off bound, the same call commits both.
	e2 := setupRuns(t, runsConfig{})
	if _, err := e2.store.QueueRun(e2.ctx, provider.QueueInput{
		PersonID: e2.mine.String(), Provider: "surfe", Trigger: provider.TriggerManual,
	}); err != nil {
		t.Fatal(err)
	}
	if e2.enqueued != 1 {
		t.Errorf("%d jobs enqueued, want 1 — the run and its job commit together or not at all", e2.enqueued)
	}
}

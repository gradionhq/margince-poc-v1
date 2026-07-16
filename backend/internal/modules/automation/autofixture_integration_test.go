// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package automation

// The shared fixture for this package's integration suites, over the
// already-migrated database (`make migrate` is the integration lane's
// precondition). The compose-layer harness cannot be imported here
// (modules never see compose, tests included — backend/arch_test.go),
// so the seeding is spelled locally: an owner connection for RLS-free
// seeding and assertions, and the RLS-bound app pool the code under
// test runs on.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

type autoFixture struct {
	owner *pgx.Conn // RLS-free seeding and assertion lane
	pool  *pgxpool.Pool
	ws    ids.UUID
	rep1  ids.UUID
	rep2  ids.UUID
}

func setupAutomationDB(t *testing.T) *autoFixture {
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
	fx := &autoFixture{owner: owner, ws: ids.NewV7(), rep1: ids.NewV7(), rep2: ids.NewV7()}
	// The database is already migrated (`make migrate`, the integration
	// lane's precondition); each run seeds its own workspace, keyed by
	// the workspace id so reruns never collide on the slug uniques.
	fx.exec(t, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Runs', $2, 'EUR')`, fx.ws, "runs-"+fx.ws.String())
	fx.exec(t, `INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep1')`, fx.rep1, fx.ws, "rep1-"+fx.rep1.String()+"@runs.test")
	fx.exec(t, `INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep2')`, fx.rep2, fx.ws, "rep2-"+fx.rep2.String()+"@runs.test")

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fx.pool = pool
	return fx
}

func (fx *autoFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := fx.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seed %q: %v", sql, err)
	}
}

func (fx *autoFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := fx.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// humanCtx binds a human principal with read/write on the automation
// object and read on the record tables the preview ranges over.
func (fx *autoFixture) humanCtx(user ids.UUID, scope principal.RowScope) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), fx.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:" + user.String(),
		UserID: user,
		Permissions: principal.Permissions{
			RoleKeys: []string{"test"},
			RowScope: scope,
			Objects: map[string]principal.ObjectGrant{
				"automation": {Create: true, Read: true, Update: true, Delete: true},
				"lead":       {Read: true},
				"deal":       {Read: true},
			},
		},
	})
}

// seedAutomation inserts one enabled instance and returns its id. It
// stamps no owner_id — the trusted system-seed shape (SeedStarterAutomationsTx)
// the match-time gate (gate.go) must run ungated.
func (fx *autoFixture) seedAutomation(t *testing.T, key string) ids.AutomationID {
	t.Helper()
	id := ids.New[ids.AutomationKind]()
	fx.exec(t, `
		INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, enabled)
		VALUES ($1, $2, $3, $3, '{"event_type":"test"}', '{"kind":"test"}', '{}'::jsonb, true)`,
		id, fx.ws, key)
	return id
}

// seedAutomationWithOwner is seedAutomation's human-authored sibling: a
// real owner_id, the exact shape automations.go's Create stamps
// (storekit.UUIDOrNil(actor.UserID)) — the gate.go match-time gate applies
// only to instances seeded this way.
func (fx *autoFixture) seedAutomationWithOwner(t *testing.T, key string, owner ids.UUID) ids.AutomationID {
	t.Helper()
	id := ids.New[ids.AutomationKind]()
	fx.exec(t, `
		INSERT INTO automation (id, workspace_id, key, name, trigger, action, params, owner_id, enabled)
		VALUES ($1, $2, $3, $3, '{"event_type":"test"}', '{"kind":"test"}', '{}'::jsonb, $4, true)`,
		id, fx.ws, key, owner)
	return id
}

// seedRun inserts one recorded firing for the automation, linked the way
// the engine links them (handler = key, idempotency key suffixed with
// "@<automation id>"). detail is the raw jsonb payload (rundetail.go) —
// callers build it with reasonDetail/stagedApprovalDetail so the fixture
// writes through the SAME shape the engine does; nil for a clean run.
func (fx *autoFixture) seedRun(t *testing.T, automationID ids.AutomationID, key, status string, detail []byte, at time.Time) {
	t.Helper()
	id := ids.NewV7()
	fx.exec(t, `
		INSERT INTO workflow_run (id, workspace_id, handler, idempotency_key, trigger_event, planned, status, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, '[]'::jsonb, $6, $7, $8)`,
		id, fx.ws, key, fmt.Sprintf("%s:%s@%s", key, id, automationID), ids.NewV7(), status, detail, at)
}

// scriptedWorkflow lets each engine test case pin one phase's behavior.
type scriptedWorkflow struct {
	name  string
	match func(ev workflow.Event) (bool, error)
	plan  func(ev workflow.Event) (workflow.Effect, error)
	apply func(ev workflow.Event) (workflow.RunResult, error)
}

const scriptedTrigger = "history.test_event"

func (s scriptedWorkflow) Spec() workflow.Spec {
	return workflow.Spec{Name: s.name, Trigger: workflow.Trigger{EventType: scriptedTrigger}, Tier: mcp.TierGreen}
}

func (s scriptedWorkflow) Match(_ context.Context, ev workflow.Event) (bool, error) {
	if s.match != nil {
		return s.match(ev)
	}
	return true, nil
}

func (s scriptedWorkflow) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	if s.plan != nil {
		return s.plan(ev)
	}
	args, err := json.Marshal(map[string]string{"note": "planned"})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
}

func (s scriptedWorkflow) Apply(_ context.Context, ev workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	if s.apply != nil {
		return s.apply(ev)
	}
	return workflow.RunResult{Applied: eff.Actions}, nil
}

func (s scriptedWorkflow) IdempotencyKey(ev workflow.Event) string {
	return s.name + ":" + ev.ID.String()
}

type recordedRun struct {
	status  string
	detail  []byte // raw jsonb; decode with parseRunDetail/decodeRunDetail (rundetail.go)
	planned string
}

func (fx *autoFixture) runsByHandler(t *testing.T) map[string]recordedRun {
	t.Helper()
	rows, err := fx.owner.Query(context.Background(),
		`SELECT handler, status, detail, planned::text FROM workflow_run WHERE workspace_id = $1`, fx.ws)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]recordedRun{}
	for rows.Next() {
		var handler string
		var run recordedRun
		if err := rows.Scan(&handler, &run.status, &run.detail, &run.planned); err != nil {
			t.Fatal(err)
		}
		if _, dup := out[handler]; dup {
			t.Fatalf("handler %s recorded more than one run for one event — the idempotency claim leaked", handler)
		}
		out[handler] = run
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

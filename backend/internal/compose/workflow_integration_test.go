// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// The deterministic workflow path (interfaces.md §5): a bus event runs
// Match → Plan → claim → Apply through the composite provider, the
// (handler, key) claim makes at-least-once delivery apply exactly
// once, and a non-matching event leaves no residue.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedStarterAutomations enrolls the starter instances the way the
// workspace bootstrap does — the engine fires nothing for a workspace
// with no enabled automation rows (B-E15.4 gating).
func seedStarterAutomations(t *testing.T, e *searchEnv) {
	t.Helper()
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return agents.SeedStarterAutomationsTx(context.Background(), tx)
	})
	if err != nil {
		t.Fatalf("seeding starter automations: %v", err)
	}
}

func TestWorkflowRouteLeadAppliesExactlyOnce(t *testing.T) {
	e := setupSearch(t)
	seedStarterAutomations(t, e)
	leadID := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Fresh Lead', 'manual', 'human:x')`)
	engine := compose.NewWorkflowEngine(e.pool)

	env := kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        "lead.created",
		WorkspaceID: e.ws,
		OccurredAt:  time.Now().UTC(),
		Entity:      kevents.EntityRef{Type: "lead", ID: leadID},
	}
	if err := engine.HandleEvent(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	// Redelivery (new event id, same lead — the idempotency key is the
	// LEAD, not the envelope) applies nothing new.
	env.EventID = ids.NewV7()
	if err := engine.HandleEvent(context.Background(), env); err != nil {
		t.Fatal(err)
	}

	var tasks, runs int
	var applied []byte
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE kind = 'task' AND subject LIKE 'Triage new lead%'`).Scan(&tasks); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*), max(applied::text)::bytea FROM workflow_run WHERE handler = 'route_lead'`).Scan(&runs, &applied)
	})
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || runs != 1 {
		t.Fatalf("route_lead applied %d tasks over %d runs, want exactly 1/1", tasks, runs)
	}
	var appliedActions []map[string]any
	if err := json.Unmarshal(applied, &appliedActions); err != nil || len(appliedActions) != 1 {
		t.Fatalf("run record lacks the applied trace: %s (%v)", applied, err)
	}
	// The workflow's write is a system-actor fact in the same audit
	// stream as everything else.
	var actorType string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT actor_type FROM audit_log WHERE entity_type = 'activity' ORDER BY occurred_at DESC LIMIT 1`).Scan(&actorType); err != nil {
		t.Fatal(err)
	}
	if actorType != "system" {
		t.Fatalf("workflow write attributed to %q, want system", actorType)
	}
}

// The engine is gated on instances: an unseeded workspace fires
// nothing, a paused instance fires nothing, and enabling flips it live
// on the very next event (no cache) with its params applied.
func TestWorkflowEngineHonorsAutomationInstances(t *testing.T) {
	e := setupSearch(t)
	engine := compose.NewWorkflowEngine(e.pool)

	dispatch := func(leadID ids.UUID) {
		t.Helper()
		if err := engine.HandleEvent(context.Background(), kevents.Envelope{
			EventID: ids.NewV7(), Type: "lead.created", WorkspaceID: e.ws,
			OccurredAt: time.Now().UTC(),
			Entity:     kevents.EntityRef{Type: "lead", ID: leadID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	countTasks := func() int {
		t.Helper()
		var tasks int
		err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT count(*) FROM activity WHERE kind = 'task' AND subject LIKE 'Triage new lead%'`).Scan(&tasks)
		})
		if err != nil {
			t.Fatal(err)
		}
		return tasks
	}

	// No automation rows at all: the registered handler stays silent.
	first := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Ungated Lead', 'manual', 'human:x')`)
	dispatch(first)
	if got := countTasks(); got != 0 {
		t.Fatalf("unconfigured workspace fired %d tasks, want 0", got)
	}

	// A PAUSED instance (the contract's created-paused shape) is silent too.
	var automationID ids.UUID
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO automation (workspace_id, key, name, trigger, action, params, enabled)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'route_lead', 'Route new leads', '{"event_type":"lead.created"}', '{"kind":"create_task"}',
			        '{"due_in_days": 5}', false)
			RETURNING id`).Scan(&automationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	second := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Paused Lead', 'manual', 'human:x')`)
	dispatch(second)
	if got := countTasks(); got != 0 {
		t.Fatalf("paused instance fired %d tasks, want 0", got)
	}

	// Enabled: the next event fires exactly once, honoring the instance
	// params (due 5 days out, not the handler's default 1).
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE automation SET enabled = true WHERE id = $1`, automationID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	third := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Live Lead', 'manual', 'human:x')`)
	dispatch(third)
	dispatch(third) // redelivery still applies exactly once
	if got := countTasks(); got != 1 {
		t.Fatalf("enabled instance fired %d tasks, want exactly 1", got)
	}
	var dueAt, occurredAt time.Time
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT a.due_at, r.created_at FROM activity a, workflow_run r
			WHERE a.kind = 'task' AND r.handler = 'route_lead'`).Scan(&dueAt, &occurredAt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if days := int(dueAt.Sub(occurredAt).Hours() / 24); days < 4 || days > 5 {
		t.Fatalf("task due %v after the run — the instance's due_in_days=5 param did not reach Plan", dueAt.Sub(occurredAt))
	}
}

func TestWorkflowStageChangeMatchGuardsSemantic(t *testing.T) {
	e := setupSearch(t)
	seedStarterAutomations(t, e)
	e.seedDealFixtures(t, 1, nil)
	var dealID ids.UUID
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM deal LIMIT 1`).Scan(&dealID); err != nil {
		t.Fatal(err)
	}
	engine := compose.NewWorkflowEngine(e.pool)

	closedPayload, _ := json.Marshal(map[string]string{"to_semantic": "won"})
	if err := engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.ws,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
		Payload:    closedPayload,
	}); err != nil {
		t.Fatal(err)
	}
	openPayload, _ := json.Marshal(map[string]string{"to_semantic": "open"})
	if err := engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.ws,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
		Payload:    openPayload,
	}); err != nil {
		t.Fatal(err)
	}

	var tasks int
	err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE kind = 'task' AND subject LIKE 'Plan the next step%'`).Scan(&tasks)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The won move matched false; only the open move minted a task —
	// and its link landed on the deal's timeline.
	if tasks != 1 {
		t.Fatalf("stage-change tasks = %d, want 1 (closed moves end the cadence)", tasks)
	}
	var links int
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity_link WHERE deal_id = $1`, dealID).Scan(&links)
	})
	if err != nil || links != 1 {
		t.Fatalf("task link on the deal: %d (%v)", links, err)
	}
}

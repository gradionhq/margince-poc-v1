// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The deterministic workflow path (interfaces.md §5): a bus event runs
// Match → Plan → claim → Apply through the composite provider, the
// (handler, key) claim makes at-least-once delivery apply exactly
// once, and a non-matching event leaves no residue.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedStarterAutomations enrolls the starter instances the way the
// workspace bootstrap does — the engine fires nothing for a workspace
// with no enabled automation rows (B-E15.4 gating).
func seedStarterAutomations(t *testing.T, e *searchEnv) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return automation.SeedStarterAutomationsTx(context.Background(), tx)
	})
	if err != nil {
		t.Fatalf("seeding starter automations: %v", err)
	}
}

// enableStageChangeCreateTask inserts an ENABLED stage_change_create_task
// instance — authorable but NOT one of the six seedStarterAutomations
// enrolls (automations_catalog.go's Catalog doc), so a suite exercising
// its own Match semantic must enable it explicitly.
func enableStageChangeCreateTask(t *testing.T, e *searchEnv) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO automation (workspace_id, key, name, trigger, action, params, enabled)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'stage_change_create_task', 'Follow up on stage changes',
			        '{"event_type":"deal.stage_changed"}', '{"kind":"create_task"}', '{}'::jsonb, true)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// enableLeadRouting inserts an ENABLED assign_lead_owner instance with
// the given routing params — the configured state seedStarterAutomations
// leaves for an admin to fill in (an unconfigured pool routes nobody).
func enableLeadRouting(t *testing.T, e *searchEnv, params map[string]any) ids.UUID {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var automationID ids.UUID
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO automation (workspace_id, key, name, trigger, action, params, enabled)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'assign_lead_owner', 'Assign new leads an owner', '{"event_type":"lead.created"}', '{"kind":"assign_owner"}',
			        $1, true)
			RETURNING id`, paramsJSON).Scan(&automationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return automationID
}

func TestWorkflowRouteLeadAssignsExactlyOnce(t *testing.T) {
	e := setupSearch(t)
	enableLeadRouting(t, e, map[string]any{"owners": []string{e.Rep1.String()}})
	leadID := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Fresh Lead', 'manual', 'human:x')`)
	engine := compose.NewWorkflowEngine(e.Pool)

	env := kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        "lead.created",
		WorkspaceID: e.WS,
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

	var runs int
	var owner *ids.UUID
	var applied []byte
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT owner_id FROM lead WHERE id = $1`, leadID).Scan(&owner); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*), max(applied::text)::bytea FROM workflow_run WHERE handler = 'assign_lead_owner'`).Scan(&runs, &applied)
	})
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || *owner != e.Rep1 || runs != 1 {
		t.Fatalf("assign_lead_owner left owner=%v over %d runs, want rep1 exactly once", owner, runs)
	}
	var appliedActions []map[string]any
	if err := json.Unmarshal(applied, &appliedActions); err != nil || len(appliedActions) != 1 {
		t.Fatalf("run record lacks the applied trace: %s (%v)", applied, err)
	}

	// The decision is an audited, system-attributed fact (AC-S5), and
	// the assignment shipped its lead.updated through the outbox — the
	// full write shape, not a bare column flip.
	var actorType, action string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT actor_type, action FROM audit_log WHERE entity_type = 'lead' AND entity_id = $1
		 ORDER BY occurred_at DESC LIMIT 1`, leadID).Scan(&actorType, &action); err != nil {
		t.Fatal(err)
	}
	if actorType != "system" || action != "assign" {
		t.Fatalf("routing audited as %s/%s, want system/assign", actorType, action)
	}
	var outboxed int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'lead.updated'
		 AND envelope->'entity'->>'id' = $1::text`, leadID).Scan(&outboxed); err != nil {
		t.Fatal(err)
	}
	if outboxed != 1 {
		t.Fatalf("assignment emitted %d lead.updated events, want exactly 1", outboxed)
	}
}

// The engine is gated on instances: an unseeded workspace fires
// nothing, a paused instance fires nothing, and enabling flips it live
// on the very next event (no cache) with its params applied.
func TestWorkflowEngineHonorsAutomationInstances(t *testing.T) {
	e := setupSearch(t)
	engine := compose.NewWorkflowEngine(e.Pool)

	dispatch := func(leadID ids.UUID) {
		t.Helper()
		if err := engine.HandleEvent(context.Background(), kevents.Envelope{
			EventID: ids.NewV7(), Type: "lead.created", WorkspaceID: e.WS,
			OccurredAt: time.Now().UTC(),
			Entity:     kevents.EntityRef{Type: "lead", ID: leadID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	ownerOf := func(leadID ids.UUID) *ids.UUID {
		t.Helper()
		var owner *ids.UUID
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT owner_id FROM lead WHERE id = $1`, leadID).Scan(&owner)
		})
		if err != nil {
			t.Fatal(err)
		}
		return owner
	}

	// No automation rows at all: the registered handler stays silent.
	first := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Ungated Lead', 'manual', 'human:x')`)
	dispatch(first)
	if got := ownerOf(first); got != nil {
		t.Fatalf("unconfigured workspace assigned owner %v, want none", got)
	}

	// A PAUSED instance (the contract's created-paused shape) is silent too.
	var automationID ids.UUID
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO automation (workspace_id, key, name, trigger, action, params, enabled)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'assign_lead_owner', 'Assign new leads an owner', '{"event_type":"lead.created"}', '{"kind":"assign_owner"}',
			        $1, false)
			RETURNING id`, fmt.Sprintf(`{"owners": [%q]}`, e.Rep3.String())).Scan(&automationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	second := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Paused Lead', 'manual', 'human:x')`)
	dispatch(second)
	if got := ownerOf(second); got != nil {
		t.Fatalf("paused instance assigned owner %v, want none", got)
	}

	// Enabled: the next event fires exactly once, honoring the instance
	// params — the configured pool of one (rep3) is where the lead lands.
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE automation SET enabled = true WHERE id = $1`, automationID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	third := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Live Lead', 'manual', 'human:x')`)
	dispatch(third)
	dispatch(third) // redelivery still applies exactly once
	if got := ownerOf(third); got == nil || *got != e.Rep3 {
		t.Fatalf("enabled instance assigned %v — the instance's owner pool did not reach the run", got)
	}
	var runs int
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_run WHERE handler = 'assign_lead_owner'`).Scan(&runs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("enabled instance recorded %d runs, want exactly 1", runs)
	}
}

func TestWorkflowStageChangeMatchGuardsSemantic(t *testing.T) {
	e := setupSearch(t)
	seedStarterAutomations(t, e)
	// stage_change_create_task is authorable but not one of the six
	// seeded templates (automations_catalog.go's Catalog doc) — this
	// suite exercises its own Match semantic, so it enables the instance
	// itself rather than relying on the bootstrap floor.
	enableStageChangeCreateTask(t, e)
	e.seedDealFixtures(t, 1, nil)
	var dealID ids.UUID
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM deal LIMIT 1`).Scan(&dealID); err != nil {
		t.Fatal(err)
	}
	engine := compose.NewWorkflowEngine(e.Pool)

	closedPayload, _ := json.Marshal(map[string]string{"to_semantic": "won"})
	if err := engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.WS,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
		Payload:    closedPayload,
	}); err != nil {
		t.Fatal(err)
	}
	openPayload, _ := json.Marshal(map[string]string{"to_semantic": "open"})
	if err := engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "deal.stage_changed", WorkspaceID: e.WS,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "deal", ID: dealID},
		Payload:    openPayload,
	}); err != nil {
		t.Fatal(err)
	}

	var tasks int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
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
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity_link WHERE deal_id = $1`, dealID).Scan(&links)
	})
	if err != nil || links != 1 {
		t.Fatalf("task link on the deal: %d (%v)", links, err)
	}
}

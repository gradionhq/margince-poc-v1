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
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestWorkflowRouteLeadAppliesExactlyOnce(t *testing.T) {
	e := setupSearch(t)
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

func TestWorkflowStageChangeMatchGuardsSemantic(t *testing.T) {
	e := setupSearch(t)
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

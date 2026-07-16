// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Lead routing under real Postgres (B-E13.7b, features/03 §3 AC-S5):
// round-robin distributes within ±1 in pinned pool order, a capped
// owner is skipped in-transaction (no TOCTOU window), a matching rule
// outranks the rotation but never the cap, an unroutable lead stays
// unowned with the decision on the audit log, and a human assignment
// outranks routing entirely.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// routingEnv is a searchEnv plus a third active rep, so the rotation
// has a shape (two owners cannot distinguish round-robin from ping-pong).
type routingEnv struct {
	*searchEnv
	Rep2   ids.UUID
	engine *automation.WorkflowEngine
}

func setupRouting(t *testing.T) *routingEnv {
	t.Helper()
	e := setupSearch(t)
	rep2 := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'rep2@search.test', 'Rep Two')`,
		rep2, e.WS); err != nil {
		t.Fatal(err)
	}
	return &routingEnv{searchEnv: e, Rep2: rep2, engine: compose.NewWorkflowEngine(e.Pool)}
}

// routeNewLead seeds one unowned lead and dispatches its lead.created
// through the engine, returning the resulting owner (nil = unassigned).
func (e *routingEnv) routeNewLead(t *testing.T, source string) (ids.UUID, *ids.UUID) {
	t.Helper()
	leadID := e.seed(t,
		`INSERT INTO lead (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Routed Lead', $3, 'human:x')`,
		source)
	if err := e.engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "lead.created", WorkspaceID: e.WS,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "lead", ID: leadID},
	}); err != nil {
		t.Fatal(err)
	}
	return leadID, e.leadOwner(t, leadID)
}

func (e *routingEnv) leadOwner(t *testing.T, leadID ids.UUID) *ids.UUID {
	t.Helper()
	var owner *ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT owner_id FROM lead WHERE id = $1`, leadID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestLeadRoutingRoundRobinIsFairAndCapsAreNeverExceeded(t *testing.T) {
	e := setupRouting(t)
	pool := []ids.UUID{e.Rep1, e.Rep2, e.Rep3}
	enableLeadRouting(t, e.searchEnv, map[string]any{
		"owners":        []string{e.Rep1.String(), e.Rep2.String(), e.Rep3.String()},
		"cap_per_owner": 2,
	})

	// Six leads over three owners: pinned rotation in pool order, ±0 at
	// the end of each full cycle.
	for i := range 6 {
		_, owner := e.routeNewLead(t, "manual")
		if owner == nil || *owner != pool[i%3] {
			t.Fatalf("lead %d routed to %v, want pool[%d] — rotation order broke", i, owner, i%3)
		}
	}

	// Everyone now holds cap (2): the seventh lead must stay unowned —
	// never an over-cap assignment — and the refusal is itself an
	// audited decision (AC-S5).
	leadID, owner := e.routeNewLead(t, "manual")
	if owner != nil {
		t.Fatalf("all-capped pool still assigned %v — the cap was exceeded", owner)
	}
	var after []byte
	if err := e.owner.QueryRow(context.Background(),
		`SELECT after FROM audit_log WHERE entity_type = 'lead' AND entity_id = $1 AND action = 'assign'`,
		leadID).Scan(&after); err != nil {
		t.Fatalf("the no-capacity decision left no audit row: %v", err)
	}
	var decision struct {
		Routed bool   `json:"routed"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(after, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Routed || decision.Reason != "no_capacity" {
		t.Fatalf("no-capacity audit does not record the refusal: %s", after)
	}

	// Promoting one of rep1's leads frees capacity; the next lead goes
	// to rep1 — the cap counts OPEN leads, so closed work hands the
	// rotation back.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE lead SET status = 'promoted', promoted_at = now()
		 WHERE id IN (SELECT id FROM lead WHERE owner_id = $1 LIMIT 1)`, e.Rep1); err != nil {
		t.Fatal(err)
	}
	if _, owner := e.routeNewLead(t, "manual"); owner == nil || *owner != e.Rep1 {
		t.Fatalf("freed capacity routed to %v, want rep1", owner)
	}
}

func TestLeadRoutingRuleOutranksRotationButNeverTheCap(t *testing.T) {
	e := setupRouting(t)
	enableLeadRouting(t, e.searchEnv, map[string]any{
		"owners":        []string{e.Rep1.String(), e.Rep2.String()},
		"cap_per_owner": 1,
		"rules": []map[string]any{
			{"field": "source", "equals": "webinar", "owner_id": e.Rep3.String()},
		},
	})

	// The rule owner (rep3) is not in the rotation pool, yet the
	// matching lead is theirs.
	_, owner := e.routeNewLead(t, "webinar")
	if owner == nil || *owner != e.Rep3 {
		t.Fatalf("webinar lead routed to %v, want the rule owner rep3", owner)
	}
	// A non-matching lead ignores the rule and follows the rotation.
	if _, owner := e.routeNewLead(t, "manual"); owner == nil || *owner != e.Rep1 {
		t.Fatalf("non-matching lead routed to %v, want pool[0]", owner)
	}
	// rep3 is now at cap: the cap outranks the rule, the next webinar
	// lead falls through to the pool's next eligible owner.
	if _, owner := e.routeNewLead(t, "webinar"); owner == nil || *owner != e.Rep2 {
		t.Fatalf("capped rule owner: lead routed to %v, want the pool's rep2", owner)
	}
}

func TestLeadRoutingLeavesHumanAssignmentsAlone(t *testing.T) {
	e := setupRouting(t)
	enableLeadRouting(t, e.searchEnv, map[string]any{
		"owners": []string{e.Rep1.String()},
	})

	leadID := e.seed(t,
		`INSERT INTO lead (id, workspace_id, full_name, source, owner_id, captured_by) VALUES ($1, $2, 'Claimed Lead', 'manual', $3, 'human:x')`,
		e.Rep3)
	if err := e.engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "lead.created", WorkspaceID: e.WS,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "lead", ID: leadID},
	}); err != nil {
		t.Fatal(err)
	}
	if owner := e.leadOwner(t, leadID); owner == nil || *owner != e.Rep3 {
		t.Fatalf("routing reassigned a human-owned lead to %v", owner)
	}
	// The run is claimed (redelivery-safe) but records no applied action.
	var runs int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_run WHERE handler = 'route_lead'`).Scan(&runs)
	})
	if err != nil || runs != 1 {
		t.Fatalf("run claim count = %d (%v), want 1", runs, err)
	}
}

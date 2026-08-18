// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The §18.2 escalation is a cross-module write — a lead breach becomes a task
// activity — so it is proven here, where the edge is wired, against the real
// activities store: the task lands, linked to the lead, assigned to the
// escalation target, and a redelivered event does not land a second one.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

func TestLeadSLAEscalationLogsOneTaskOnTheLead(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	lead := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO lead (id, full_name, status, source, captured_by, owner_id)
		 VALUES ($1, 'Overdue Lead', 'new', 'inbound', 'human:x', $2)`, lead, e.Rep1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	target := openapi_types.UUID(e.Rep1)
	payload, err := json.Marshal(crmcontracts.PublicEventLeadSlaBreached{
		Deadline: deadline, OwnerId: &target, EscalationTarget: &target,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := workflow.Event{
		ID: ids.NewV7(), Type: "lead.sla_breached", WorkspaceID: e.WS, OccurredAt: deadline,
		Entity:  datasource.EntityRef{Type: datasource.EntityLead, ID: lead},
		Payload: payload,
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:test"})

	h := leadSLAEscalation{activities: activities.NewStore(e.DB()), now: func() time.Time { return deadline.Add(time.Hour) }}
	eff, err := h.Plan(ctx, ev)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // the bus is at-least-once; the same breach delivered twice is one task
		if _, err := h.Apply(ctx, ev, eff, nil); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	var tasks int
	var assignee *ids.UUID
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*), max(a.assignee_id::text)::uuid FROM activity a
		JOIN activity_link l ON l.activity_id = a.id
		WHERE l.lead_id = $1 AND a.kind = 'task' AND NOT a.is_done`, lead).Scan(&tasks, &assignee); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 {
		t.Fatalf("open tasks on the lead = %d, want exactly 1 after two deliveries of one breach", tasks)
	}
	if assignee == nil || *assignee != e.Rep1 {
		t.Errorf("task assignee = %v, want the escalation target %s", assignee, e.Rep1)
	}
}

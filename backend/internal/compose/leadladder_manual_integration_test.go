// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A hand-logged meeting moves the lead status ladder — the whole chain a rep
// drives from the Log activity composer: the contract request (kind meeting,
// meeting_status held) through the real activities writer, then the ladder
// workflow reading the activity it captured. Proven here because the edge is
// cross-module; the people-side tests seed touches, never the writer.

import (
	"context"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

func TestManualMeetingClimbsTheLeadLadder(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	lead := ids.NewV7()
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO lead (id, full_name, status, source, captured_by, owner_id)
		 VALUES ($1, 'Met In Person', 'new', 'inbound', 'human:x', $2)`, lead, e.Rep1); err != nil {
		t.Fatal(err)
	}
	// The rep logging from the composer: activity create plus read on the
	// lead the link row-scope-gates.
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"activity": {Create: true, Read: true},
			"lead":     {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})

	subject := "Kickoff on site"
	held := crmcontracts.CreateActivityRequestMeetingStatusHeld
	in, err := activities.LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind:          crmcontracts.CreateActivityRequestKindMeeting,
		Subject:       &subject,
		MeetingStatus: &held,
		Links: &[]struct {
			EntityId   openapi_types.UUID                                `json:"entity_id"` //nolint:staticcheck // mirrors the generated inline struct, whose field is spelled EntityId
			EntityType crmcontracts.CreateActivityRequestLinksEntityType `json:"entity_type"`
		}{{EntityId: openapi_types.UUID(lead), EntityType: crmcontracts.CreateActivityRequestLinksEntityTypeLead}},
		Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	act, _, err := activities.NewStore(e.DB()).LogActivity(ctx, in)
	if err != nil {
		t.Fatalf("log the meeting: %v", err)
	}

	var ladder workflow.Handler
	for _, h := range people.LeadSLAWorkflows(people.NewStore(e.DB())) {
		if h.Spec().Name == "lead_status_ladder" {
			ladder = h
		}
	}
	if ladder == nil {
		t.Fatal("lead_status_ladder is not among the registered lead workflows")
	}
	ev := workflow.Event{
		ID: ids.NewV7(), Type: "activity.captured", WorkspaceID: e.WS,
		OccurredAt: time.Now().UTC(),
		Entity:     datasource.EntityRef{Type: datasource.EntityActivity, ID: ids.UUID(act.Id)},
	}
	// The workflow runner drives Apply as the system, exactly as the relay
	// does in production.
	sysCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	sysCtx = principal.WithCorrelationID(sysCtx, ids.NewV7())
	sysCtx = principal.WithActor(sysCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system:test"})
	eff, err := ladder.Plan(sysCtx, ev)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // at-least-once bus: a redelivery must not double-move
		if _, err := ladder.Apply(sysCtx, ev, eff, nil); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}

	var status string
	var setBy *string
	if err := owner.QueryRow(context.Background(),
		`SELECT status, status_set_by FROM lead WHERE id = $1`, lead).Scan(&status, &setBy); err != nil {
		t.Fatal(err)
	}
	if status != "engaged" {
		t.Fatalf("lead status = %q, want engaged — a held meeting a rep logged by hand is engagement", status)
	}
	if setBy == nil || *setBy != "system" {
		t.Errorf("status_set_by = %v, want system", setBy)
	}
}

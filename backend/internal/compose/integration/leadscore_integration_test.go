// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Behavioral lead-score recompute (formulas-and-rules §3): an activity
// LINKED TO A LEAD re-runs the weighted-signal formula through the
// SYSTEM workflow lane — always on, no automation instance behind it —
// exactly once per event, and an activity with no lead link moves
// nothing.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asFullUser binds a principal that may log activities and work leads.
func (e *searchEnv) asFullUser() context.Context {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range []string{"person", "organization", "deal", "lead", "activity"} {
		grants[object] = principal.ObjectGrant{Create: true, Read: true, Update: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeAll},
	})
}

func TestLeadScoreRecomputesFromLinkedActivities(t *testing.T) {
	e := setupSearch(t)
	engine := compose.NewWorkflowEngine(e.Pool)
	ctx := e.asFullUser()

	// A working lead with a decision-maker title from a high-intent
	// source: fit = 15 + 8 = 23 (§3.1). Inserted with score 0 so the
	// recompute demonstrably rebuilds fit AND behavior.
	leadID := e.seed(t, `INSERT INTO lead (id, workspace_id, full_name, title, status, source, score, captured_by)
	                     VALUES ($1, $2, 'Vera VP', 'VP Sales', 'working', 'inbound', 0, 'human:x')`)

	// An inbound email linked to the lead = one fresh reply (+25).
	subject := "Re: your offer"
	direction := "inbound"
	occurred := time.Now().UTC().Add(-1 * time.Minute)
	store := activities.NewStore(e.Pool)
	reply, _, err := store.LogActivity(ctx, activities.LogActivityInput{
		Kind: "email", Subject: &subject, Direction: &direction, OccurredAt: &occurred,
		Links:  []activities.ActivityLinkInput{{EntityType: "lead", EntityID: leadID}},
		Source: "manual",
	})
	if err != nil {
		t.Fatalf("logging the lead-linked reply: %v", err)
	}

	eventID := ids.NewV7()
	dispatchActivityCaptured(t, engine, e.WS, eventID, ids.UUID(reply.Id))
	// fit 23 + one fresh reply ≈ 25 (one minute of decay is noise).
	if got := currentLeadScore(t, e, leadID); got != 48 {
		t.Fatalf("score after reply = %d, want 48 (fit 23 + fresh reply 25)", got)
	}

	// Redelivery of the SAME event applies nothing twice: one run row,
	// score unchanged, exactly one audited score update.
	dispatchActivityCaptured(t, engine, e.WS, eventID, ids.UUID(reply.Id))
	assertRecomputeRanExactlyOnce(t, e)

	// A held meeting adds +30 on the next event.
	meetingSubject := "Demo"
	meetingAt := time.Now().UTC().Add(-30 * time.Second)
	meeting, _, err := store.LogActivity(ctx, activities.LogActivityInput{
		Kind: "meeting", Subject: &meetingSubject, OccurredAt: &meetingAt,
		Links:  []activities.ActivityLinkInput{{EntityType: "lead", EntityID: leadID}},
		Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE activity SET meeting_status = 'held' WHERE id = $1`, ids.UUID(meeting.Id)); err != nil {
		t.Fatal(err)
	}
	dispatchActivityCaptured(t, engine, e.WS, ids.NewV7(), ids.UUID(meeting.Id))
	if got := currentLeadScore(t, e, leadID); got != 78 {
		t.Fatalf("score after held meeting = %d, want 78 (48 + 30)", got)
	}

	// An activity with NO lead link changes nothing.
	noteSubject := "internal note"
	note, _, err := store.LogActivity(ctx, activities.LogActivityInput{
		Kind: "note", Subject: &noteSubject, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatchActivityCaptured(t, engine, e.WS, ids.NewV7(), ids.UUID(note.Id))
	if got := currentLeadScore(t, e, leadID); got != 78 {
		t.Fatalf("unlinked activity moved the score to %d", got)
	}

	// The recompute emitted the catalog's lead.updated with the score delta.
	var events int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'lead.updated'
		   AND envelope->'payload'->'delta' ? 'score'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("lead.updated score events = %d, want 2 (reply, meeting)", events)
	}
}

// dispatchActivityCaptured hands one activity.captured envelope to the
// system workflow lane, the way the relay would deliver it.
func dispatchActivityCaptured(t *testing.T, engine *automation.WorkflowEngine, ws ids.UUID, eventID, activityID ids.UUID) {
	t.Helper()
	if err := engine.HandleEvent(context.Background(), kevents.Envelope{
		EventID: eventID, Type: "activity.captured", WorkspaceID: ws,
		OccurredAt: time.Now().UTC(),
		Entity:     kevents.EntityRef{Type: "activity", ID: activityID},
	}); err != nil {
		t.Fatal(err)
	}
}

// currentLeadScore reads the lead's score through the owner connection.
func currentLeadScore(t *testing.T, e *searchEnv, leadID ids.UUID) int {
	t.Helper()
	var score int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT score FROM lead WHERE id = $1`, leadID).Scan(&score); err != nil {
		t.Fatal(err)
	}
	return score
}

// assertRecomputeRanExactlyOnce checks a redelivered event applied
// nothing twice: one workflow run row and exactly one audited lead
// score update.
func assertRecomputeRanExactlyOnce(t *testing.T, e *searchEnv) {
	t.Helper()
	var runs, audits int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_run WHERE handler = 'recompute_lead_score'`).Scan(&runs); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE entity_type = 'lead' AND action = 'update'`).Scan(&audits)
	})
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || audits != 1 {
		t.Fatalf("redelivery reran the recompute: %d runs, %d lead audits, want 1/1", runs, audits)
	}
}

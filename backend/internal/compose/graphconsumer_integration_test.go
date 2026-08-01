// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The cg:graph-edge consumer and the agent seams behind it (ADR-0078).
//
// The consumer is where this branch went wrong twice: once by listening for an
// event name nothing emits, once by routing erasure through it at all. So the
// tests here are mostly about ROUTING — does this envelope reach the fold, and
// does that one correctly not.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// envelopeFor builds one bus envelope the way the relay ships it.
func envelopeFor(ws ids.UUID, eventType, entityType string, entity ids.UUID) kevents.Envelope {
	return kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     1,
		WorkspaceID: ws,
		OccurredAt:  time.Now().UTC(),
		Entity:      kevents.EntityRef{Type: entityType, ID: entity},
		Trace:       kevents.Trace{CorrelationID: ids.NewV7()},
	}
}

// seedExchange writes one activity with both participant rows and returns it.
func seedExchange(t *testing.T, e *integration.Env, person ids.UUID) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (`+ws+`, 'email', 'Alt', 'inbound', now() - interval '1 day', 'manual', 'human:test')
			RETURNING id`).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (`+ws+`, $1, $2, 'to')`, id, e.Rep1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (`+ws+`, $1, $2, 'from')`, id, person)
		return err
	}); err != nil {
		t.Fatalf("seeding an exchange: %v", err)
	}
	return id
}

func edgeCount(t *testing.T, e *integration.Env, person ids.UUID) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM graph_interaction_edge WHERE person_id = $1`, person).Scan(&n)
	}); err != nil {
		t.Fatalf("counting edges: %v", err)
	}
	return n
}

func TestTheConsumerFoldsAnActivityEventAndIgnoresWhatIsNotItsBusiness(t *testing.T) {
	e := integration.Setup(t)
	person := seedGraphPerson(t, e, "Consumed Contact")
	activityID := seedExchange(t, e, person)

	gen := search.NewGraphEdgeGen(search.NewStore(e.Pool))
	ctx := context.Background()

	// An event for an entity this projection does not care about must answer
	// nil rather than erroring — the group carries other traffic, and a
	// consumer that failed on it would wedge the stream for everybody.
	if err := gen.HandleEvent(ctx, envelopeFor(e.WS, "deal.created", "deal", ids.NewV7())); err != nil {
		t.Errorf("an unrelated event errored: %v", err)
	}
	if edgeCount(t, e, person) != 0 {
		t.Fatal("an edge appeared before any activity event was delivered")
	}

	// The activity event folds it.
	if err := gen.HandleEvent(ctx, envelopeFor(e.WS, "activity.captured", "activity", activityID)); err != nil {
		t.Fatalf("activity.captured: %v", err)
	}
	if edgeCount(t, e, person) != 1 {
		t.Fatal("activity.captured did not fold the interaction into an edge")
	}

	// Redelivery is free: the bus is at-least-once, and the fold recomputes
	// rather than counting.
	for i := 0; i < 3; i++ {
		if err := gen.HandleEvent(ctx, envelopeFor(e.WS, "activity.captured", "activity", activityID)); err != nil {
			t.Fatalf("redelivery %d: %v", i, err)
		}
	}
	if got := edgeCount(t, e, person); got != 1 {
		t.Errorf("redelivery produced %d edges, want 1", got)
	}
}

func TestRetentionAppliedReachesTheConsumer(t *testing.T) {
	e := integration.Setup(t)
	person := seedGraphPerson(t, e, "Retained Contact")
	activityID := seedExchange(t, e, person)

	gen := search.NewGraphEdgeGen(search.NewStore(e.Pool))
	ctx := context.Background()
	if err := gen.HandleEvent(ctx, envelopeFor(e.WS, "activity.captured", "activity", activityID)); err != nil {
		t.Fatalf("folding: %v", err)
	}
	if edgeCount(t, e, person) != 1 {
		t.Fatal("setup produced no edge")
	}

	// The retention sweep archives under ITS name, not the activity's own
	// verb. That branch exists because the consumer previously listened only
	// for names retention never uses, and a projection that silently stops
	// updating is the worst failure this thing has.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE activity SET archived_at = now() WHERE id = $1`, activityID)
		return err
	}); err != nil {
		t.Fatalf("archiving: %v", err)
	}
	if err := gen.HandleEvent(ctx, envelopeFor(e.WS, "retention.applied", "activity", activityID)); err != nil {
		t.Fatalf("retention.applied: %v", err)
	}
	if got := edgeCount(t, e, person); got != 0 {
		t.Errorf("%d edges survived retention.applied — the fold did not react to the archive", got)
	}
}

func TestTheAgentSeamsAnswerThroughTheSameGates(t *testing.T) {
	e := integration.Setup(t)
	person := seedGraphPerson(t, e, "Agent Contact")
	activityID := seedExchange(t, e, person)
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(e.Admin(), tx, []ids.UUID{activityID})
	}); err != nil {
		t.Fatalf("folding: %v", err)
	}

	// who_knows, through the seam the tool actually calls.
	colleagues, err := whoKnowsLister(e.Pool)(e.Admin(), person)
	if err != nil {
		t.Fatalf("who_knows seam: %v", err)
	}
	if len(colleagues) != 1 || colleagues[0].UserID != e.Rep1 {
		t.Fatalf("who_knows answered %+v, want the one colleague who exchanged mail", colleagues)
	}
	if colleagues[0].DisplayName == "" {
		t.Error("the colleague has no name — an id is not an answer to 'who should I ask'")
	}

	// An unknown contact refuses rather than answering an empty network:
	// through the agent exactly as through the URL.
	if _, err := whoKnowsLister(e.Pool)(e.Admin(), ids.NewV7()); err == nil {
		t.Error("the seam answered for a contact that does not exist")
	}
}

func TestCoverageNamesItsColleaguesForTheAgent(t *testing.T) {
	e := integration.Setup(t)
	person := seedGraphPerson(t, e, "Coverage Contact")
	activityID := seedExchange(t, e, person)
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(e.Admin(), tx, []ids.UUID{activityID})
	}); err != nil {
		t.Fatalf("folding: %v", err)
	}

	var dealID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		// The harness seeds no pipeline, so this test brings its own.
		var pipelineID, stageID ids.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO pipeline (workspace_id, name) VALUES (`+ws+`, 'Coverage Test')
			RETURNING id`).Scan(&pipelineID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO stage (workspace_id, pipeline_id, name, position)
			VALUES (`+ws+`, $1, 'Qualified', 0) RETURNING id`, pipelineID).Scan(&stageID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO deal (workspace_id, name, stage_id, pipeline_id, owner_id, source, captured_by)
			VALUES (`+ws+`, 'Covered', $1, $2, $3, 'manual', 'human:test')
			RETURNING id`, stageID, pipelineID, e.Rep1).Scan(&dealID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO relationship (workspace_id, kind, person_id, deal_id, role, source, captured_by)
			VALUES (`+ws+`, 'deal_stakeholder', $1, $2, 'champion', 'manual', 'human:test')`,
			person, dealID)
		return err
	}); err != nil {
		t.Fatalf("seeding the deal: %v", err)
	}

	answer, err := coverageReader(e.Pool)(e.Admin(), dealID)
	if err != nil {
		t.Fatalf("coverage seam: %v", err)
	}
	if len(answer.OurSide) == 0 {
		t.Fatal("coverage named no colleagues though one exchanged mail with the stakeholder")
	}
	// A bare id leaves a model unable to say who to ask, which is the only
	// reason it asked.
	if answer.OurSide[0].DisplayName == "" {
		t.Error("the colleague came back with an empty name")
	}
	if len(answer.Stakeholders) != 1 {
		t.Errorf("coverage listed %d seats, want 1", len(answer.Stakeholders))
	}
}

// The case that made this consumer necessary: a rep types a contact in by hand,
// months after the LinkedIn export was uploaded, and the ghost attaches without
// anybody running an import or clicking a button.
//
// The trigger is the EVENT, not the writer. Every path that creates a person
// emits person.created because the write shape puts it in the outbox, so manual
// entry, mail capture, a site read and a merge all reach this consumer without
// any of them knowing it exists — and a writer added tomorrow is covered the
// day it emits its first event.
func TestAContactAddedLaterMeetsTheGhostThatWasWaiting(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	// The export lands first, on a workspace that knows nobody.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO linkedin_connection
			  (workspace_id, owner_user_id, full_name, normalized_name, email, source)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, 'Dana Buyer', 'dana buyer', 'dana@acme.test', 'csv_export')`, e.Rep1)
		return err
	}); err != nil {
		t.Fatalf("seeding the ghost: %v", err)
	}

	// Months later a rep adds the contact by hand.
	person, err := e.People.CreatePerson(ctx, people.CreatePersonInput{
		FullName: "Dana Buyer", Source: "manual",
		Emails: []people.PersonEmailInput{{Email: "dana@acme.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("creating the contact: %v", err)
	}

	// The consumer reacts to the event that create emitted.
	matcher := NewLinkedInMatchGen(e.People, slog.New(slog.DiscardHandler))
	if err := matcher.HandleEvent(context.Background(),
		envelopeFor(e.WS, "person.created", "person", ids.UUID(person.Id))); err != nil {
		t.Fatalf("handling person.created: %v", err)
	}

	var status string
	var matched *ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT match_status, matched_person_id FROM linkedin_connection
			  WHERE normalized_name = 'dana buyer'`).Scan(&status, &matched)
	}); err != nil {
		t.Fatalf("reading the ghost back: %v", err)
	}
	if status != "confirmed" || matched == nil || *matched != ids.UUID(person.Id) {
		t.Errorf("the ghost is %q → %v, want confirmed → %s — a contact added by "+
			"hand must meet the connection that was already waiting for them",
			status, matched, person.Id)
	}
}

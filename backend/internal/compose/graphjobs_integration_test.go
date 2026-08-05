// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The two background passes behind the relationship graph (ADR-0078): the
// backfill that gives the projection a past, and the reconcile that keeps the
// fold true as time passes.
//
// They are worth testing as WORKERS rather than as their inner functions,
// because the thing that goes wrong with a periodic pass is not its
// arithmetic — it is running over the wrong workspaces, or not terminating.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// legacyInteraction writes an activity with NO participant rows — the shape
// every message captured before ACT-DDL-3 has.
func legacyInteraction(t *testing.T, e *integration.Env, person ids.UUID, capturedBy string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at,
			                      source, captured_by, counterparty_email)
			VALUES (`+ws+`, 'email', 'Alt', 'outbound', now() - interval '3 days',
			        'manual', $1, 'pat@counterparty.test')
			RETURNING id`, capturedBy).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES (`+ws+`, $1, 'person', $2)`, id, person)
		return err
	}); err != nil {
		t.Fatalf("seeding a legacy activity: %v", err)
	}
	return id
}

func seedGraphPerson(t *testing.T, e *integration.Env, name string) ids.UUID {
	t.Helper()
	var id ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO person (workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2,
			        'manual', 'human:test', 'workspace')
			RETURNING id`, name, e.Rep1).Scan(&id)
	}); err != nil {
		t.Fatalf("seeding a person: %v", err)
	}
	return id
}

func TestTheBackfillWorkerRecoversHistoryAndThenStops(t *testing.T) {
	e := integration.Setup(t)
	person := seedGraphPerson(t, e, "Legacy Contact")
	legacyInteraction(t, e, person, "human:"+e.Rep1.String())

	// The WORKSPACE worker is what backfills; the dispatcher beside it only
	// enumerates and enqueues, and driving it here would need a River client.
	worker := &participantBackfillWorkspaceWorker{
		participantBackfillWorker: newParticipantBackfillWorker(e.Pool, quietLog()),
	}
	pass := &river.Job[ParticipantBackfillWorkspaceArgs]{
		Args: ParticipantBackfillWorkspaceArgs{Workspace: e.WS},
	}
	if err := worker.Work(context.Background(), pass); err != nil {
		t.Fatalf("backfill pass: %v", err)
	}

	var participants int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity_participant WHERE user_id = $1`, e.Rep1).Scan(&participants)
	}); err != nil {
		t.Fatal(err)
	}
	if participants == 0 {
		t.Fatal("the backfill recovered nothing from a human-logged activity")
	}

	// A second pass is a no-op. The pass carries no cursor, so this is what
	// makes it safe to re-run after a crash — and what stops the daily
	// schedule from re-doing the same work forever.
	before := participants
	if err := worker.Work(context.Background(), pass); err != nil {
		t.Fatalf("second backfill pass: %v", err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity_participant WHERE user_id = $1`, e.Rep1).Scan(&participants)
	}); err != nil {
		t.Fatal(err)
	}
	if participants != before {
		t.Errorf("a second pass grew the participant set from %d to %d", before, participants)
	}
}

func TestTheReconcileWorkerRebuildsTheProjection(t *testing.T) {
	e := integration.Setup(t)
	person := seedGraphPerson(t, e, "Reconciled Contact")
	activityID := legacyInteraction(t, e, person, "human:"+e.Rep1.String())

	// Participants exist, but nothing has folded them — the state after a
	// backfill runs with no consumer, or after a projection is lost.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (`+ws+`, $1, $2, 'from')`, activityID, e.Rep1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (`+ws+`, $1, $2, 'to')`, activityID, person)
		return err
	}); err != nil {
		t.Fatalf("seeding participants: %v", err)
	}

	// The WORKSPACE worker rebuilds; the dispatcher beside it only enqueues.
	worker := &graphEdgeWorkspaceWorker{
		graphEdgeReconcileWorker: newGraphEdgeReconcileWorker(e.Pool, quietLog()),
	}
	if err := worker.Work(context.Background(), &river.Job[GraphEdgeWorkspaceArgs]{
		Args: GraphEdgeWorkspaceArgs{Workspace: e.WS},
	}); err != nil {
		t.Fatalf("reconcile pass: %v", err)
	}

	var edges int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM graph_interaction_edge WHERE person_id = $1`, person).Scan(&edges)
	}); err != nil {
		t.Fatal(err)
	}
	if edges != 1 {
		t.Errorf("the nightly rebuild produced %d edges from one exchange, want 1 — "+
			"this pass is the corruption remedy, so it has to be able to rebuild from nothing", edges)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The company page's "who here has been in touch" group, over the interaction
// projection (ADR-0078).
//
// This is the defect the whole graph slice started from, and it was invisible.
// The group used to match `captured_by = 'human:<uuid>'` on the activity row.
// Connector-captured mail is stamped `connector:gmail`, so on a workspace whose
// history comes from a real mailbox — which is every workspace that matters —
// the group matched nothing and came back empty. A rep opening a busy account
// read "nobody here has been in touch" with no error anywhere to contradict it.
//
// The test that would have caught it is the one below: capture mail through the
// connector, and demand the colleague appears.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ourSideUsers reads the colleagues the projection would put on the account
// card for one contact set, through the same read the card performs.
func ourSideUsers(t *testing.T, e *integration.Env, people []ids.UUID) map[ids.UUID]bool {
	t.Helper()
	out := map[ids.UUID]bool{}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		edges, err := search.EdgesForPeople(e.Admin(), tx, people)
		if err != nil {
			return err
		}
		for _, edge := range edges {
			out[edge.UserID] = true
		}
		return nil
	}); err != nil {
		t.Fatalf("reading our-side edges: %v", err)
	}
	return out
}

func TestConnectorCapturedMailPutsAColleagueOnTheAccount(t *testing.T) {
	e := integration.Setup(t)
	owner := e.Rep1
	now := time.Now().UTC()

	// A contact, and mail that arrived the way real mail arrives: through a
	// connector, stamped `connector:gmail`, naming no human on the activity.
	var contact ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO person (workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'Pat Counterparty', $1, 'gmail:seed', 'connector:gmail', 'workspace')
			RETURNING id`, owner).Scan(&contact)
	}); err != nil {
		t.Fatalf("seeding the contact: %v", err)
	}

	var activityID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (`+wsClause+`, 'email', 'Angebot', 'inbound', $1, 'gmail:m-1', 'connector:gmail')
			RETURNING id`, now.AddDate(0, 0, -1)).Scan(&activityID); err != nil {
			return err
		}
		// The participant rows capture stamps: the mailbox owner and the
		// counterparty. This is the fact the old derivation had no access to.
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (`+wsClause+`, $1, $2, 'to')`, activityID, owner); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (`+wsClause+`, $1, $2, 'from')`, activityID, contact)
		return err
	}); err != nil {
		t.Fatalf("seeding connector-captured mail: %v", err)
	}

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(e.Admin(), tx, []ids.UUID{activityID})
	}); err != nil {
		t.Fatalf("folding the edge: %v", err)
	}

	// The assertion the old code could never satisfy.
	if got := ourSideUsers(t, e, []ids.UUID{contact}); !got[owner] {
		t.Error("connector-captured mail put nobody on the account: the company page " +
			"would tell a rep that nobody here has been in touch, on an account " +
			"where a colleague has been corresponding all along")
	}
}

func TestADepartedColleagueIsNotOfferedAsAWayIn(t *testing.T) {
	e := integration.Setup(t)
	now := time.Now().UTC()

	var contact ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO person (workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'Known Contact', $1, 'manual', 'human:test', 'workspace')
			RETURNING id`, e.Rep1).Scan(&contact)
	}); err != nil {
		t.Fatalf("seeding the contact: %v", err)
	}

	var activityID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (`+wsClause+`, 'email', 'Alt', 'outbound', $1, 'manual', 'human:test')
			RETURNING id`, now.AddDate(0, 0, -2)).Scan(&activityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (`+wsClause+`, $1, $2, 'from')`, activityID, e.Rep2); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES (`+wsClause+`, $1, $2, 'to')`, activityID, contact)
		return err
	}); err != nil {
		t.Fatalf("seeding the interaction: %v", err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(e.Admin(), tx, []ids.UUID{activityID})
	}); err != nil {
		t.Fatalf("folding the edge: %v", err)
	}
	if got := ourSideUsers(t, e, []ids.UUID{contact}); !got[e.Rep2] {
		t.Fatal("the edge was not created in the first place")
	}

	// They leave — the way the product actually makes that happen.
	// DeactivateUser sets status and leaves archived_at NULL, so a test that
	// archived the row instead would pass against a read that filters only on
	// archived_at while production kept offering the departed colleague. That
	// is exactly what happened here before this line was corrected.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE app_user SET status = 'deactivated' WHERE id = $1`, e.Rep2)
		return err
	}); err != nil {
		t.Fatalf("deactivating the colleague: %v", err)
	}

	if got := ourSideUsers(t, e, []ids.UUID{contact}); got[e.Rep2] {
		t.Error("a departed colleague is still offered as a way into the account")
	}
}

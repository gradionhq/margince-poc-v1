// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Art. 17 over the relationship graph (ADR-0078).
//
// The graph stores two things about a data subject that the person row does
// not: WHO they were in a conversation with, and — for a party who never
// became a record — the raw address a message carried them under. Both are
// personal data, and both survived the person_email purge.
//
// This was a live hole. The projection's consumer listened for a
// `person.erased` event that no code path emits, so an erasure completed
// successfully while leaving the subject's correspondence pattern standing —
// who talked to them, how often, how recently. Erasure now discharges it
// inside its own transaction, because an obligation that depends on an event
// being delivered is one that fails silently when the bus is behind.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// countRows answers one scalar count under the workspace transaction.
func countRows(t *testing.T, e *integration.Env, query string, args ...any) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("counting: %v", err)
	}
	return n
}

func TestErasureRemovesTheSubjectFromTheRelationshipGraph(t *testing.T) {
	e := integration.Setup(t)
	now := time.Now().UTC()
	const subjectEmail = "erase.me@counterparty.test"

	var person ids.UUID
	var activityID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if err := tx.QueryRow(ctx, `
			INSERT INTO person (workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES (`+ws+`, 'Erase Me', $1, 'manual', 'human:test', 'workspace')
			RETURNING id`, e.Rep1).Scan(&person); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
			VALUES (`+ws+`, $1, $2, true, 'manual', 'human:test')`, person, subjectEmail); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
			VALUES (`+ws+`, 'email', 'Angebot', 'inbound', $1, 'manual', 'human:test')
			RETURNING id`, now.AddDate(0, 0, -1)).Scan(&activityID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
			VALUES (`+ws+`, $1, $2, 'to')`, activityID, e.Rep1); err != nil {
			return err
		}
		// The subject appears twice: as a resolved person, and — on a second
		// message — as a bare address, the row that exists for a party who
		// never became a record.
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_participant (workspace_id, activity_id, person_id, address, role)
			VALUES (`+ws+`, $1, $2, $3, 'from')`, activityID, person, subjectEmail); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			VALUES (`+ws+`, $1, 'person', $2)`, activityID, person); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding the subject: %v", err)
	}

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(e.Admin(), tx, []ids.UUID{activityID})
	}); err != nil {
		t.Fatalf("folding the edge: %v", err)
	}
	if n := countRows(t, e, `SELECT count(*) FROM graph_interaction_edge WHERE person_id = $1`, person); n != 1 {
		t.Fatalf("the edge was not created in the first place (%d rows)", n)
	}

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "subject request"); err != nil {
		t.Fatalf("erasing: %v", err)
	}

	// The correspondence pattern goes. It says who talked to the subject, how
	// often and how recently — derived data, but derived from data that is
	// now gone.
	if n := countRows(t, e, `SELECT count(*) FROM graph_interaction_edge WHERE person_id = $1`, person); n != 0 {
		t.Errorf("%d interaction edges survived the erasure: the projection still holds who corresponded with the subject", n)
	}
	// And the raw address goes with it — it survives the person_email purge
	// by construction, and would keep the erased subject re-matchable.
	if n := countRows(t, e,
		`SELECT count(*) FROM activity_participant WHERE address = $1 OR person_id = $2`, subjectEmail, person); n != 0 {
		t.Errorf("%d participant rows still name the erased subject by address or id", n)
	}
	// The colleague's own participation is NOT collateral damage: other
	// people in that conversation are not the subject, and erasing the record
	// that a colleague was in a meeting is not what Art. 17 asks for.
	if n := countRows(t, e,
		`SELECT count(*) FROM activity_participant WHERE activity_id = $1 AND user_id = $2`, activityID, e.Rep1); n != 1 {
		t.Errorf("the colleague's own participant row was destroyed by someone else's erasure (%d rows)", n)
	}
}

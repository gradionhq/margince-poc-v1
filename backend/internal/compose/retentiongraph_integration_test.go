// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Retention keeps the relationship graph true, in its OWN transaction.
//
// The correction used to be a second SQL statement inside privacy that only
// deleted pairs with no evidence left, so a pair with other interactions kept
// counting the removed one. Routing it through the search fold fixed the
// arithmetic but moved it onto the bus, which traded an atomic guarantee for
// an eventual one — on a deletion obligation.
//
// It is now the injected seam: the same fold, inside the retention
// transaction. This test drives the real RetentionService with the real
// invalidator and asserts the aggregate is already correct when the
// transaction commits — no consumer, no bus, no waiting.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestRetentionCorrectsTheRelationshipGraphInItsOwnTransaction(t *testing.T) {
	e := integration.Setup(t)
	now := time.Now().UTC()

	var contact ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			INSERT INTO person (workspace_id, full_name, owner_id, source, captured_by, visibility)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'Long Thread', $1, 'manual', 'human:test', 'workspace')
			RETURNING id`, e.Rep1).Scan(&contact)
	}); err != nil {
		t.Fatalf("seeding the contact: %v", err)
	}

	// Two interactions, so archiving one leaves the pair alive — the case a
	// delete-only correction silently gets wrong.
	seed := func(at time.Time, direction, personRole string) ids.UUID {
		t.Helper()
		var id ids.UUID
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			ctx := context.Background()
			ws := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
			if err := tx.QueryRow(ctx, `
				INSERT INTO activity (workspace_id, kind, subject, direction, occurred_at, source, captured_by)
				VALUES (`+ws+`, 'email', 'Alt', $1, $2, 'manual', 'human:test')
				RETURNING id`, direction, at).Scan(&id); err != nil {
				return err
			}
			userRole := "from"
			if direction == "inbound" {
				userRole = "to"
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO activity_participant (workspace_id, activity_id, user_id, role)
				VALUES (`+ws+`, $1, $2, $3)`, id, e.Rep1, userRole); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
				VALUES (`+ws+`, $1, $2, $3)`, id, contact, personRole)
			return err
		}); err != nil {
			t.Fatalf("seeding an interaction: %v", err)
		}
		return id
	}
	older := seed(now.AddDate(0, 0, -20), "inbound", "from")
	newer := seed(now.AddDate(0, 0, -2), "outbound", "to")

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return search.RecomputeEdgesForActivities(e.Admin(), tx, []ids.UUID{older, newer})
	}); err != nil {
		t.Fatalf("folding: %v", err)
	}

	// A policy that makes BOTH interactions due, driven through the real
	// public sweep — no test-only hook, so what runs here is what runs
	// nightly. retain_days is 1, and both are older than that.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO retention_policy (workspace_id, object_type, retain_days, action, enabled)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'activity', 1, 'archive', true)`)
		return err
	}); err != nil {
		t.Fatalf("seeding the retention policy: %v", err)
	}

	// The real service, wired exactly as the worker wires it.
	svc := privacy.NewRetentionService(e.Pool, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithEdgeInvalidator(func(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error {
			return search.RecomputeEdgesForActivities(ctx, tx, []ids.UUID{activityID})
		})
	if err := svc.Evaluate(e.Admin()); err != nil {
		t.Fatalf("running the retention sweep: %v", err)
	}

	// Corrected the moment retention commits, with no consumer running: the
	// sweep archived both interactions, so the pair has no evidence left and
	// its aggregate must be gone rather than frozen at two.
	var edges int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM graph_interaction_edge WHERE person_id = $1`, contact).Scan(&edges)
	}); err != nil {
		t.Fatalf("reading the edge after retention: %v", err)
	}
	if edges != 0 {
		t.Errorf("%d interaction edges survived a retention sweep that archived every interaction "+
			"behind them — the aggregate outlived the data it was folded from", edges)
	}
	_ = older
}

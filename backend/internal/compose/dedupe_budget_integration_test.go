// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Two composition seams: a captured lead colliding with a live lead
// stages a 🟡 merge proposal (never a second row, never an auto-merge),
// and the AI budget derives live from the workspace's full seats.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func connectorCtx(e *authzEnv) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:test",
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"lead": {Create: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestCaptureDedupeStagesMergeInsteadOfDuplicating(t *testing.T) {
	e := setupAuthz(t)
	sink := capture.NewSink(e.pool).WithStager(mergeStager{svc: approvals.NewService(e.pool)})
	ctx := connectorCtx(e)

	first, err := sink.Upsert(ctx, connector.NormalizedRecord{
		EntityType: "lead",
		NaturalKey: connector.NaturalKey{SourceSystem: "apollo", SourceID: "a-1"},
		Fields:     capture.LeadFields{FullName: "Dana Dup", Email: "dana@example.test"},
		Source:     "apollo:a-1", CapturedBy: "connector:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same address from ANOTHER source: no second row, the existing ref
	// answers, and a merge proposal lands in the inbox.
	second, err := sink.Upsert(ctx, connector.NormalizedRecord{
		EntityType: "lead",
		NaturalKey: connector.NaturalKey{SourceSystem: "hubspot", SourceID: "h-9"},
		Fields:     capture.LeadFields{FullName: "Dana Duplicate", Email: "DANA@example.test "},
		Source:     "hubspot:h-9", CapturedBy: "connector:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = second
	err = database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		var leads, proposals int
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM lead WHERE email = 'dana@example.test'`).Scan(&leads); err != nil {
			return err
		}
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM approval WHERE kind = 'merge_records' AND target_entity_id = $1 AND status = 'pending'`,
			first.ID).Scan(&proposals); err != nil {
			return err
		}
		if leads != 1 || proposals != 1 {
			t.Errorf("dedupe left %d leads and %d proposals, want 1/1", leads, proposals)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A replay of the ORIGINAL natural key is idempotent, not a
	// self-collision.
	replay, err := sink.Upsert(ctx, connector.NormalizedRecord{
		EntityType: "lead",
		NaturalKey: connector.NaturalKey{SourceSystem: "apollo", SourceID: "a-1"},
		Fields:     capture.LeadFields{FullName: "Dana Dup", Email: "dana@example.test"},
		Source:     "apollo:a-1", CapturedBy: "connector:test",
	})
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay → %v / %v, want the original row", replay, err)
	}
}

func TestSeatDerivedBudget(t *testing.T) {
	e := setupAuthz(t)
	// setupAuthz seeds three full-seat humans.
	budget, err := NewSeatBudget(e.pool).MonthlyTokenBudget(context.Background(), e.ws)
	if err != nil {
		t.Fatal(err)
	}
	if budget != 3*perSeatBaseTokens*budgetSafetyFactor {
		t.Fatalf("3-seat budget = %d, want %d", budget, 3*perSeatBaseTokens*budgetSafetyFactor)
	}
	// An empty workspace floors at one seat rather than refusing. The
	// workspace table sits outside RLS and is owner-seeded, so the seed
	// goes through the owner connection like every other fixture.
	empty := ids.NewV7()
	owner, err := pgx.Connect(context.Background(), os.Getenv("MARGINCE_TEST_DSN"))
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Empty', 'empty-budget', 'EUR')`, empty); err != nil {
		t.Fatalf("workspace insert: %v", err)
	}
	budget, err = NewSeatBudget(e.pool).MonthlyTokenBudget(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	if budget != perSeatBaseTokens*budgetSafetyFactor {
		t.Fatalf("empty-workspace budget = %d, want the single-seat floor", budget)
	}
}

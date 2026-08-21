// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Two composition seams: a captured lead colliding with a live lead
// stages a 🟡 merge proposal (never a second row, never an auto-merge),
// and the AI budget derives live from the workspace's full seats.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

func connectorCtx(e *integration.Env) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
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
	e := integration.Setup(t)
	sink := capture.NewSink(e.DB()).WithStager(mergeStager{svc: approvals.NewService(e.DB())})
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
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
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
	e := integration.Setup(t)
	// The harness seeds four full-seat humans: three reps and the admin seat.
	const seats = 4
	budget, err := NewSeatBudget(e.Pool).MonthlyTokenBudget(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	if err != nil {
		t.Fatal(err)
	}
	if budget != seats*perSeatBaseTokens*budgetSafetyFactor {
		t.Fatalf("%d-seat budget = %d, want %d", seats, budget, seats*perSeatBaseTokens*budgetSafetyFactor)
	}
	// An installation with no live full seat floors at one rather than
	// refusing. Reached by deactivating the seats, which is how it actually
	// happens: the case this floor exists for is an onboarding flow calling the
	// model before the first seat settles. It used to be reached by pointing
	// the budget at a second, empty workspace — ADR-0091 §8 phase D took the
	// tenant column off app_user, so a seat count is the installation's and
	// there is no empty workspace to ask about.
	owner := integration.OwnerConn(t)
	if _, err := owner.Exec(context.Background(),
		`UPDATE app_user SET status = 'deactivated' WHERE seat_type = 'full'`); err != nil {
		t.Fatalf("deactivating the full seats: %v", err)
	}
	budget, err = NewSeatBudget(e.Pool).MonthlyTokenBudget(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
	if err != nil {
		t.Fatal(err)
	}
	if budget != perSeatBaseTokens*budgetSafetyFactor {
		t.Fatalf("seatless-installation budget = %d, want the single-seat floor", budget)
	}
}

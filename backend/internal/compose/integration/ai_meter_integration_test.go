// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The metering counters under real RLS: usage accumulates per
// (workspace, day, task, tier), the month sum feeds the budget bands,
// and one tenant's spend is invisible to another — the ai_usage table
// is tenant data like any other.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// meterFreshDatabase resets the data to a clean slate and ensures the schema is
// migrated (once per process, then a fast TRUNCATE — see package testdb),
// returning the owner connection and the RLS-bound app DSN.
func meterFreshDatabase(t *testing.T, ctx context.Context) (*pgx.Conn, string) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatalf("migrating schema: %v", err)
	}
	if err := testdb.Truncate(ctx, owner); err != nil {
		t.Fatalf("resetting database: %v", err)
	}
	return owner, appDSN
}

// meterWorkspace plants one tenant row through the owner connection.
func meterWorkspace(t *testing.T, ctx context.Context, owner *pgx.Conn, slug string) ids.UUID {
	t.Helper()
	var raw string
	if err := owner.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, base_currency) VALUES ($1, $1, 'EUR') RETURNING id`,
		slug).Scan(&raw); err != nil {
		t.Fatalf("workspace insert: %v", err)
	}
	wsID, err := ids.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return wsID
}

func TestMeterAccumulatesUnderRLS(t *testing.T) {
	ctx := context.Background()
	owner, appDSN := meterFreshDatabase(t, ctx)
	wsA := meterWorkspace(t, ctx, owner, "meter-a")
	wsB := meterWorkspace(t, ctx, owner, "meter-b")

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	meter := ai.NewMeter(pool)
	ctxA := principal.WithWorkspaceID(ctx, wsA)
	ctxB := principal.WithWorkspaceID(ctx, wsB)

	// Two calls on the same (day, task, tier) fold into one counter row.
	for _, usage := range []ai.Usage{
		{Task: ai.TaskSummarize, Tier: ai.TierCheapCloud, TokensIn: 100, TokensOut: 40},
		{Task: ai.TaskSummarize, Tier: ai.TierCheapCloud, TokensIn: 50, TokensOut: 10, Cached: true},
		{Task: ai.TaskBriefRanking, Tier: ai.TierPremium, TokensIn: 500, TokensOut: 300},
	} {
		if err := meter.Record(ctxA, usage); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	total, err := meter.MonthTokens(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000 {
		t.Fatalf("month tokens = %d, want 1000", total)
	}

	share, alarm, err := meter.PremiumShare(ctxA, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if share <= 0.20 || !alarm {
		t.Fatalf("premium share %f should trip the 20%% alarm", share)
	}

	// Tenant isolation: workspace B sees none of A's spend.
	totalB, err := meter.MonthTokens(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	if totalB != 0 {
		t.Fatalf("workspace B sees foreign usage: %d", totalB)
	}

	// A workspace-less call is a programming error, not an empty result.
	if err := meter.Record(ctx, ai.Usage{Task: ai.TaskSummarize, Tier: ai.TierCheapCloud}); err == nil {
		t.Fatal("metering outside workspace context must fail")
	}

	// The counter row itself folded: one row, calls=2, one cached hit.
	var calls, cachedHits int64
	err = database.WithWorkspaceTx(ctxA, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT calls, cached_hits FROM ai_usage WHERE task = $1`, string(ai.TaskSummarize)).
			Scan(&calls, &cachedHits)
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || cachedHits != 1 {
		t.Fatalf("counter fold wrong: calls=%d cached=%d", calls, cachedHits)
	}
}

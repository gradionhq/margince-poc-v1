//go:build integration

package compose_test

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
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
)

func TestMeterAccumulatesUnderRLS(t *testing.T) {
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	newWorkspace := func(slug string) ids.UUID {
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
	wsA := newWorkspace("meter-a")
	wsB := newWorkspace("meter-b")

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

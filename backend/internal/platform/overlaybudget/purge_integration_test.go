// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlaybudget_test

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// purgeTestIncumbent is an arbitrary configured incumbent name — its
// identity doesn't matter to PurgeWorkspace, only that seedCounter's write
// lands under the seeded workspace's ovb:<ws>:… prefix.
const purgeTestIncumbent = "acme"

// purgeTestCfg configures purgeTestIncumbent just enough for the metering
// calls seedCounter makes (via the public ConsumeSearch API, not a
// hand-built key) to actually resolve and write.
func purgeTestCfg() overlaybudget.Config {
	return overlaybudget.Config{
		purgeTestIncumbent: {
			Search: overlaybudget.WindowConfig{Ceiling: 100, Cap: 50},
			REST:   overlaybudget.WindowConfig{Ceiling: 100, Cap: 50},
		},
	}
}

// budgetTestRedis wraps the package's shared budgettest.Client fixture with
// a context, since PurgeWorkspace (unlike the meter's other methods) takes
// its workspace as an explicit argument rather than reading it off ctx.
func budgetTestRedis(t *testing.T) (context.Context, *redis.Client) {
	t.Helper()
	return t.Context(), budgettest.Client(t)
}

// seedCounter writes one budget counter for ws through the meter's own
// public metering API (ConsumeSearch, which writes exactly one key — the
// per-second search window has no per-source breakdown) rather than
// hand-building a key, so the counter it creates is exactly what production
// traffic would leave behind.
func seedCounter(t *testing.T, ctx context.Context, rdb *redis.Client, ws ids.UUID) {
	t.Helper()
	m := overlaybudget.New(rdb, purgeTestCfg())
	wsCtx := principal.WithWorkspaceID(ctx, ws)
	if err := m.ConsumeSearch(wsCtx, purgeTestIncumbent, 1); err != nil {
		t.Fatalf("seeding counter: %v", err)
	}
}

// countCounters counts the keys under ws's prefix directly with KEYS,
// independent of PurgeWorkspace's own SCAN — so the assertion doesn't just
// re-check the method against itself.
func countCounters(t *testing.T, ctx context.Context, rdb *redis.Client, ws ids.UUID) int {
	t.Helper()
	keys, err := rdb.Keys(ctx, "ovb:"+ws.String()+":*").Result()
	if err != nil {
		t.Fatalf("counting counters: %v", err)
	}
	return len(keys)
}

func TestPurgeWorkspaceLeavesAnotherWorkspacesCounters(t *testing.T) {
	ctx, rdb := budgetTestRedis(t) // the package's existing budgettest harness
	mine, theirs := ids.NewV7(), ids.NewV7()
	seedCounter(t, ctx, rdb, mine)
	seedCounter(t, ctx, rdb, theirs)

	meter := overlaybudget.New(rdb, overlaybudget.Config{})
	deleted, err := meter.PurgeWorkspace(ctx, mine)
	if err != nil {
		t.Fatalf("PurgeWorkspace: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if n := countCounters(t, ctx, rdb, theirs); n != 1 {
		t.Errorf("another workspace's counters were purged (%d remain, want 1)", n)
	}
}

func TestPurgeWorkspaceOnAMeterWithoutRedisIsANoOp(t *testing.T) {
	// compose constructs a fail-closed meter with a nil client and rebinds it
	// later; a role that never rebound has no counters to purge and must not
	// fail the reset.
	deleted, err := overlaybudget.New(nil, overlaybudget.Config{}).PurgeWorkspace(context.Background(), ids.NewV7())
	if err != nil {
		t.Fatalf("PurgeWorkspace on a nil-client meter: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
}

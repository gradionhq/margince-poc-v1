// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlaybudget_test

// The OVB meter's contract against a real Redis: the shed gate that
// declines further live charges (OVB-AC-2), per-workspace/per-incumbent
// isolation (OVB-AC-3), the per-source breakdown reconciling to the REST
// total (OVB-AC-5), the `~unknown` headroom sentinel (OVB-AC-1), the
// per-second search burst gate, fixed-window rollover via the injected
// clock (T11 — no sleeps), and fail-closed answers when the meter cannot
// truthfully account. The meter is Redis-only (no Postgres), so a
// workspace-bound context is all the fixture it needs — no DB row.

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const testIncumbent = "hubspot"

// testCfg is a small, fast-to-exhaust config: REST cap 10 (shed at 9,
// warn at 7), search cap 5 (shed at 4). Ceilings sit above the caps only
// to satisfy the config's own cap<ceiling invariant; the meter never reads
// the ceiling (headroom against it is always ~unknown).
func testCfg() overlaybudget.Config {
	return overlaybudget.Config{
		testIncumbent: {
			Search:       overlaybudget.WindowConfig{Ceiling: 10, Cap: 5},
			REST:         overlaybudget.WindowConfig{Ceiling: 100, Cap: 10},
			WarnFraction: 0.7,
			ShedFraction: 0.9,
		},
	}
}

func testRedisDB(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("MARGINCE_TEST_REDIS_DB")
	if raw == "" {
		return 15
	}
	db, err := strconv.Atoi(raw)
	if err != nil || db < 1 || db > 15 {
		t.Fatalf("MARGINCE_TEST_REDIS_DB=%q is not a Redis db index in 1..15", raw)
	}
	return db
}

// testRedis returns a flushed client on the isolated test db, failing
// loudly (never skipping) when Redis is not provisioned.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("MARGINCE_TEST_REDIS")
	if addr == "" {
		t.Fatal("MARGINCE_TEST_REDIS not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: testRedisDB(t)})
	ctx := t.Context()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis at %s unreachable — run `make db-up`: %v", addr, err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushing test redis db: %v", err)
	}
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("closing redis: %v", err)
		}
	})
	return rdb
}

// wsCtx binds a fresh workspace id — each test gets its own so counters
// never leak between tests (the meter keys on the workspace).
func wsCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), ids.NewV7())
}

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestReserveRESTDeclinesAtShedAndRecordsBelow(t *testing.T) {
	rdb := testRedis(t)
	m := overlaybudget.NewWithClock(rdb, testCfg(), fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)))
	ctx := wsCtx()

	// REST cap 10, shed threshold = floor(0.9*10) = 9. Reservations 1..9
	// are below the threshold and allowed; the 10th observes total=9 (>=9)
	// and is declined, recording nothing.
	for i := 1; i <= 9; i++ {
		ok, err := m.ReserveREST(ctx, testIncumbent, overlaybudget.SourceForceFresh, 1)
		if err != nil {
			t.Fatalf("ReserveREST #%d: %v", i, err)
		}
		if !ok {
			t.Fatalf("ReserveREST #%d declined below the shed threshold", i)
		}
	}
	if got := m.Snapshot(ctx, testIncumbent).Consumed; got != 9 {
		t.Fatalf("consumed after 9 reservations = %d, want 9", got)
	}
	ok, err := m.ReserveREST(ctx, testIncumbent, overlaybudget.SourceForceFresh, 1)
	if err != nil {
		t.Fatalf("ReserveREST (shed): %v", err)
	}
	if ok {
		t.Fatal("ReserveREST at the shed threshold was allowed, want declined (OVB-AC-2)")
	}
	if got := m.Snapshot(ctx, testIncumbent).Consumed; got != 9 {
		t.Fatalf("a declined reservation recorded a charge: consumed = %d, want 9", got)
	}
}

func TestBreakdownSumsToRESTTotal(t *testing.T) {
	rdb := testRedis(t)
	m := overlaybudget.NewWithClock(rdb, testCfg(), fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)))
	ctx := wsCtx()

	mustReserve(ctx, t, m, overlaybudget.SourceForceFresh, 3)
	if err := m.ConsumeREST(ctx, testIncumbent, overlaybudget.SourcePoller, 4); err != nil {
		t.Fatalf("ConsumeREST poller: %v", err)
	}
	snap := m.Snapshot(ctx, testIncumbent)
	if snap.Breakdown[overlaybudget.SourceForceFresh] != 3 || snap.Breakdown[overlaybudget.SourcePoller] != 4 {
		t.Fatalf("breakdown = %+v, want force_fresh=3 poller=4", snap.Breakdown)
	}
	sum := 0
	for _, v := range snap.Breakdown {
		sum += v
	}
	if sum != snap.Consumed {
		t.Fatalf("per-source breakdown %d does not sum to the REST total %d (OVB-AC-5)", sum, snap.Consumed)
	}
	if snap.Headroom != overlaybudget.UnknownHeadroom {
		t.Fatalf("headroom = %q, want the %q sentinel (OVB-AC-1)", snap.Headroom, overlaybudget.UnknownHeadroom)
	}
}

func TestWorkspaceAndIncumbentIsolation(t *testing.T) {
	rdb := testRedis(t)
	m := overlaybudget.NewWithClock(rdb, twoIncumbentCfg(), fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)))
	ctxA, ctxB := wsCtx(), wsCtx()

	// Drive workspace A's hubspot REST window to shed.
	for i := 0; i < 9; i++ {
		mustReserve(ctxA, t, m, overlaybudget.SourceForceFresh, 1)
	}
	if got := m.BandREST(ctxA, testIncumbent); got != overlaybudget.BandShed {
		t.Fatalf("workspace A band = %q, want shed", got)
	}
	// A different workspace is untouched (OVB-AC-3, per-connection).
	if got := m.BandREST(ctxB, testIncumbent); got != overlaybudget.BandOK {
		t.Fatalf("workspace B band = %q, want ok — A's spend must not leak", got)
	}
	// A different incumbent on the SAME workspace is untouched (per-incumbent).
	if got := m.BandREST(ctxA, "salesforce"); got != overlaybudget.BandOK {
		t.Fatalf("workspace A salesforce band = %q, want ok — hubspot's spend must not leak across incumbents", got)
	}
}

func TestSearchWindowMeteredAndRollsOverPerSecond(t *testing.T) {
	rdb := testRedis(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := overlaybudget.NewWithClock(rdb, testCfg(), clock)
	ctx := wsCtx()

	// The search window is METERED, not gated (the poller cannot abort a
	// paginated scan). Record 5 requests in one second; with cap 5 the band
	// crosses to shed (5 >= floor(0.9*5)=4) but recording never declines.
	for i := 0; i < 5; i++ {
		if err := m.ConsumeSearch(ctx, testIncumbent, 1); err != nil {
			t.Fatalf("ConsumeSearch #%d: %v", i, err)
		}
	}
	snap := m.Snapshot(ctx, testIncumbent)
	if snap.SearchConsumed != 5 {
		t.Fatalf("search consumed = %d, want 5", snap.SearchConsumed)
	}
	if snap.SearchBand != overlaybudget.BandShed {
		t.Fatalf("search band at 5/5 = %q, want shed", snap.SearchBand)
	}

	// Advance one second: a fresh per-second window (fixed 1s bucket).
	now = now.Add(time.Second)
	rolled := m.Snapshot(ctx, testIncumbent)
	if rolled.SearchConsumed != 0 {
		t.Fatalf("search consumed after 1s rollover = %d, want 0", rolled.SearchConsumed)
	}
	if rolled.SearchBand != overlaybudget.BandOK {
		t.Fatalf("search band after rollover = %q, want ok", rolled.SearchBand)
	}
}

func TestRESTWindowRollsOverAtDayBoundary(t *testing.T) {
	rdb := testRedis(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := overlaybudget.NewWithClock(rdb, testCfg(), clock)
	ctx := wsCtx()

	for i := 0; i < 9; i++ {
		mustReserve(ctx, t, m, overlaybudget.SourceForceFresh, 1)
	}
	if got := m.BandREST(ctx, testIncumbent); got != overlaybudget.BandShed {
		t.Fatalf("band before rollover = %q, want shed", got)
	}
	// Advance a full day: a fresh REST window.
	now = now.Add(24 * time.Hour)
	if got := m.BandREST(ctx, testIncumbent); got != overlaybudget.BandOK {
		t.Fatalf("band after day rollover = %q, want ok (fresh window)", got)
	}
	if got := m.Snapshot(ctx, testIncumbent).Consumed; got != 0 {
		t.Fatalf("consumed after day rollover = %d, want 0", got)
	}
}

func TestBandCrossesWarnAndShed(t *testing.T) {
	rdb := testRedis(t)
	m := overlaybudget.NewWithClock(rdb, testCfg(), fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)))
	ctx := wsCtx()

	// cap 10, warn at 7 (0.7*10), shed at 9 (0.9*10).
	for i := 0; i < 6; i++ {
		mustReserve(ctx, t, m, overlaybudget.SourcePoller, 1)
	}
	if got := m.BandREST(ctx, testIncumbent); got != overlaybudget.BandOK {
		t.Fatalf("band at 6/10 = %q, want ok", got)
	}
	mustReserve(ctx, t, m, overlaybudget.SourcePoller, 1) // 7 → warn
	if got := m.BandREST(ctx, testIncumbent); got != overlaybudget.BandWarn {
		t.Fatalf("band at 7/10 = %q, want warn", got)
	}
	mustReserve(ctx, t, m, overlaybudget.SourcePoller, 1) // 8
	mustReserve(ctx, t, m, overlaybudget.SourcePoller, 1) // 9 → shed
	if got := m.BandREST(ctx, testIncumbent); got != overlaybudget.BandShed {
		t.Fatalf("band at 9/10 = %q, want shed", got)
	}
}

func TestFailClosed(t *testing.T) {
	rdb := testRedis(t)
	cfg := testCfg()
	m := overlaybudget.NewWithClock(rdb, cfg, fixedClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)))

	// No workspace bound → shed / declined / no record.
	bare := context.Background()
	if got := m.BandREST(bare, testIncumbent); got != overlaybudget.BandShed {
		t.Fatalf("BandREST with no workspace = %q, want shed", got)
	}
	if ok, _ := m.ReserveREST(bare, testIncumbent, overlaybudget.SourceForceFresh, 1); ok {
		t.Fatal("ReserveREST with no workspace was allowed, want declined")
	}
	// ConsumeSearch/ConsumeREST no-op (record nothing) with no workspace —
	// they never error, and the read side stays fail-closed shed above.
	if err := m.ConsumeSearch(bare, testIncumbent, 1); err != nil {
		t.Fatalf("ConsumeSearch with no workspace should be a silent no-op, got %v", err)
	}

	// Unconfigured incumbent → shed / declined.
	ctx := wsCtx()
	if got := m.BandREST(ctx, "unconfigured"); got != overlaybudget.BandShed {
		t.Fatalf("BandREST for an unconfigured incumbent = %q, want shed", got)
	}
	if ok, _ := m.ReserveREST(ctx, "unconfigured", overlaybudget.SourceForceFresh, 1); ok {
		t.Fatal("ReserveREST for an unconfigured incumbent was allowed, want declined")
	}

	// Nil Redis → shed everywhere (a role with no Redis never spends live
	// quota it cannot account for).
	noRedis := overlaybudget.New(nil, cfg)
	if got := noRedis.BandREST(ctx, testIncumbent); got != overlaybudget.BandShed {
		t.Fatalf("BandREST with no Redis = %q, want shed", got)
	}
	if ok, _ := noRedis.ReserveREST(ctx, testIncumbent, overlaybudget.SourceForceFresh, 1); ok {
		t.Fatal("ReserveREST with no Redis was allowed, want declined")
	}
	if snap := noRedis.Snapshot(ctx, testIncumbent); snap.Band != overlaybudget.BandShed || snap.Consumed != 0 {
		t.Fatalf("Snapshot with no Redis = %+v, want shed/0", snap)
	}
}

func twoIncumbentCfg() overlaybudget.Config {
	cfg := testCfg()
	cfg["salesforce"] = cfg[testIncumbent]
	return cfg
}

func mustReserve(ctx context.Context, t *testing.T, m *overlaybudget.Meter, src overlaybudget.Source, n int) {
	t.Helper()
	ok, err := m.ReserveREST(ctx, testIncumbent, src, n)
	if err != nil {
		t.Fatalf("ReserveREST: %v", err)
	}
	if !ok {
		t.Fatal("ReserveREST unexpectedly declined")
	}
}

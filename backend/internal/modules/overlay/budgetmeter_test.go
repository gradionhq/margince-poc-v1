// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// Meter's contract against the real, shared Postgres window
// (overlay_budget_window): the ok/warn/shed band arithmetic, the
// per-workspace fixed-window reset (via NewMeterWithClock's injected
// clock — T11, no time.Sleep), Reserve's cross-transaction atomicity
// (the property that makes the counter safe to share between cmd/api and
// cmd/worker), and the fail-closed answers a context with no workspace
// bound gets from Band/Snapshot. The counter lives in the database now,
// so this is an integration test, not a pure-unit one: the row-lock that
// serializes concurrent Reserves is a Postgres row lock, provable only
// against Postgres. Consume's use from a real caller (reconcile.go,
// freshness.go) is proven by their own tests.
//
// Each test mints its OWN workspace via testWorkspaceCtx, so one test's
// consumption can never leak into another's band assertion (and the FK
// to workspace(id) the counter carries is satisfied by a real row).

import (
	"sync"
	"testing"
	"time"
)

func testMeterCfg() MeterConfig {
	return MeterConfig{Window: time.Hour, Limit: 100, WarnFraction: 0.7, ShedFraction: 0.9}
}

// fixedClock is a stable clock for the meter tests that assert band
// arithmetic rather than window expiry — NewMeterWithClock over it keeps
// those tests off the real wall clock (T11), with no window ever elapsing
// mid-test to reset a counter out from under an assertion.
func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

// TestMeterReserveRecordsOnlyWhenNotShed proves the atomic check-and-record
// FreshnessReader relies on: under budget Reserve allows and records the
// spend; once shed it refuses AND records nothing — the two decided under
// one row lock so concurrent force-fresh reads can't collectively overshoot.
func TestMeterReserveRecordsOnlyWhenNotShed(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock()) // Limit 100, shed 0.9

	allowed, err := m.Reserve(ctx, LaneForceFresh, 1)
	if err != nil {
		t.Fatalf("Reserve (under budget): %v", err)
	}
	if !allowed {
		t.Fatal("Reserve under budget = not allowed, want allowed")
	}
	if got := m.Snapshot(ctx).Consumed; got != 1 {
		t.Fatalf("consumed after an allowed Reserve = %d, want 1", got)
	}

	// Push total to the shed threshold (>= 0.9*100), then Reserve must
	// refuse and record nothing.
	if err := m.Consume(ctx, LanePoller, 89); err != nil { // total 90 >= 90
		t.Fatalf("Consume to shed: %v", err)
	}
	before := m.Snapshot(ctx).Consumed
	allowed, err = m.Reserve(ctx, LaneForceFresh, 1)
	if err != nil {
		t.Fatalf("Reserve (shed): %v", err)
	}
	if allowed {
		t.Fatal("Reserve in the shed band = allowed, want refused")
	}
	if got := m.Snapshot(ctx).Consumed; got != before {
		t.Fatalf("a refused Reserve recorded %d units, want 0", got-before)
	}
}

// TestMeterReserveIsAtomicUnderConcurrency proves Reserve's whole point:
// the band check and the spend record happen under one Postgres row lock,
// so a burst of concurrent force-fresh reservations near the shed
// threshold cannot collectively overshoot the budget — the property that
// makes the counter safe to share across cmd/api and cmd/worker. Primed at
// 88/100 (shed at 90), 20 concurrent Reserves — each its own transaction
// over the pool — must allow EXACTLY 2 (88→89, 89→90, then shed) and leave
// the total at 90. A non-atomic band-then-record would let several
// transactions all observe 88/89 and push the total past the threshold.
func TestMeterReserveIsAtomicUnderConcurrency(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock()) // Limit 100, shed 0.9 → shed at 90
	if err := m.Consume(ctx, LanePoller, 88); err != nil {
		t.Fatalf("priming the window to 88: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := m.Reserve(ctx, LaneForceFresh, 1)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Reserve errored: %v", err)
	}

	if allowed != 2 {
		t.Fatalf("allowed concurrent reservations = %d, want exactly 2 (88→89→90, then shed)", allowed)
	}
	if got := m.Snapshot(ctx).Consumed; got != 90 {
		t.Fatalf("consumed after the concurrent burst = %d, want 90 — Reserve must never overshoot the shed threshold", got)
	}
}

func TestMeterBandStartsOK(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock())

	if got := m.Band(ctx); got != BandOK {
		t.Fatalf("Band() on an untouched window = %q, want %q", got, BandOK)
	}
}

func TestMeterBandCrossesWarnAndShedThresholds(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock()) // Limit 100, warn 0.7, shed 0.9

	if err := m.Consume(ctx, LaneForceFresh, 65); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != BandOK {
		t.Fatalf("Band() at 65/100 = %q, want %q", got, BandOK)
	}

	if err := m.Consume(ctx, LanePoller, 10); err != nil { // total 75 >= 70
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != BandWarn {
		t.Fatalf("Band() at 75/100 = %q, want %q", got, BandWarn)
	}

	if err := m.Consume(ctx, LanePoller, 20); err != nil { // total 95 >= 90
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != BandShed {
		t.Fatalf("Band() at 95/100 = %q, want %q", got, BandShed)
	}
}

func TestMeterNonPositiveLimitAlwaysSheds(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, MeterConfig{Window: time.Hour, Limit: 0, WarnFraction: 0.7, ShedFraction: 0.9}, fixedClock())

	if got := m.Band(ctx); got != BandShed {
		t.Fatalf("Band() with a non-positive Limit = %q, want %q (fail closed)", got, BandShed)
	}
}

func TestMeterBandAndSnapshotFailClosedWithNoWorkspaceBound(t *testing.T) {
	_, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock())
	ctx := t.Context() // no workspace bound → WithWorkspaceTx fails

	if got := m.Band(ctx); got != BandShed {
		t.Fatalf("Band() with no workspace bound = %q, want %q (fail closed)", got, BandShed)
	}
	snap := m.Snapshot(ctx)
	if snap.Band != BandShed || snap.Consumed != 0 {
		t.Fatalf("Snapshot() with no workspace bound = %+v, want Band=shed Consumed=0", snap)
	}
}

func TestMeterConsumeRequiresAWorkspaceBoundContext(t *testing.T) {
	_, pool, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock())
	if err := m.Consume(t.Context(), LaneForceFresh, 1); err == nil {
		t.Fatal("Consume with no workspace bound: want an error, got nil")
	}
}

// TestMeterWindowResetsAfterExpiry proves the FIXED window reset: once
// the injected clock advances past cfg.Window, the next Consume/Band
// call rolls into a brand-new window rather than accumulating against the
// stale one — proven deterministically via NewMeterWithClock (the clock's
// reading is the SQL $now parameter), never by sleeping against the real
// clock (T11).
func TestMeterWindowResetsAfterExpiry(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := NewMeterWithClock(pool, testMeterCfg(), clock)

	if err := m.Consume(ctx, LaneForceFresh, 95); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != BandShed {
		t.Fatalf("Band() at 95/100 = %q, want %q", got, BandShed)
	}

	now = now.Add(testMeterCfg().Window + time.Second) // advance past the window
	if got := m.Band(ctx); got != BandOK {
		t.Fatalf("Band() after window expiry = %q, want %q (a fresh window)", got, BandOK)
	}
	snap := m.Snapshot(ctx)
	if snap.Consumed != 0 {
		t.Fatalf("Snapshot().Consumed after window expiry = %d, want 0", snap.Consumed)
	}
}

// TestMeterWindowResetOnWriteAfterExpiry proves the reset is a WRITE-path
// roll, not only a read-path zeroing: after the window expires, a Consume
// starts a fresh window at exactly the units it records (not stale + new),
// so the shared row a later reader sees reflects the new window alone.
func TestMeterWindowResetOnWriteAfterExpiry(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := NewMeterWithClock(pool, testMeterCfg(), clock)

	if err := m.Consume(ctx, LaneForceFresh, 80); err != nil {
		t.Fatalf("Consume (first window): %v", err)
	}
	now = now.Add(testMeterCfg().Window + time.Second) // expire the window
	if err := m.Consume(ctx, LanePoller, 5); err != nil {
		t.Fatalf("Consume (rolled window): %v", err)
	}
	if got := m.Snapshot(ctx).Consumed; got != 5 {
		t.Fatalf("consumed after a post-expiry Consume = %d, want 5 (fresh window, not 85)", got)
	}
}

// TestMeterSnapshotReportsWindowLimitAndBand proves Snapshot's full
// shape — the GetOverlayBudget wire read's own source of truth.
func TestMeterSnapshotReportsWindowLimitAndBand(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	cfg := testMeterCfg()
	m := NewMeterWithClock(pool, cfg, fixedClock())

	if err := m.Consume(ctx, LaneForceFresh, 42); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	snap := m.Snapshot(ctx)
	if snap.Consumed != 42 {
		t.Fatalf("Snapshot().Consumed = %d, want 42", snap.Consumed)
	}
	if snap.Limit != cfg.Limit {
		t.Fatalf("Snapshot().Limit = %d, want %d", snap.Limit, cfg.Limit)
	}
	if snap.Window != cfg.Window.String() {
		t.Fatalf("Snapshot().Window = %q, want %q", snap.Window, cfg.Window.String())
	}
	if snap.Band != BandOK {
		t.Fatalf("Snapshot().Band = %q, want %q", snap.Band, BandOK)
	}
}

// TestMeterConsumeIsSharedAcrossInstances proves the point of the whole
// change: two SEPARATE Meter instances over the same pool (as cmd/api's
// force_fresh meter and cmd/worker's poller meter are two distinct
// instances in two processes) share ONE per-workspace count — a spend
// through one is visible to the other, so they cannot each burn a full
// budget against the shared incumbent quota.
func TestMeterConsumeIsSharedAcrossInstances(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	clock := fixedClock()
	apiMeter := NewMeterWithClock(pool, testMeterCfg(), clock)
	workerMeter := NewMeterWithClock(pool, testMeterCfg(), clock)

	if err := workerMeter.Consume(ctx, LanePoller, 75); err != nil { // worker spends
		t.Fatalf("worker Consume: %v", err)
	}
	// The api-side meter, a different instance, must see the worker's spend.
	if got := apiMeter.Snapshot(ctx).Consumed; got != 75 {
		t.Fatalf("api meter sees consumed = %d, want 75 (the worker's spend on the shared window)", got)
	}
	if got := apiMeter.Band(ctx); got != BandWarn { // 75/100 >= 70
		t.Fatalf("api meter Band = %q, want %q — it must reflect the shared count", got, BandWarn)
	}
}

// TestMeterWorkspacesAreIsolated proves the count is per-workspace: a
// spend in one workspace never moves another's band, even through meters
// over the same pool.
func TestMeterWorkspacesAreIsolated(t *testing.T) {
	ctxA, pool, _ := testWorkspaceCtx(t)
	ctxB, poolB, _ := testWorkspaceCtx(t)
	m := NewMeterWithClock(pool, testMeterCfg(), fixedClock())
	mB := NewMeterWithClock(poolB, testMeterCfg(), fixedClock())

	if err := m.Consume(ctxA, LaneForceFresh, 95); err != nil { // A into shed
		t.Fatalf("workspace A Consume: %v", err)
	}
	if got := m.Band(ctxA); got != BandShed {
		t.Fatalf("workspace A Band = %q, want %q", got, BandShed)
	}
	if got := mB.Band(ctxB); got != BandOK {
		t.Fatalf("workspace B Band = %q, want %q — A's spend must not leak into B", got, BandOK)
	}
}

// TestDefaultMeterConfigShape pins the placeholder OVB numbers this
// build ships pending an upstream-pinned OVA-PARAM value (budgetmeter.go's
// own doc) — a regression here silently changes production behavior with
// no test signal otherwise.
func TestDefaultMeterConfigShape(t *testing.T) {
	cfg := DefaultMeterConfig()
	if cfg.Window != time.Hour {
		t.Fatalf("DefaultMeterConfig().Window = %v, want 1h", cfg.Window)
	}
	if cfg.Limit != 3000 {
		t.Fatalf("DefaultMeterConfig().Limit = %d, want 3000", cfg.Limit)
	}
	if cfg.WarnFraction != 0.7 || cfg.ShedFraction != 0.9 {
		t.Fatalf("DefaultMeterConfig() fractions = (%v, %v), want (0.7, 0.9)", cfg.WarnFraction, cfg.ShedFraction)
	}
}

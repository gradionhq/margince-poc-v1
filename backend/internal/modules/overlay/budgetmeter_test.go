// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

// Meter's unit-level proof: the ok/warn/shed band arithmetic, the
// per-workspace fixed-window reset (via NewMeterWithClock's injected
// clock — T11, no time.Sleep), and the fail-closed answers a
// context with no workspace bound gets from Band/Snapshot. Consume's
// lane-crossing accumulation and the meter's use from a real caller
// (reconcile.go, freshness.go) are proven by their own tests; this file
// is the meter's own contract in isolation.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func testMeterCfg() overlay.MeterConfig {
	return overlay.MeterConfig{Window: time.Hour, Limit: 100, WarnFraction: 0.7, ShedFraction: 0.9}
}

// fixedClock is a stable clock for the meter tests that assert band
// arithmetic rather than window expiry — NewMeterWithClock over it keeps
// those tests off the real wall clock (T11), with no window ever elapsing
// mid-test to reset a counter out from under an assertion.
func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}

func wsCtx() context.Context {
	return principal.WithWorkspaceID(context.Background(), mustWorkspaceID())
}

// mustWorkspaceID mints a fresh workspace id for a test's own isolated
// meter state — tests never share a workspace id, so one test's
// consumption can never leak into another's band assertion.
func mustWorkspaceID() (id [16]byte) {
	// A fixed, non-zero id is enough here: each test constructs its own
	// Meter instance, so there is no cross-test window state to collide
	// with even if two tests reused the same workspace id.
	id[0] = 1
	return id
}

// TestMeterReserveRecordsOnlyWhenNotShed proves the atomic check-and-record
// FreshnessReader relies on: under budget Reserve allows and records the
// spend; once shed it refuses AND records nothing — the two decided under
// one lock so concurrent force-fresh reads can't collectively overshoot.
func TestMeterReserveRecordsOnlyWhenNotShed(t *testing.T) {
	m := overlay.NewMeterWithClock(testMeterCfg(), fixedClock()) // Limit 100, shed 0.9
	ctx := wsCtx()

	allowed, err := m.Reserve(ctx, overlay.LaneForceFresh, 1)
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
	if err := m.Consume(ctx, overlay.LanePoller, 89); err != nil { // total 90 >= 90
		t.Fatalf("Consume to shed: %v", err)
	}
	before := m.Snapshot(ctx).Consumed
	allowed, err = m.Reserve(ctx, overlay.LaneForceFresh, 1)
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
// the band check and the spend record happen under one lock, so a burst of
// concurrent force-fresh reservations near the shed threshold cannot
// collectively overshoot the budget. Primed at 88/100 (shed at 90), 20
// concurrent Reserves must allow EXACTLY 2 (88→89, 89→90, then shed) and
// leave the total at 90 — a non-atomic band-then-record would let several
// goroutines all observe 88/89 and push the total past the threshold.
func TestMeterReserveIsAtomicUnderConcurrency(t *testing.T) {
	m := overlay.NewMeterWithClock(testMeterCfg(), fixedClock()) // Limit 100, shed 0.9 → shed at 90
	ctx := wsCtx()
	if err := m.Consume(ctx, overlay.LanePoller, 88); err != nil {
		t.Fatalf("priming the window to 88: %v", err)
	}

	const goroutines = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := m.Reserve(ctx, overlay.LaneForceFresh, 1)
			if err != nil {
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

	if allowed != 2 {
		t.Fatalf("allowed concurrent reservations = %d, want exactly 2 (88→89→90, then shed)", allowed)
	}
	if got := m.Snapshot(ctx).Consumed; got != 90 {
		t.Fatalf("consumed after the concurrent burst = %d, want 90 — Reserve must never overshoot the shed threshold", got)
	}
}

func TestMeterBandStartsOK(t *testing.T) {
	m := overlay.NewMeterWithClock(testMeterCfg(), fixedClock())
	ctx := wsCtx()

	if got := m.Band(ctx); got != overlay.BandOK {
		t.Fatalf("Band() on an untouched window = %q, want %q", got, overlay.BandOK)
	}
}

func TestMeterBandCrossesWarnAndShedThresholds(t *testing.T) {
	m := overlay.NewMeterWithClock(testMeterCfg(), fixedClock()) // Limit 100, warn 0.7, shed 0.9
	ctx := wsCtx()

	if err := m.Consume(ctx, overlay.LaneForceFresh, 65); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != overlay.BandOK {
		t.Fatalf("Band() at 65/100 = %q, want %q", got, overlay.BandOK)
	}

	if err := m.Consume(ctx, overlay.LanePoller, 10); err != nil { // total 75 >= 70
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != overlay.BandWarn {
		t.Fatalf("Band() at 75/100 = %q, want %q", got, overlay.BandWarn)
	}

	if err := m.Consume(ctx, overlay.LanePoller, 20); err != nil { // total 95 >= 90
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != overlay.BandShed {
		t.Fatalf("Band() at 95/100 = %q, want %q", got, overlay.BandShed)
	}
}

func TestMeterNonPositiveLimitAlwaysSheds(t *testing.T) {
	m := overlay.NewMeterWithClock(overlay.MeterConfig{Window: time.Hour, Limit: 0, WarnFraction: 0.7, ShedFraction: 0.9}, fixedClock())
	ctx := wsCtx()

	if got := m.Band(ctx); got != overlay.BandShed {
		t.Fatalf("Band() with a non-positive Limit = %q, want %q (fail closed)", got, overlay.BandShed)
	}
}

func TestMeterBandAndSnapshotFailClosedWithNoWorkspaceBound(t *testing.T) {
	m := overlay.NewMeterWithClock(testMeterCfg(), fixedClock())
	ctx := context.Background() // no workspace bound

	if got := m.Band(ctx); got != overlay.BandShed {
		t.Fatalf("Band() with no workspace bound = %q, want %q (fail closed)", got, overlay.BandShed)
	}
	snap := m.Snapshot(ctx)
	if snap.Band != overlay.BandShed || snap.Consumed != 0 {
		t.Fatalf("Snapshot() with no workspace bound = %+v, want Band=shed Consumed=0", snap)
	}
}

func TestMeterConsumeRequiresAWorkspaceBoundContext(t *testing.T) {
	m := overlay.NewMeterWithClock(testMeterCfg(), fixedClock())
	if err := m.Consume(context.Background(), overlay.LaneForceFresh, 1); err == nil {
		t.Fatal("Consume with no workspace bound: want an error, got nil")
	}
}

// TestMeterWindowResetsAfterExpiry proves the FIXED window reset: once
// the injected clock advances past cfg.Window, the next Consume/Band
// call starts a brand-new window rather than accumulating against the
// stale one — proven deterministically via NewMeterWithClock, never by
// sleeping against the real clock (T11).
func TestMeterWindowResetsAfterExpiry(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	m := overlay.NewMeterWithClock(testMeterCfg(), clock)
	ctx := wsCtx()

	if err := m.Consume(ctx, overlay.LaneForceFresh, 95); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := m.Band(ctx); got != overlay.BandShed {
		t.Fatalf("Band() at 95/100 = %q, want %q", got, overlay.BandShed)
	}

	now = now.Add(testMeterCfg().Window + time.Second) // advance past the window
	if got := m.Band(ctx); got != overlay.BandOK {
		t.Fatalf("Band() after window expiry = %q, want %q (a fresh window)", got, overlay.BandOK)
	}
	snap := m.Snapshot(ctx)
	if snap.Consumed != 0 {
		t.Fatalf("Snapshot().Consumed after window expiry = %d, want 0", snap.Consumed)
	}
}

// TestMeterSnapshotReportsWindowLimitAndBand proves Snapshot's full
// shape — the GetOverlayBudget wire read's own source of truth.
func TestMeterSnapshotReportsWindowLimitAndBand(t *testing.T) {
	cfg := testMeterCfg()
	m := overlay.NewMeterWithClock(cfg, fixedClock())
	ctx := wsCtx()

	if err := m.Consume(ctx, overlay.LaneForceFresh, 42); err != nil {
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
	if snap.Band != overlay.BandOK {
		t.Fatalf("Snapshot().Band = %q, want %q", snap.Band, overlay.BandOK)
	}
}

// TestDefaultMeterConfigShape pins the placeholder OVB numbers this
// build ships pending an upstream-pinned OVA-PARAM value (budgetmeter.go's
// own doc) — a regression here silently changes production behavior with
// no test signal otherwise.
func TestDefaultMeterConfigShape(t *testing.T) {
	cfg := overlay.DefaultMeterConfig()
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

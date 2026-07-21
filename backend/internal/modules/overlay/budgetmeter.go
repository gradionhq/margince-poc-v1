// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The OVB (overlay budget) meter (design.md §4.5 S2, OVA-EVT-3): a
// per-workspace fixed-window consumption counter over the shared
// HubSpot API quota, shared by its lanes — force_fresh
// (FreshnessReader.Read) and poller (metered at its own call site,
// reconcile.go). Band() answers ok/warn/shed
// against the window's total consumption so FreshnessReader can decide
// whether a force-fresh read is affordable; Snapshot() answers the
// GET /overlay/budget read surface.
//
// Storage: in-memory, per-PROCESS, per-workspace. ADR-0054/A69 already
// runs FOUR separate cmd/<role> binaries in normal operation — cmd/api (this
// meter's force_fresh caller) and cmd/worker (the poller
// caller) are two DISTINCT OS processes today, each with
// its OWN Meter instance and its OWN in-memory counters, even with a
// single replica of each. They do NOT share this counter. So the
// combined spend against the ONE real HubSpot quota a workspace holds
// is bounded by (process count) x Limit, not by Limit — exactly the
// "don't starve the shared quota" property this meter exists to
// protect, undercut by construction the moment more than one role
// process reads/writes for the same workspace. This is not a
// multi-replica-only concern; it is true right now, with one replica
// of each of the two roles.
//
// TODO(branch-1b/pre-GA, design.md#5.1): replace this per-process counter
// with a shared PG/Redis counter before customer-GA — required so
// api+worker don't each spend a full budget against the shared incumbent
// quota (OVA-PARAM-8/9). Gated the same way design.md §5.1 gates the
// branch-1b visibility floor: "no customer-GA until [the gate] pass[es]."
// Not built now because OVA-PARAM-8/9's own numeric values are still
// unpinned upstream (see DefaultMeterConfig's doc) — a shared counter
// with unspecified capacity/threshold parameters would be premature
// machinery, not a fix.
//
// The window itself is fixed (start, then reset after Window elapses),
// not sliding — good enough to protect a per-process slice of the quota
// without the bookkeeping a sliding window needs, and it mirrors an
// incumbent's own quota-reset cadence (a fixed per-hour/per-day window),
// which this build has no visibility into.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Lane names Consume's caller declares — the fixed lane set this
// package's meter and the poller call site share.
const (
	LaneForceFresh = "force_fresh"
	LanePoller     = "poller"
)

// Band names the meter's three degradation bands (crmcontracts'
// OverlayBudgetBand: ok/warn/shed).
const (
	BandOK   = "ok"
	BandWarn = "warn"
	BandShed = "shed"
)

// MeterConfig is the meter's window/threshold knobs. Limit and the two
// fractions have no OVA-PARAM value pinned yet (design.md §5.1 notes
// OVA-PARAM-8/9 — the per-object freshness SLOs and latency budgets —
// are unset upstream; the OVB numeric limit is the same kind of gap);
// DefaultMeterConfig's numbers are this build's honest placeholder, not
// fabricated as if the spec had already pinned them.
type MeterConfig struct {
	// Window is the fixed-window duration the meter resets its
	// per-workspace counters on.
	Window time.Duration
	// Limit is the total spend (across all three lanes) the window
	// allows before the shed band takes over entirely.
	Limit int
	// WarnFraction/ShedFraction are the consumed/Limit ratios at which
	// Band moves from ok->warn and warn->shed (0 < WarnFraction <
	// ShedFraction <= 1).
	WarnFraction float64
	ShedFraction float64
}

// DefaultMeterConfig is the placeholder OVB configuration this build
// runs with pending an upstream-pinned OVA-PARAM value: a one-hour
// window, warn at 70% and shed at 90% of a conservative 3,000-call
// budget (HubSpot's own default private-app burst limit is ~100
// req/10s ≈ 36,000/hour; 3,000 leaves headroom for the poller lane on
// top of force-fresh reads).
func DefaultMeterConfig() MeterConfig {
	return MeterConfig{
		Window:       time.Hour,
		Limit:        3000,
		WarnFraction: 0.7,
		ShedFraction: 0.9,
	}
}

// Budget is the meter's own snapshot shape — the GetOverlayBudget
// handler maps it onto the wire crmcontracts.OverlayBudget (whose fields
// are all pointers/omitempty, an awkward shape to compute directly
// against; the name here drops the redundant "Overlay" prefix since it
// already lives in package overlay — revive's stutter check). Window is
// rendered as the config's Window duration's string form (e.g.
// "1h0m0s") — the handler decides the wire's exact window label; this is
// the meter's honest internal answer, not a formatted-for-display one.
type Budget struct {
	Window   string
	Consumed int
	Limit    int
	Band     string
}

// windowState is one workspace's current fixed window: when it started
// and how much each lane has spent inside it.
type windowState struct {
	start  time.Time
	byLane map[string]int
	total  int
}

// Meter is the OVB consumption meter: Consume records spend, Band
// answers the current degradation band, Snapshot answers the read
// surface. The zero value is not usable; construct with NewMeter or
// NewMeterWithClock.
type Meter struct {
	cfg MeterConfig
	now func() time.Time

	mu      sync.Mutex
	windows map[ids.UUID]*windowState
}

// NewMeter constructs a Meter over cfg using the real wall clock.
func NewMeter(cfg MeterConfig) *Meter {
	return NewMeterWithClock(cfg, time.Now)
}

// NewMeterWithClock takes the clock as a dependency so window expiry is
// a property tests assert by advancing time, not by sleeping against
// it (T11) — the same discipline platform/ratelimit.NewWithClock uses.
func NewMeterWithClock(cfg MeterConfig, now func() time.Time) *Meter {
	return &Meter{cfg: cfg, now: now, windows: make(map[ids.UUID]*windowState)}
}

// withState resolves ctx's workspace's current window (resetting it
// first if the prior window has expired) and runs fn against it WHILE
// STILL HOLDING m.mu — the whole read-or-modify operation is one
// critical section. This is load-bearing, not defensive style: an
// earlier version resolved the window under the lock, released it, and
// let Consume/Band/Snapshot re-acquire separately to read/mutate — a
// window that expired and got replaced in the gap between those two
// lock sections would silently lose a spend the caller believed it had
// recorded. A ctx with no workspace bound is a programming error (every
// operation context binds one — the HTTP middleware, or a job's own
// operation scope); it never runs fn against an empty workspace key.
func (m *Meter) withState(ctx context.Context, fn func(st *windowState)) error {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return fmt.Errorf("overlay: budget meter requires a workspace-bound context")
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.windows[wsID]
	if !ok || now.Sub(st.start) >= m.cfg.Window {
		st = &windowState{start: now, byLane: make(map[string]int)}
		m.windows[wsID] = st
	}
	fn(st)
	return nil
}

// Consume records n spend units against lane for ctx's workspace's
// current window.
func (m *Meter) Consume(ctx context.Context, lane string, n int) error {
	return m.withState(ctx, func(st *windowState) {
		st.byLane[lane] += n
		st.total += n
	})
}

// band computes the ok/warn/shed band for consumed against limit and
// the configured fractions.
func (cfg MeterConfig) band(consumed int) string {
	switch {
	case cfg.Limit <= 0:
		// A misconfigured (non-positive) limit can never be "under
		// budget" — fail closed to shed rather than divide by zero or
		// silently treat every read as free.
		return BandShed
	case float64(consumed) >= cfg.ShedFraction*float64(cfg.Limit):
		return BandShed
	case float64(consumed) >= cfg.WarnFraction*float64(cfg.Limit):
		return BandWarn
	default:
		return BandOK
	}
}

// Band reports ctx's workspace's current degradation band. A ctx with
// no workspace bound answers shed — the fail-closed direction: Band's
// only caller inside this package (FreshnessReader.Read) uses it to
// decide whether a force-fresh spend is affordable, and an unknown
// budget must never be read as "spend freely."
func (m *Meter) Band(ctx context.Context) string {
	var consumed int
	if err := m.withState(ctx, func(st *windowState) { consumed = st.total }); err != nil {
		return BandShed
	}
	return m.cfg.band(consumed)
}

// Snapshot answers the current window's consumption for ctx's
// workspace — the shape GetOverlayBudget reads. A ctx with no
// workspace bound answers the same fail-closed shed band Band does,
// with zero consumption (there is no window to report on).
func (m *Meter) Snapshot(ctx context.Context) Budget {
	var consumed int
	if err := m.withState(ctx, func(st *windowState) { consumed = st.total }); err != nil {
		return Budget{Window: m.cfg.Window.String(), Consumed: 0, Limit: m.cfg.Limit, Band: BandShed}
	}
	return Budget{
		Window:   m.cfg.Window.String(),
		Consumed: consumed,
		Limit:    m.cfg.Limit,
		Band:     m.cfg.band(consumed),
	}
}

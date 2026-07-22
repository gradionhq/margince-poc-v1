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
// Storage: a shared, cross-process PG counter (overlay_budget_window), ONE
// row per workspace. ADR-0054/A69 runs cmd/api (this meter's force_fresh
// caller) and cmd/worker (the poller caller) as two DISTINCT OS processes;
// both read and advance the SAME row, so a workspace's combined spend against
// its ONE real HubSpot quota is measured against ONE shared Limit — not
// (process count)×Limit, the "don't starve the shared quota" hole a
// per-process in-memory counter had by construction — and GET /overlay/budget
// reflects the poller's spend, not just the reading process's.
//
// "Measured", not hard-capped: force_fresh reads Reserve (they shed once the
// shared window crosses the threshold), but the poller lane Consumes
// unconditionally — an incumbent it must mirror is not something to refuse
// partway. So the shared count bounds what force_fresh will SPEND, and
// reports how close the poller is to the ceiling, but it does not itself cap
// the poller; a hard poller cap (a token bucket over this same shared row) is
// the coupled follow-up.
//
// The window is fixed (window_start + Window duration), not sliding: a
// consume/reserve whose clock reads past window_start+Window rolls the window
// (resets window_start and consumed) in the SAME statement, so a stale window
// can never over-count. It mirrors an incumbent's own fixed quota-reset
// cadence, which this build has no visibility into.
//
// "now" is the meter's injected clock, threaded into every statement as a
// parameter rather than SQL now(): window expiry stays a property tests
// assert by advancing the clock, never by sleeping (T11).
//
// OVA-PARAM-8/9 (the OVB numeric capacity/thresholds) are still unpinned
// upstream — DefaultMeterConfig's numbers are this build's honest placeholder
// (see its doc), now enforced by a shared counter rather than a per-process
// one.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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

// Meter is the OVB consumption meter: Consume records spend, Reserve
// atomically band-checks then records, Band answers the current
// degradation band, Snapshot answers the read surface. Every operation
// reads/advances the ONE overlay_budget_window row for ctx's workspace,
// so the count is shared across processes. The zero value is not usable;
// construct with NewMeter or NewMeterWithClock.
type Meter struct {
	cfg  MeterConfig
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewMeter constructs a Meter over cfg backed by pool, using the real
// wall clock.
func NewMeter(pool *pgxpool.Pool, cfg MeterConfig) *Meter {
	return NewMeterWithClock(pool, cfg, time.Now)
}

// NewMeterWithClock takes the clock as a dependency so window expiry is
// a property tests assert by advancing time, not by sleeping against
// it (T11): the clock's reading is threaded into every statement as the
// $now parameter (rather than SQL now()), so a test rolls the window by
// handing the meter a later time, deterministically.
func NewMeterWithClock(pool *pgxpool.Pool, cfg MeterConfig, now func() time.Time) *Meter {
	return &Meter{cfg: cfg, pool: pool, now: now}
}

// windowSeconds is the configured window as float seconds, the shape the
// SQL's make_interval(secs => $2) wants. Threading the window as a
// parameter (not a literal) keeps the roll condition — "now minus
// window_start has reached a full window" — identical across every
// statement below.
func (m *Meter) windowSeconds() float64 { return m.cfg.Window.Seconds() }

// upsertRollAdd is the shared upsert every WRITE runs: insert a fresh
// window (consumed = $3) if the workspace has none, else — in the SAME
// statement — roll the window when $now has reached window_start + Window
// (reset window_start to $now and consumed to $3) or otherwise ADD $3 to
// the running total. Because the roll and the add are one statement, a
// window that has just expired can never be added onto: it is reset
// first, atomically. $1=now, $2=window seconds, $3=units to add.
const upsertRollAdd = `
	INSERT INTO overlay_budget_window (workspace_id, window_start, consumed, updated_at)
	VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $3, $1)
	ON CONFLICT (workspace_id) DO UPDATE SET
		window_start = CASE WHEN $1 - overlay_budget_window.window_start >= make_interval(secs => $2)
			THEN $1 ELSE overlay_budget_window.window_start END,
		consumed = CASE WHEN $1 - overlay_budget_window.window_start >= make_interval(secs => $2)
			THEN $3 ELSE overlay_budget_window.consumed + $3 END,
		updated_at = $1`

// effectiveConsumed is the shared READ: the workspace's consumed count,
// but ZERO when the stored window has already expired ($now has reached
// window_start + Window) or no row exists yet. It never mutates, so a
// read of an expired window reports 0 without rolling it — the next write
// does the reset. $1=now, $2=window seconds.
const effectiveConsumed = `
	SELECT COALESCE((
		SELECT CASE WHEN $1 - window_start >= make_interval(secs => $2) THEN 0 ELSE consumed END
		FROM overlay_budget_window
		WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
	), 0)`

// Consume records n spend units for ctx's workspace's current window
// (rolling the window first if it has expired). A non-positive n is a
// no-op — the poller call site passes len(page.Records), which is 0 for
// an empty page, and an empty page spent no quota to record. The write
// runs inside WithWorkspaceTx so RLS binds it to ctx's workspace and the
// GUC the SQL reads is set.
func (m *Meter) Consume(ctx context.Context, lane string, n int) error {
	if n <= 0 {
		return nil
	}
	return database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, upsertRollAdd, m.now(), m.windowSeconds(), n); err != nil {
			return fmt.Errorf("overlay: recording %d %s budget units: %w", n, lane, err)
		}
		return nil
	})
}

// Reserve atomically checks ctx's workspace band and, if it is NOT shed,
// records n spend units and returns allowed=true; if the band is already
// shed it records nothing and returns allowed=false. The check and the
// record are ONE transaction holding the row lock the whole time: an
// earlier ROLL-and-lock upsert takes the row's write lock (and returns
// the post-roll consumed), the band is judged on that locked count, and
// the conditional add commits under the same lock — so N concurrent
// force-fresh reservations serialize through the row rather than all
// observing a non-shed band, all reaching the incumbent, and only then
// recording. Reserving BEFORE the live call also means a read that later
// FAILS still counts (its HTTP call spent quota all the same), unlike a
// Consume placed after the call that a failure path skips.
func (m *Meter) Reserve(ctx context.Context, lane string, n int) (bool, error) {
	if n <= 0 {
		// A non-positive reservation would either weaken enforcement (n=0
		// "reserves" nothing yet reports allowed) or, worse, lower the
		// window total (n<0). A caller asking to reserve nothing is a
		// programming error, refused rather than silently accepted.
		return false, fmt.Errorf("overlay: budget reserve requires a positive unit count, got %d", n)
	}
	allowed := false
	err := database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		// Roll-if-expired and take the row's write lock, returning the
		// current (post-roll) consumed. A 0-unit upsert here does not
		// advance the count — it exists only to create/roll the row and
		// hold its lock for the band decision below.
		var consumed int
		if err := tx.QueryRow(ctx, upsertRollAdd+`
			RETURNING consumed`, m.now(), m.windowSeconds(), 0).Scan(&consumed); err != nil {
			return fmt.Errorf("overlay: locking the budget window to reserve %d %s units: %w", n, lane, err)
		}
		if m.cfg.band(consumed) == BandShed {
			return nil // allowed stays false; nothing recorded
		}
		if _, err := tx.Exec(ctx, `
			UPDATE overlay_budget_window
			SET consumed = consumed + $1, updated_at = $2
			WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`,
			n, m.now()); err != nil {
			return fmt.Errorf("overlay: recording the reserved %d %s units: %w", n, lane, err)
		}
		allowed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return allowed, nil
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

// Band reports ctx's workspace's current degradation band. A read error
// — including a ctx with no workspace bound, which makes WithWorkspaceTx
// fail — answers shed, the fail-closed direction: Band's only caller
// inside this package (FreshnessReader.Read) uses it to decide whether a
// force-fresh spend is affordable, and an unknown budget must never be
// read as "spend freely."
func (m *Meter) Band(ctx context.Context) string {
	consumed, err := m.readConsumed(ctx)
	if err != nil {
		return BandShed
	}
	return m.cfg.band(consumed)
}

// Snapshot answers the current window's consumption for ctx's
// workspace — the shape GetOverlayBudget reads. A read error answers the
// same fail-closed shed band Band does, with zero consumption (there is
// no window it can trust to report on).
func (m *Meter) Snapshot(ctx context.Context) Budget {
	consumed, err := m.readConsumed(ctx)
	if err != nil {
		return Budget{Window: m.cfg.Window.String(), Consumed: 0, Limit: m.cfg.Limit, Band: BandShed}
	}
	return Budget{
		Window:   m.cfg.Window.String(),
		Consumed: consumed,
		Limit:    m.cfg.Limit,
		Band:     m.cfg.band(consumed),
	}
}

// readConsumed runs the read-only effectiveConsumed query for ctx's
// workspace (0 for an expired or absent window). Both read surfaces
// (Band, Snapshot) go through it so the "expired reads as 0, never
// rolls" rule lives in one place.
func (m *Meter) readConsumed(ctx context.Context) (int, error) {
	var consumed int
	err := database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, effectiveConsumed, m.now(), m.windowSeconds()).Scan(&consumed)
	})
	if err != nil {
		return 0, err
	}
	return consumed, nil
}

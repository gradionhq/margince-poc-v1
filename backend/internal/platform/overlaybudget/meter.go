// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package overlaybudget is the OVB per-incumbent consumption meter
// (overlay-budget subsystem): a first-class, server-side budget for an
// incumbent CRM's API allocation, metered from OUR OWN call counts. It
// keeps overlay traffic a conservative, truthful distance below a rate
// ceiling we share with integrations we can't see, and never fabricates
// the headroom it can't account for.
//
// It lives in the platform layer and imports nothing from modules/compose
// (ADR-0014). Overlay adapters (the module) charge it and degrade on its
// answer; the admin budget surface reads its snapshot.
//
// Storage: Redis fixed-window counters with expiry, keyed per workspace +
// incumbent + window (+ per-source for the REST window). Two rolling
// windows per incumbent connection:
//
//   - Search (per-second): the burst limiter over the incumbent's
//     per-second Search-API ceiling. The poller RESERVES a slot before each
//     search request and paces itself when the window sheds, so we never
//     trip the incumbent's per-second 429.
//   - REST (daily): the day's allocation. force-fresh reads RESERVE against
//     it (degrading to mirror-with-staleness on shed, AC-OV-7); the poller
//     CONSUMES against it unconditionally (an incumbent it must mirror is
//     not something to refuse partway — but its spend is still counted).
//     Every REST charge is attributed to a source and the per-source
//     breakdown sums exactly to the REST total (OVB-AC-5).
//
// Headroom against the published ceiling is inherently `~unknown`: we meter
// 100% of our own calls, but foreign integrations sharing the ceiling are
// invisible to us, so the remaining headroom can never be attributed
// (OVB-AC-1 / OVB-PARAM-5) — the meter reports our own consumption and the
// band, never an invented headroom number.
//
// Fail-closed: with no Redis (nil client, unreachable, or a scripting
// error) or no configured incumbent, the meter answers shed / declines
// every reservation — it never invents headroom it cannot account for.
package overlaybudget

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Source is the metered attribution of a REST charge (OVB-AC-5): the
// per-source breakdown sums to the REST total.
type Source string

const (
	// SourceForceFresh is a synchronous force-fresh read-through.
	SourceForceFresh Source = "force_fresh"
	// SourcePoller is the reconciliation poller's sweep traffic.
	SourcePoller Source = "poller"
	// SourceCapture is capture write-back (branch 2; unmetered until it
	// exists, but reserved here so the breakdown enumerates every source).
	SourceCapture Source = "capture"
)

// allSources is the fixed attribution set the breakdown reads back.
var allSources = []Source{SourceForceFresh, SourcePoller, SourceCapture}

// Band is the meter's three-state answer to a charge/read.
const (
	BandOK   = "ok"
	BandWarn = "warn"
	BandShed = "shed"
)

// UnknownHeadroom is the sentinel rendered wherever headroom cannot be
// attributed to our own counts (OVB-PARAM-5) — never replaced by a
// fabricated number.
const UnknownHeadroom = "~unknown"

// WindowConfig is one rolling window's published ceiling and our own
// (lower) cap. Consumption is metered against the cap; the ceiling is the
// incumbent's published rate limit the cap stays strictly below.
type WindowConfig struct {
	Ceiling int
	Cap     int
}

// IncumbentConfig is one incumbent's resolved meter config: the two
// windows plus the shared warn/shed fractions of the cap.
type IncumbentConfig struct {
	Search       WindowConfig
	REST         WindowConfig
	WarnFraction float64
	ShedFraction float64
}

// Config maps incumbent name (e.g. "hubspot") to its resolved meter
// config. compose builds it from deployconfig.EffectiveOverlayBudget.
type Config map[string]IncumbentConfig

// Budget is the meter's snapshot of one incumbent's current REST (daily)
// window — the shape the admin budget surface and GetOverlayBudget read.
// Headroom is always the unknown sentinel (see the package doc); the
// per-source Breakdown sums to Consumed (OVB-AC-5).
type Budget struct {
	Window    string
	Consumed  int
	Limit     int
	Band      string
	Headroom  string
	Breakdown map[Source]int
}

// Meter is the OVB consumption meter. The zero value is not usable;
// construct with New or NewWithClock.
type Meter struct {
	rdb *redis.Client
	cfg Config
	now func() time.Time
}

// New constructs a Meter over rdb and cfg using the real wall clock. A nil
// rdb is allowed and makes the meter fail-closed (every band shed, every
// reservation declined) — a role with no Redis can never spend live quota
// it cannot account for.
func New(rdb *redis.Client, cfg Config) *Meter {
	return NewWithClock(rdb, cfg, time.Now)
}

// RebindFrom copies src's Redis client and config onto this meter — the
// boot-time injection point compose uses WITHOUT itself naming a Redis
// client (that dependency stays in the cmd/platform tiers, never compose):
// newServer constructs a fail-closed meter (nil client) and shares that ONE
// pointer with the read dispatch and the budget handlers, then a
// WithOverlayMeter option RebindsFrom the live meter cmd built once the
// Redis client and deployment config are known — so every holder of the
// shared pointer sees the live meter without re-plumbing. Called at server
// assembly, before any request is served, so it never races an operation
// (the same discipline FreshnessReader.SetIncumbentResolver follows).
func (m *Meter) RebindFrom(src *Meter) {
	m.rdb = src.rdb
	m.cfg = src.cfg
}

// NewWithClock takes the clock as a dependency so the fixed-window bucket a
// charge lands in is a property tests assert by advancing time, not by
// sleeping against the real clock (T11): the clock's reading picks the
// per-second / per-day Redis key and the expiry, deterministically.
func NewWithClock(rdb *redis.Client, cfg Config, now func() time.Time) *Meter {
	return &Meter{rdb: rdb, cfg: cfg, now: now}
}

// searchTTL/restTTL cover the fixed window plus clock-skew slack so a
// counter never outlives its window (over-counting) nor expires inside it.
const (
	searchTTL = 2 * time.Second
	restTTL   = 25 * time.Hour
)

// band computes ok/warn/shed for consumed against a window's cap and the
// configured fractions. A non-positive cap can never be "under budget" —
// it fails closed to shed rather than dividing by zero or reading as free.
func (ic IncumbentConfig) band(consumed, limit int) string {
	switch {
	case limit <= 0:
		return BandShed
	case float64(consumed) >= ic.ShedFraction*float64(limit):
		return BandShed
	case float64(consumed) >= ic.WarnFraction*float64(limit):
		return BandWarn
	default:
		return BandOK
	}
}

// shedThreshold is the count at or over which a window sheds — the integer
// floor of shedFraction*limit. A reservation is declined at or over it
// (OVB-AC-2).
func shedThreshold(frac float64, limit int) int {
	return int(frac * float64(limit))
}

// resolve fetches ctx's workspace and the incumbent's config together — the
// two inputs every operation needs. A missing workspace or an unconfigured
// incumbent is the fail-closed direction: the caller degrades/declines.
func (m *Meter) resolve(ctx context.Context, incumbent string) (wsID ids.UUID, ic IncumbentConfig, ok bool) {
	if m.rdb == nil {
		return wsID, ic, false
	}
	ws, bound := principal.WorkspaceID(ctx)
	if !bound {
		return wsID, ic, false
	}
	cfg, configured := m.cfg[incumbent]
	if !configured {
		return wsID, ic, false
	}
	return ws, cfg, true
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlaybudget

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Redis key layout, keyed per workspace + incumbent + window so one
// connection's counters never dent another's (OVB-AC-3). The time bucket
// makes each key a FIXED window with expiry — the per-second search window
// buckets on the UTC second, the REST window on the UTC day (a fixed
// calendar-day window, not a sliding 24h one: the spec's mechanism is
// "fixed-window counters with expiry", so the daily allocation resets at
// UTC midnight). The bucket comes from the injected clock, never redis
// TIME, so window rollover is deterministic under test (T11).
func searchKey(ws ids.UUID, incumbent string, second int64) string {
	return fmt.Sprintf("ovb:%s:%s:search:%d", ws.String(), incumbent, second)
}

func restTotalKey(ws ids.UUID, incumbent string, day int64) string {
	return fmt.Sprintf("ovb:%s:%s:rest:%d", ws.String(), incumbent, day)
}

func restSourceKey(ws ids.UUID, incumbent string, day int64, src Source) string {
	return fmt.Sprintf("ovb:%s:%s:rest:%d:src:%s", ws.String(), incumbent, day, src)
}

// reserveRestScript is the force-fresh REST gate: it declines (return 0,
// record nothing) when the increment would take the total PAST the shed
// threshold — so a batch reservation can never overshoot, not just a
// single unit — and otherwise INCRBYs the total (KEYS[1]) and the
// per-source counter (KEYS[2]) together so the breakdown always sums to
// the total (OVB-AC-5), setting the fixed-window expiry on first write.
// ARGV=[n, shedThreshold, ttlSeconds]. Atomic, so concurrent reservers
// cannot all observe a non-shed total and collectively overshoot.
var reserveRestScript = redis.NewScript(`
local total = tonumber(redis.call('GET', KEYS[1]) or '0')
local n = tonumber(ARGV[1])
if total + n > tonumber(ARGV[2]) then return 0 end
local newtotal = redis.call('INCRBY', KEYS[1], n)
redis.call('INCRBY', KEYS[2], n)
if newtotal == n then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3])) end
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[3]))
return 1`)

// consumeRestScript records unconditionally (no gate) into the REST total
// and the per-source counter together — the poller's path (it must mirror,
// so it never refuses, but its spend is still counted and attributed).
// ARGV=[n, ttlSeconds]; returns the new total.
var consumeRestScript = redis.NewScript(`
local n = tonumber(ARGV[1])
local newtotal = redis.call('INCRBY', KEYS[1], n)
redis.call('INCRBY', KEYS[2], n)
if newtotal == n then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2])) end
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[2]))
return newtotal`)

// consumeSearchScript records unconditionally into the per-second search
// window (a single counter — the search window has no per-source
// breakdown; only the REST total does). ARGV=[n, ttlSeconds].
var consumeSearchScript = redis.NewScript(`
local n = tonumber(ARGV[1])
local cur = redis.call('INCRBY', KEYS[1], n)
if cur == n then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2])) end
return cur`)

// ReserveREST is the daily-allocation gate used by force-fresh reads: if
// recording n would take the REST window PAST its shed threshold it returns
// allowed=false (the consumer degrades to mirror-with-staleness, AC-OV-7)
// and records nothing; otherwise it records n against the total and the
// source and returns true. Doing the band check and the record atomically
// is the point: concurrent force-fresh reads cannot all observe a non-shed
// band and collectively overshoot. n must be positive. Fail-closed
// (declined) on no Redis / no config / no workspace / a Redis error.
func (m *Meter) ReserveREST(ctx context.Context, incumbent string, source Source, n int) (bool, error) {
	if n <= 0 {
		return false, fmt.Errorf("overlaybudget: ReserveREST requires a positive count, got %d", n)
	}
	if !source.known() {
		return false, fmt.Errorf("overlaybudget: unknown REST charge source %q — Snapshot only reads the fixed attribution set, so an unknown source would silently omit its spend", source)
	}
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return false, nil
	}
	day := m.now().UTC().Unix() / 86400
	total := restTotalKey(ws, incumbent, day)
	src := restSourceKey(ws, incumbent, day, source)
	shed := threshold(ic.ShedFraction, ic.REST.Cap)
	res, err := reserveRestScript.Run(ctx, m.rdb, []string{total, src}, n, shed, int(restTTL.Seconds())).Int()
	if err != nil {
		return false, fmt.Errorf("overlaybudget: reserving a REST charge: %w", err)
	}
	return res == 1, nil
}

// ConsumeREST records n REST charges for source UNCONDITIONALLY — the
// poller's path, which mirrors the incumbent and so never refuses, but
// whose spend must still be counted and attributed. A non-positive n is a
// no-op (an empty page spent no quota). A no-Redis/no-config/no-workspace
// meter silently records nothing (it has no window to record into and
// fails closed on the read side instead). A Redis error is returned so the
// caller can log it.
func (m *Meter) ConsumeREST(ctx context.Context, incumbent string, source Source, n int) error {
	if n <= 0 {
		return nil
	}
	if !source.known() {
		return fmt.Errorf("overlaybudget: unknown REST charge source %q — Snapshot only reads the fixed attribution set, so an unknown source would silently omit its spend", source)
	}
	ws, _, ok := m.resolve(ctx, incumbent)
	if !ok {
		return nil
	}
	day := m.now().UTC().Unix() / 86400
	total := restTotalKey(ws, incumbent, day)
	src := restSourceKey(ws, incumbent, day, source)
	if err := consumeRestScript.Run(ctx, m.rdb, []string{total, src}, n, int(restTTL.Seconds())).Err(); err != nil {
		return fmt.Errorf("overlaybudget: recording %d %s REST charges: %w", n, source, err)
	}
	return nil
}

// ConsumeSearch records n Search-API requests against the per-second
// search window UNCONDITIONALLY. The search window is METERED, not gated:
// the poller cannot decline a page mid-scan (that would strand later pages
// — a paginated scan is not resumable from scratch), so it records its
// search rate here and the window is reported (Snapshot) for the budget
// surface. Active per-second admission (a token bucket that spaces or
// defers requests) is a follow-up; it is NOT an abort of the sweep. A
// non-positive n is a no-op; a no-Redis/no-config/no-workspace meter
// records nothing; a Redis error is returned for the caller to log.
func (m *Meter) ConsumeSearch(ctx context.Context, incumbent string, n int) error {
	if n <= 0 {
		return nil
	}
	ws, _, ok := m.resolve(ctx, incumbent)
	if !ok {
		return nil
	}
	second := m.now().UTC().Unix()
	if err := consumeSearchScript.Run(ctx, m.rdb, []string{searchKey(ws, incumbent, second)}, n, int(searchTTL.Seconds())).Err(); err != nil {
		return fmt.Errorf("overlaybudget: recording %d search requests: %w", n, err)
	}
	return nil
}

// BandREST reports the incumbent's current REST (daily) degradation band
// for ctx's workspace. A no-Redis/no-config/no-workspace meter or a Redis
// error answers shed — the fail-closed direction: an unknown budget must
// never read as "spend freely".
func (m *Meter) BandREST(ctx context.Context, incumbent string) string {
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return BandShed
	}
	total, err := m.readCounter(ctx, restTotalKey(ws, incumbent, m.now().UTC().Unix()/86400))
	if err != nil {
		return BandShed
	}
	return ic.band(total, ic.REST.Cap)
}

// readCounter reads a single window counter, treating a missing key
// (redis.Nil) as zero (no charges yet this window) and surfacing any other
// error so the caller can fail closed.
func (m *Meter) readCounter(ctx context.Context, key string) (int, error) {
	v, err := m.rdb.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	return v, nil
}

// Snapshot answers both windows' current consumption for ctx's workspace:
// the REST (daily) total/cap/band plus its per-source breakdown (summing to
// the total, OVB-AC-5), the per-second search window's count/cap/band, and
// the always-`~unknown` headroom (foreign integrations sharing the ceiling
// are invisible to us — OVB-AC-1). GET /overlay/budget surfaces only the
// REST figures today; the search figures and breakdown feed the separate
// admin budget surface. A no-Redis/no-config/no-workspace meter or a Redis
// error answers the same fail-closed shed band with zero consumption.
func (m *Meter) Snapshot(ctx context.Context, incumbent string) Budget {
	// shedFn builds a fail-closed snapshot: shed band, zero consumption, but
	// the REAL caps when config resolved (a Redis read failure must not
	// misreport the configured limit as zero — limit stays 0 only when no
	// config was found at all).
	shedFn := func(ic IncumbentConfig) Budget {
		return Budget{
			Window: "24h", Limit: ic.REST.Cap, Band: BandShed, Headroom: UnknownHeadroom, Breakdown: map[Source]int{},
			SearchWindow: "1s", SearchLimit: ic.Search.Cap, SearchBand: BandShed,
		}
	}
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return shedFn(IncumbentConfig{})
	}
	day := m.now().UTC().Unix() / 86400
	breakdown := make(map[Source]int, len(allSources))
	sum := 0
	for _, s := range allSources {
		v, err := m.readCounter(ctx, restSourceKey(ws, incumbent, day, s))
		if err != nil {
			return shedFn(ic)
		}
		breakdown[s] = v
		sum += v
	}
	search, err := m.readCounter(ctx, searchKey(ws, incumbent, m.now().UTC().Unix()))
	if err != nil {
		return shedFn(ic)
	}
	return Budget{
		Window:         "24h",
		Consumed:       sum,
		Limit:          ic.REST.Cap,
		Band:           ic.band(sum, ic.REST.Cap),
		Headroom:       UnknownHeadroom,
		Breakdown:      breakdown,
		SearchWindow:   "1s",
		SearchConsumed: search,
		SearchLimit:    ic.Search.Cap,
		SearchBand:     ic.band(search, ic.Search.Cap),
	}
}

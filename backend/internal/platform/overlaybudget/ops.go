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
// (per-second for search, per-day for REST) makes each key a fixed window
// with expiry; the bucket comes from the injected clock, never redis TIME,
// so window rollover is deterministic under test (T11).
func searchKey(ws ids.UUID, incumbent string, second int64) string {
	return fmt.Sprintf("ovb:%s:%s:search:%d", ws.String(), incumbent, second)
}

func restTotalKey(ws ids.UUID, incumbent string, day int64) string {
	return fmt.Sprintf("ovb:%s:%s:rest:%d", ws.String(), incumbent, day)
}

func restSourceKey(ws ids.UUID, incumbent string, day int64, src Source) string {
	return fmt.Sprintf("ovb:%s:%s:rest:%d:src:%s", ws.String(), incumbent, day, src)
}

// reserveScript gates a single-counter window: if the current count is at
// or over the shed threshold, decline (return 0, record nothing); else
// INCRBY and, on the first increment of a fresh window, set the expiry.
// KEYS[1]=counter; ARGV=[n, shedThreshold, ttlSeconds]. Atomic, so
// concurrent reservers cannot all observe a non-shed count and overshoot.
var reserveScript = redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
if cur >= tonumber(ARGV[2]) then return 0 end
local n = redis.call('INCRBY', KEYS[1], tonumber(ARGV[1]))
if n == tonumber(ARGV[1]) then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3])) end
return 1`)

// reserveRestScript is reserveScript for the REST window's total PLUS the
// per-source counter: gate on the total's shed threshold, then INCRBY both
// the total (KEYS[1]) and the source counter (KEYS[2]) together so the
// per-source breakdown always sums to the total (OVB-AC-5).
// ARGV=[n, shedThreshold, ttlSeconds].
var reserveRestScript = redis.NewScript(`
local total = tonumber(redis.call('GET', KEYS[1]) or '0')
if total >= tonumber(ARGV[2]) then return 0 end
local newtotal = redis.call('INCRBY', KEYS[1], tonumber(ARGV[1]))
redis.call('INCRBY', KEYS[2], tonumber(ARGV[1]))
if newtotal == tonumber(ARGV[1]) then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[3])) end
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[3]))
return 1`)

// consumeRestScript records unconditionally (no gate) into the REST total
// and the per-source counter together — the poller's path (it must mirror,
// so it never refuses, but its spend is still counted and attributed).
// ARGV=[n, ttlSeconds]; returns the new total.
var consumeRestScript = redis.NewScript(`
local newtotal = redis.call('INCRBY', KEYS[1], tonumber(ARGV[1]))
redis.call('INCRBY', KEYS[2], tonumber(ARGV[1]))
if newtotal == tonumber(ARGV[1]) then redis.call('EXPIRE', KEYS[1], tonumber(ARGV[2])) end
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[2]))
return newtotal`)

// ReserveSearch is the per-second burst gate: if the incumbent's search
// window is at or over its shed threshold, it returns allowed=false so the
// poller paces itself (defers the rest of the sweep) rather than tripping
// the incumbent's per-second 429; otherwise it records n and returns true.
// n must be positive. Fail-closed (declined) on no Redis / no config /
// no workspace / a Redis error.
func (m *Meter) ReserveSearch(ctx context.Context, incumbent string, n int) (bool, error) {
	if n <= 0 {
		return false, fmt.Errorf("overlaybudget: ReserveSearch requires a positive count, got %d", n)
	}
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return false, nil
	}
	second := m.now().UTC().Unix()
	key := searchKey(ws, incumbent, second)
	threshold := shedThreshold(ic.ShedFraction, ic.Search.Cap)
	res, err := reserveScript.Run(ctx, m.rdb, []string{key}, n, threshold, int(searchTTL.Seconds())).Int()
	if err != nil {
		// Fail closed: a metering error must never read as "spend freely".
		return false, fmt.Errorf("overlaybudget: reserving a search slot: %w", err)
	}
	return res == 1, nil
}

// ReserveREST is the daily-allocation gate used by force-fresh reads: if
// the incumbent's REST window is at or over its shed threshold it returns
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
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return false, nil
	}
	day := m.now().UTC().Unix() / 86400
	total := restTotalKey(ws, incumbent, day)
	src := restSourceKey(ws, incumbent, day, source)
	threshold := shedThreshold(ic.ShedFraction, ic.REST.Cap)
	res, err := reserveRestScript.Run(ctx, m.rdb, []string{total, src}, n, threshold, int(restTTL.Seconds())).Int()
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

// BandREST reports the incumbent's current REST (daily) degradation band
// for ctx's workspace. A no-Redis/no-config/no-workspace meter or a Redis
// error answers shed — the fail-closed direction: an unknown budget must
// never read as "spend freely".
func (m *Meter) BandREST(ctx context.Context, incumbent string) string {
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return BandShed
	}
	day := m.now().UTC().Unix() / 86400
	// A missing key (redis.Nil) is "no charges yet this window" — Int()
	// returns 0 for it, so total is already 0; any OTHER error fails closed.
	total, err := m.rdb.Get(ctx, restTotalKey(ws, incumbent, day)).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return BandShed
	}
	return ic.band(total, ic.REST.Cap)
}

// Snapshot answers the current REST window's consumption for ctx's
// workspace: the total, the cap, the band, the per-source breakdown, and
// the always-`~unknown` headroom (foreign integrations sharing the ceiling
// are invisible to us — OVB-AC-1). A no-Redis/no-config/no-workspace meter
// or a Redis error answers the same fail-closed shed band with zero
// consumption (there is no window it can trust to report on).
func (m *Meter) Snapshot(ctx context.Context, incumbent string) Budget {
	shed := Budget{Window: "24h", Consumed: 0, Limit: 0, Band: BandShed, Headroom: UnknownHeadroom, Breakdown: map[Source]int{}}
	ws, ic, ok := m.resolve(ctx, incumbent)
	if !ok {
		return shed
	}
	day := m.now().UTC().Unix() / 86400
	breakdown := make(map[Source]int, len(allSources))
	sum := 0
	for _, s := range allSources {
		// A missing source key (redis.Nil) is zero charges — Int() already
		// returns 0 for it; any OTHER error fails closed.
		v, err := m.rdb.Get(ctx, restSourceKey(ws, incumbent, day, s)).Int()
		if err != nil && !errors.Is(err, redis.Nil) {
			return shed
		}
		breakdown[s] = v
		sum += v
	}
	return Budget{
		Window:    "24h",
		Consumed:  sum,
		Limit:     ic.REST.Cap,
		Band:      ic.band(sum, ic.REST.Cap),
		Headroom:  UnknownHeadroom,
		Breakdown: breakdown,
	}
}

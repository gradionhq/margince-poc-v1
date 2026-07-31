// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package ratelimit is a small in-process fixed-window limiter for the
// callers that must bound how often something happens per key.
//
// Two shapes of caller, and the API serves both. Allow counts an ATTEMPT and
// decides in one step — the unauthenticated auth endpoints take this one, since
// login brute-force is expensive to serve (Argon2id ≈ 19 MiB per attempt) and
// bootstrap mints whole tenants. Blocked and Record split the two halves for a
// caller that meters an OUTCOME instead: Blocked peeks without spending a slot,
// and Record spends one only once the metered thing actually happened. Outbound
// send pacing (comms.MailboxRatePolicy) takes that pair, because merely asking
// whether a mailbox may send must not consume its quota.
//
// Keys are bounded: one longer than maxKeyLen is not metered at all, on any of
// the three entry points, so a caller may key on a value a client chose without
// first bounding it itself.
//
// In-process is the honest scope for a single-binary PoC — a multi-replica
// deployment paces each replica's own view — and moving the same keys into
// Redis would not change callers.
package ratelimit

import (
	"sync"
	"time"
)

// maxKeyLen bounds what may become a map key. Every caller keys on something a
// remote client chose — a path segment, a slug, a token, an email — and Go
// admits a request line near 1 MB, so an unbounded key turns a protective edge
// into a memory-exhaustion path: what the map takes stays until the next Record
// or Allow sweeps it, which is never once the flood that created it stops
// arriving. Bounding it here rather than at each call site is what stops the
// next caller reintroducing it.
//
// The bound is generous rather than tight, because an unmetered key is a hole
// in whatever the caller was rate-limiting and the longest LEGITIMATE key here
// is not short: identity meters login failures on `email|IP`, and RFC 5321
// permits a 254-character address, which an IPv6 literal takes past 300. A tight
// bound would silently stop metering exactly the accounts with long addresses.
// At this size no honest key is refused, and the memory ceiling is still three
// orders of magnitude below what an unbounded key allows.
const maxKeyLen = 512

// meterable reports whether key is one this limiter will account for.
//
// An over-long key is refused, never truncated: truncation merges distinct
// callers into one bucket, so a single long shared prefix would spend everyone
// else's budget. Refusing means such a key is simply not metered — Allow admits
// it, Blocked never reports it, Record counts nothing. That direction is
// deliberate. A composite key can legitimately run long (an RFC 5321 address
// paired with an IPv6 literal already can), and denying on length would lock a
// real caller out of a real account for the size of a value they did not
// choose. So a caller that keys on a value a client chose must also meter a
// bounded one — the client IP — because that second budget is what brakes a
// flood whether or not the first key was meterable.
func meterable(key string) bool { return len(key) <= maxKeyLen }

// Limiter counts events per key in fixed windows. The zero value is not
// usable; construct with New.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	starts  map[string]time.Time
	counts  map[string]int
	sweepAt time.Time
}

func New(limit int, window time.Duration) *Limiter {
	return NewWithClock(limit, window, time.Now)
}

// NewWithClock takes the clock as a dependency so window expiry is a
// property tests assert by advancing time, not by sleeping against it.
func NewWithClock(limit int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  window,
		now:     now,
		starts:  make(map[string]time.Time),
		counts:  make(map[string]int),
		sweepAt: now().Add(window),
	}
}

// Allow records one attempt for key and reports whether it is within the
// limit. Counting before deciding means an attacker cannot probe the
// limit boundary for free.
func (l *Limiter) Allow(key string) bool {
	if !meterable(key) {
		return true
	}
	return l.count(key) <= l.limit
}

// Record counts one event for key without deciding. Paired with Blocked
// for limiters that count OUTCOMES (failed logins) rather than attempts —
// counting every attempt would let an attacker's noise throttle a
// legitimate caller's successes.
func (l *Limiter) Record(key string) {
	if !meterable(key) {
		return
	}
	l.count(key)
}

// Blocked reports whether key has already reached the limit in its
// current window, without counting the probe.
func (l *Limiter) Blocked(key string) bool {
	if !meterable(key) {
		return false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	start, ok := l.starts[key]
	if !ok || now.Sub(start) >= l.window {
		return false
	}
	return l.counts[key] >= l.limit
}

func (l *Limiter) count(key string) int {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Amortized sweep: drop expired windows so abandoned keys do not
	// accumulate forever (an unauthenticated endpoint sees arbitrary keys).
	if now.After(l.sweepAt) {
		for k, start := range l.starts {
			if now.Sub(start) >= l.window {
				delete(l.starts, k)
				delete(l.counts, k)
			}
		}
		l.sweepAt = now.Add(l.window)
	}

	if start, ok := l.starts[key]; !ok || now.Sub(start) >= l.window {
		l.starts[key] = now
		l.counts[key] = 0
	}
	l.counts[key]++
	return l.counts[key]
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package ratelimit is a small in-process fixed-window limiter for the
// unauthenticated auth endpoints: login brute-force is expensive to serve
// (Argon2id ≈ 19 MiB per attempt) and bootstrap mints whole tenants, so
// both need a throttle in front of them. In-process is the honest scope
// for a single-binary PoC; a multi-replica deployment moves the same keys
// into Redis without changing callers.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter counts events per key in fixed windows. The zero value is not
// usable; construct with New.
type Limiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	starts  map[string]time.Time
	counts  map[string]int
	sweepAt time.Time
}

func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  window,
		starts:  make(map[string]time.Time),
		counts:  make(map[string]int),
		sweepAt: time.Now().Add(window),
	}
}

// Allow records one attempt for key and reports whether it is within the
// limit. Counting before deciding means an attacker cannot probe the
// limit boundary for free.
func (l *Limiter) Allow(key string) bool {
	return l.count(key) <= l.limit
}

// Record counts one event for key without deciding. Paired with Blocked
// for limiters that count OUTCOMES (failed logins) rather than attempts —
// counting every attempt would let an attacker's noise throttle a
// legitimate caller's successes.
func (l *Limiter) Record(key string) { l.count(key) }

// Blocked reports whether key has already reached the limit in its
// current window, without counting the probe.
func (l *Limiter) Blocked(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	start, ok := l.starts[key]
	if !ok || now.Sub(start) >= l.window {
		return false
	}
	return l.counts[key] >= l.limit
}

func (l *Limiter) count(key string) int {
	now := time.Now()
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

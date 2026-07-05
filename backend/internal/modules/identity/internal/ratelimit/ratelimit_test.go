// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ratelimit

import (
	"testing"
	"time"
)

// fakeClock makes window expiry a value the test controls: advancing it
// is deterministic where sleeping against a real window is a race.
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestAllowCountsPerKeyWithinTheWindow(t *testing.T) {
	l := New(3, time.Hour)
	for i := 1; i <= 3; i++ {
		if !l.Allow("alice") {
			t.Fatalf("attempt %d should be within the limit", i)
		}
	}
	if l.Allow("alice") {
		t.Error("attempt 4 of 3 should be rejected")
	}
	if !l.Allow("bob") {
		t.Error("another key must not share alice's window")
	}
}

func TestWindowExpiryResetsTheCount(t *testing.T) {
	clock := newFakeClock()
	l := NewWithClock(1, time.Minute, clock.Now)
	if !l.Allow("k") {
		t.Fatal("first attempt")
	}
	if l.Allow("k") {
		t.Fatal("second attempt inside the window should be rejected")
	}
	clock.advance(time.Minute)
	if !l.Allow("k") {
		t.Error("a fresh window should admit again")
	}
}

func TestBlockedReportsWithoutCounting(t *testing.T) {
	clock := newFakeClock()
	l := NewWithClock(2, time.Minute, clock.Now)
	if l.Blocked("k") {
		t.Fatal("an unseen key is not blocked")
	}
	l.Record("k")
	l.Record("k")
	if !l.Blocked("k") {
		t.Fatal("the limit is reached; Blocked must say so")
	}
	clock.advance(time.Minute)
	if l.Blocked("k") {
		t.Error("an expired window no longer blocks")
	}
}

func TestSweepDropsAbandonedKeys(t *testing.T) {
	clock := newFakeClock()
	l := NewWithClock(1, time.Minute, clock.Now)
	for i := 0; i < 100; i++ {
		l.Allow(string(rune('a' + i%26)))
	}
	clock.advance(time.Minute + time.Second)
	l.Allow("fresh") // triggers the amortized sweep
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.counts) > 2 {
		t.Errorf("sweep left %d expired keys behind", len(l.counts))
	}
}

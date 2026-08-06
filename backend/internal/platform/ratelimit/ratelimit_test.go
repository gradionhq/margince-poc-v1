// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ratelimit

import (
	"strings"
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

// Every caller keys on a value a remote client chose — a slug, a token, an
// email — and Go admits a request line near 1 MB, so a key the limiter accepts
// verbatim is memory an unauthenticated caller decides the size of. The map
// keeps whatever it has taken until the next Record or Allow sweeps it, which
// is never once a flood stops arriving, so the cost is resident for the life of
// the process rather than for one window.
//
// The refusal is what this asserts, on all three entry points and on the map
// itself: an over-long key must not be stored, must not deny the caller (a
// legitimate caller cannot be locked out by the size of a value it did not
// choose — the bounded per-IP budget is what brakes a flood), and must not
// disturb the metering of a normal key.
func TestALimiterRefusesAnOversizedKey(t *testing.T) {
	clock := newFakeClock()
	l := NewWithClock(1, time.Minute, clock.Now)
	oversized := strings.Repeat("x", 100*1024)

	if !l.Allow(oversized) {
		t.Error("Allow denied an unmeterable key; refusing to meter must not refuse the caller")
	}
	l.Record(oversized)
	if l.Blocked(oversized) {
		t.Error("Blocked reported a key that was never counted as over its limit")
	}
	if got := heldKeys(t, l); got != 0 {
		t.Errorf("the limiter retains %d keys after three over-long calls, want 0", got)
	}

	if !l.Allow("alice") {
		t.Fatal("a normal key is still metered: attempt 1 of 1 should be within the limit")
	}
	if l.Allow("alice") {
		t.Error("a normal key is still metered: attempt 2 of 1 should be rejected")
	}
	if got := heldKeys(t, l); got != 1 {
		t.Errorf("the limiter retains %d keys, want 1 — only the normal one", got)
	}
}

// heldKeys reports what the limiter is holding, read under its own lock so the
// assertion cannot race the code it asserts on. starts and counts are written
// as a pair, so the two disagreeing is itself the fault.
func heldKeys(t *testing.T, l *Limiter) int {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.starts) != len(l.counts) {
		t.Fatalf("the limiter holds %d window starts but %d counts", len(l.starts), len(l.counts))
	}
	return len(l.counts)
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

func TestResetClearsEverySpentBucket(t *testing.T) {
	lim := New(1, time.Minute)
	if !lim.Allow("k") {
		t.Fatal("the first attempt must be admitted")
	}
	if lim.Allow("k") {
		t.Fatal("the second attempt must be refused; the bucket is spent")
	}

	lim.Reset()

	if !lim.Allow("k") {
		t.Error("Allow refused after Reset; the bucket was not cleared")
	}
}

// Allow is only one of three entry points that read the accumulated state;
// Blocked and Record split attempt from outcome and must see the same clean
// slate, or a Reset that only satisfies Allow is a half-fix.
func TestResetClearsWhatBlockedAndRecordRead(t *testing.T) {
	clock := newFakeClock()
	lim := NewWithClock(2, time.Minute, clock.Now)
	lim.Record("k")
	lim.Record("k")
	if !lim.Blocked("k") {
		t.Fatal("the limit is reached; Blocked must say so before Reset")
	}

	lim.Reset()

	if lim.Blocked("k") {
		t.Error("Blocked still reports the key spent after Reset")
	}
	lim.Record("k")
	if lim.Blocked("k") {
		t.Error("one Record after Reset must not already read as blocked")
	}
}

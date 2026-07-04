// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ratelimit

import (
	"testing"
	"time"
)

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
	l := New(1, 20*time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("first attempt")
	}
	if l.Allow("k") {
		t.Fatal("second attempt inside the window should be rejected")
	}
	time.Sleep(25 * time.Millisecond)
	if !l.Allow("k") {
		t.Error("a fresh window should admit again")
	}
}

func TestSweepDropsAbandonedKeys(t *testing.T) {
	l := New(1, 10*time.Millisecond)
	for i := 0; i < 100; i++ {
		l.Allow(string(rune('a' + i%26)))
	}
	time.Sleep(15 * time.Millisecond)
	l.Allow("fresh") // triggers the amortized sweep
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.counts) > 2 {
		t.Errorf("sweep left %d expired keys behind", len(l.counts))
	}
}

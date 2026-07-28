// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// Hermetic unit lane: the pieces of Store that need no database. The
// real-Postgres behaviour (staging, attempt counting, ErrTerminal, the
// status transitions) is proven in store_integration_test.go, gated
// //go:build integration — a unit test in this file must never dial
// Postgres (scripts/check-test-lanes.sh enforces it).

import (
	"testing"
	"time"
)

// A nil clock is a caller forgetting to inject one, not a caller who wants
// no delivery timestamps. NewStore must still hand back a usable clock
// rather than a nil func that panics on first call.
func TestNewStoreDefaultsANilClockToTheRealOne(t *testing.T) {
	s := NewStore(nil, nil)
	before := time.Now()
	got := s.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("now() = %s, want a value between %s and %s (the real clock)", got, before, after)
	}
}

// An injected clock is used as given, never silently replaced.
func TestNewStoreKeepsAnInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	s := NewStore(nil, func() time.Time { return fixed })
	if got := s.now(); !got.Equal(fixed) {
		t.Fatalf("now() = %s, want the injected %s", got, fixed)
	}
}

// The status vocabulary is a closed set pinned to the comms_outbound CHECK
// constraint (migrations/core/0136_comms_outbound.up.sql); a typo here would
// silently desync the Go constants from what the database actually accepts.
func TestStatusConstantsMatchTheSchemaCheck(t *testing.T) {
	want := map[string]string{"pending": StatusPending, "sent": StatusSent, "parked": StatusParked}
	for lit, constant := range want {
		if lit != constant {
			t.Errorf("status constant %q, want %q", constant, lit)
		}
	}
}

// R3: there is no 'sending' status, and none of the three that exist reads
// as an in-flight claim — that absence is the point (a crash mid-send must
// never strand a row where a redelivery guard would skip it).
func TestNoStatusIsAnInFlightClaim(t *testing.T) {
	for _, s := range []string{StatusPending, StatusSent, StatusParked} {
		if s == "sending" {
			t.Fatalf("a %q status exists; R3 forbids an in-flight claim status", s)
		}
	}
}

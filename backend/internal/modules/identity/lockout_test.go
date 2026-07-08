// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The §27 lockout fold under a fixed clock: every transition the
// formulas-and-rules RC-17 knobs promise — accumulate, window reset,
// lock at the threshold, refuse while locked, unlock on expiry.

import (
	"testing"
	"time"
)

var lockoutEpoch = time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return lockoutEpoch.Add(d) }

func lockedUntil(t time.Time) *time.Time { return &t }

func TestLockoutFailFoldsPerFormulas27(t *testing.T) {
	cases := map[string]struct {
		state           lockoutState
		now             time.Time
		wantCount       int
		wantLockedUntil *time.Time
	}{
		"first failure starts the streak": {
			state:     lockoutState{},
			now:       at(0),
			wantCount: 1,
		},
		"failure inside the window accumulates": {
			state:     lockoutState{FailedCount: 2, LastFailure: at(0)},
			now:       at(14 * time.Minute),
			wantCount: 3,
		},
		"failure exactly at the window edge still counts": {
			state:     lockoutState{FailedCount: 3, LastFailure: at(0)},
			now:       at(lockoutWindow),
			wantCount: 4,
		},
		"a slow drip restarts the streak instead of accumulating": {
			state:     lockoutState{FailedCount: 4, LastFailure: at(0)},
			now:       at(lockoutWindow + time.Second),
			wantCount: 1,
		},
		"the fifth failure inside the window locks for the RC-17 duration": {
			state:           lockoutState{FailedCount: 4, LastFailure: at(0)},
			now:             at(5 * time.Minute),
			wantCount:       5,
			wantLockedUntil: lockedUntil(at(5*time.Minute + lockoutDuration)),
		},
		"a stale streak of four never locks on the next failure": {
			state:     lockoutState{FailedCount: 4, LastFailure: at(-time.Hour)},
			now:       at(0),
			wantCount: 1,
		},
		"a failure after the lock expired restarts, not re-locks": {
			state: lockoutState{
				FailedCount: 5, LastFailure: at(0),
				LockedUntil: lockedUntil(at(lockoutDuration)),
			},
			now:       at(lockoutDuration + 2*time.Minute),
			wantCount: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.state.fail(tc.now)
			if got.FailedCount != tc.wantCount {
				t.Errorf("fail() count = %d, want %d", got.FailedCount, tc.wantCount)
			}
			if got.LastFailure != tc.now {
				t.Errorf("fail() last failure = %v, want the attempt time %v", got.LastFailure, tc.now)
			}
			switch {
			case tc.wantLockedUntil == nil:
				if got.LockedUntil != nil && got.LockedUntil.After(tc.now) {
					t.Errorf("fail() locked the account until %v, want no live lock", got.LockedUntil)
				}
			case got.LockedUntil == nil || !got.LockedUntil.Equal(*tc.wantLockedUntil):
				t.Errorf("fail() locked_until = %v, want %v", got.LockedUntil, tc.wantLockedUntil)
			}
		})
	}
}

func TestLockoutLockedIsStrictlyBeforeLockedUntil(t *testing.T) {
	cases := map[string]struct {
		state lockoutState
		now   time.Time
		want  bool
	}{
		"no lock set":                {lockoutState{FailedCount: 4}, at(0), false},
		"inside the lock window":     {lockoutState{LockedUntil: lockedUntil(at(15 * time.Minute))}, at(14 * time.Minute), true},
		"at the expiry instant":      {lockoutState{LockedUntil: lockedUntil(at(15 * time.Minute))}, at(15 * time.Minute), false},
		"after the lock expired":     {lockoutState{LockedUntil: lockedUntil(at(15 * time.Minute))}, at(16 * time.Minute), false},
		"one nanosecond before open": {lockoutState{LockedUntil: lockedUntil(at(15 * time.Minute))}, at(15*time.Minute - time.Nanosecond), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.state.locked(tc.now); got != tc.want {
				t.Errorf("locked(%v) = %t, want %t", tc.now, got, tc.want)
			}
		})
	}
}

func TestLockoutThresholdNeedsFiveConsecutiveFailures(t *testing.T) {
	// Walk the whole streak through the fold: four rapid failures stay
	// unlocked, the fifth locks — the RC-17 threshold end to end.
	state := lockoutState{}
	for attempt := 1; attempt <= lockoutThreshold; attempt++ {
		now := at(time.Duration(attempt) * time.Minute)
		state = state.fail(now)
		wantLocked := attempt == lockoutThreshold
		if got := state.locked(now); got != wantLocked {
			t.Fatalf("after failure %d: locked = %t, want %t", attempt, got, wantLocked)
		}
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// TestReconcileFloor pins the sweep window a class actually reads from. The
// two failures on either side of it are asymmetric and both silent, which is
// why each case is spelled out rather than folded into one bound: too low and
// the sweep re-reads the whole portal (wasting a connect's entire Search quota
// and undoing the backfill cap); too high and records are skipped forever,
// because the watermark only ever advances.
func TestReconcileFloor(t *testing.T) {
	connectedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	// The grace is unexported, so each case states its expectation as a
	// predicate over the answer rather than restating the constant. Comparing
	// against ReconcileFloor's own output would be tautological — it would pass
	// for any grace at all, including zero.
	cases := []struct {
		name      string
		watermark time.Time
		wants     string
		ok        func(got time.Time) bool
	}{
		{
			// The case the whole function exists for: a class that has never
			// checkpointed reads back the zero time, which the incumbent renders
			// as "every record you hold".
			name:      "no watermark falls back to just before the connect instant",
			watermark: time.Time{},
			wants:     "a bounded window below the connect instant, never the epoch",
			ok: func(got time.Time) bool {
				return got.Before(connectedAt) && got.After(connectedAt.Add(-time.Hour))
			},
		},
		{
			// A live sweep owns its own progress. Raising it to the connect
			// instant would skip everything between.
			name:      "a watermark past the floor wins",
			watermark: connectedAt.Add(48 * time.Hour),
			wants:     "the watermark itself, untouched",
			ok:        func(got time.Time) bool { return got.Equal(connectedAt.Add(48 * time.Hour)) },
		},
		{
			// A watermark from before the connect cannot bound the sweep: it
			// predates this connection (Disconnect purges watermarks in the
			// transaction that revokes), and honouring it would re-read
			// everything the backfill is already responsible for.
			name:      "a watermark below the floor is raised",
			watermark: connectedAt.Add(-72 * time.Hour),
			wants:     "the floor, not the stale watermark",
			ok: func(got time.Time) bool {
				return got.After(connectedAt.Add(-time.Hour)) && !got.After(connectedAt)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := overlay.ReconcileFloor(tc.watermark, connectedAt); !tc.ok(got) {
				t.Errorf("ReconcileFloor(%v, %v) = %v, want %s", tc.watermark, connectedAt, got, tc.wants)
			}
		})
	}
}

// TestReconcileFloorBackdatesForClockSkew proves the fallback floor sits
// strictly BELOW the connect instant, by a bounded amount.
//
// connectedAt is the only value in the sweep window that comes from this
// host's clock — a persisted watermark is always an incumbent-generated
// timestamp — yet the incumbent filters on its own. An exact-connect floor
// loses any record the incumbent stamps inside that skew, permanently. An
// unbounded backdate is the opposite failure: it re-reads a wide slice of the
// portal on every connect, which is what the backfill cap exists to prevent.
func TestReconcileFloorBackdatesForClockSkew(t *testing.T) {
	connectedAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	const maxBackdate = time.Hour

	floor := overlay.ReconcileFloor(time.Time{}, connectedAt)

	if !floor.Before(connectedAt) {
		t.Errorf("floor = %v, want strictly before the connect instant %v — an exact-connect floor loses records stamped inside our clock skew", floor, connectedAt)
	}
	if floor.Before(connectedAt.Add(-maxBackdate)) {
		t.Errorf("floor = %v, backdated more than %v below the connect instant %v — the grace absorbs clock skew, not a portal re-read", floor, maxBackdate, connectedAt)
	}
}

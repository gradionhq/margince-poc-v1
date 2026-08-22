// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// countingStarter records how often the rail was told a call began.
type countingStarter struct {
	memCallStore
	starts []Call
	leases []time.Duration
}

func (c *countingStarter) AnnounceRailStart(_ context.Context, call Call, lease time.Duration) {
	c.starts = append(c.starts, call)
	c.leases = append(c.leases, lease)
}

// The lease must outlast the work it covers, and the property that makes it a
// derivation rather than a guess is that it grows with the ladder: a call may
// spend a full requestTimeout on EVERY rung before the flush that settles it.
//
// Stated as a strict inequality against the worst case rather than as the
// formula, so the test fails for a constant. A fixed lease passes any
// "lease == railLease(ladder)" check trivially and renders a healthy four-rung
// call as a dead worker in production, which is the failure nobody sees until
// somebody is watching a spinner that has already given up.
func TestTheLeaseOutlastsEveryRungTheLadderCanSpend(t *testing.T) {
	for _, rungs := range []int{1, 2, 3, 4} {
		ladder := make([]Tier, rungs)
		worstCase := requestTimeout * time.Duration(rungs)
		if got := railLease(ladder); got <= worstCase {
			t.Errorf("railLease over %d rungs = %s, which does not outlast the %s those rungs can spend — "+
				"a call still running would render stalled", rungs, got, worstCase)
		}
	}
}

// An empty ladder still gets a lease that means something. It serves nothing,
// so no call runs — but the lease is computed before that is known, and a zero
// would mark the occurrence stale in the instant it appeared.
func TestAnEmptyLadderStillLeasesAboveZero(t *testing.T) {
	if got := railLease(nil); got <= 0 {
		t.Errorf("railLease(nil) = %s, want a positive lease", got)
	}
}

// CompleteStructured threads ONE logical call through up to three attempts —
// the first try, the schema-invalid retry, the tier escalation. They are rungs
// of one thing a reader asked for once, so the rail is told once.
//
// The failure this prevents is not cosmetic: a second start carries a higher
// attempt, which outranks the first attempt's settle, so the occurrence would
// reopen and one request would report as several starts.
func TestOneLogicalCallAnnouncesItsStartOnce(t *testing.T) {
	starter := &countingStarter{}
	r := assembleRouter(nil, nil, ProfileEUHosted, &memoryMeter{}, StaticBudget(0), starter, nil, false, nil)
	lc := newLogicalCall()
	ctx := principal.WithCorrelationID(context.Background(), ids.NewV7())

	for range 3 {
		lc.announceRailStartOnce(ctx, r, TaskSummarize, []Tier{TierCheapCloud})
	}

	if len(starter.starts) != 1 {
		t.Fatalf("three attempts of one logical call announced %d starts, want 1", len(starter.starts))
	}
}

// A recorder that cannot reach Postgres is not asked to pretend. The DB-less
// local router and the cert lane both inject one, and the honest behaviour is
// no announcement rather than a no-op method they were forced to grow.
func TestARecorderThatCannotAnnounceIsNotAskedTo(t *testing.T) {
	r := assembleRouter(nil, nil, ProfileEUHosted, &memoryMeter{}, StaticBudget(0), &memCallStore{}, nil, false, nil)
	lc := newLogicalCall()
	ctx := principal.WithCorrelationID(context.Background(), ids.NewV7())

	// The assertion is that this does not panic and marks nothing: a type
	// assertion that succeeded against a recorder with no database would fail
	// inside AnnounceRailStart instead, one layer further from the cause.
	lc.announceRailStartOnce(ctx, r, TaskSummarize, []Tier{TierCheapCloud})

	if lc.railAnnounced {
		t.Error("a recorder that announces nothing marked the call announced, so a recorder that CAN announce would be skipped after it")
	}
}

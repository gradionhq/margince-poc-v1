// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// countingStarter records how often the rail was told a call began, and still
// records the calls themselves — so one recorder can drive a real router call
// and answer both halves of what that call owes the rail.
type countingStarter struct {
	fakeCallStore
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
		// The worst case is not one walk of the ladder. CompleteStructured
		// walks it up to maxLadderWalks times for ONE logical call, the start
		// is announced once, and the projection refuses a re-announcement at
		// the same attempt — so the lease has to cover every walk or a
		// structured call that legitimately retried renders stalled while it is
		// still working.
		worstCase := requestTimeout * time.Duration(rungs*maxLadderWalks)
		if got := railLease(ladder); got <= worstCase {
			t.Errorf("railLease over %d rungs = %s, which does not outlast the %s that %d walks of those rungs can spend — "+
				"a healthy structured call would render stalled", rungs, got, worstCase, maxLadderWalks)
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
	ladder := []Tier{TierCheapCloud}

	for range 3 {
		lc.announceRailStartOnce(ctx, r, TaskSummarize, ladder)
	}

	if len(starter.starts) != 1 {
		t.Fatalf("three attempts of one logical call announced %d starts, want 1", len(starter.starts))
	}
	// The lease is asserted here and nowhere else. Without it the recorder holds
	// a field no test reads, and the claim this whole file rests on — that the
	// starter is handed the DERIVED lease for the ladder in hand — would survive
	// that argument being dropped or replaced by a constant.
	if want := railLease(ladder); starter.leases[0] != want {
		t.Errorf("the start was leased for %s, want the derived %s", starter.leases[0], want)
	}
}

// A recorder that cannot reach Postgres is not asked to pretend. The DB-less
// local router and the cert lane both inject one, and the honest behaviour is
// no announcement rather than a no-op method they were forced to grow.
func TestARecorderThatCannotAnnounceIsNotAskedTo(t *testing.T) {
	starter := &countingStarter{}
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
	if len(starter.leases) != 0 {
		t.Errorf("a recorder with no database was handed %d lease(s)", len(starter.leases))
	}
}

// The PRODUCTION call site, which nothing else here exercises.
//
// Every other test in this file drives announceRailStartOnce by hand, and the
// integration tests drive CallMeter.AnnounceRailStart by hand. Both would pass
// against a router that never announces anything — which is exactly the
// behaviour this feature replaces. So this one serves a real call through
// serveCompletion and asserts the rail heard about it, with the identity it
// needs to pair the start with the settle that follows.
func TestServingACallAnnouncesItsStartToTheRail(t *testing.T) {
	starter := &countingStarter{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "answer"}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, starter,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-cheap"}},
		false, nil,
	)
	corr := ids.NewV7()
	ctx := principal.WithCorrelationID(wsCtx(), corr)

	if _, _, err := r.serveCompletion(ctx, TaskSummarize, []Tier{TierCheapCloud}, model.Request{}); err != nil {
		t.Fatalf("serving the call: %v", err)
	}

	if len(starter.starts) != 1 {
		t.Fatalf("serving one call announced %d starts, want 1", len(starter.starts))
	}
	got := starter.starts[0]
	if got.Task != TaskSummarize {
		t.Errorf("the start named task %q, want %q", got.Task, TaskSummarize)
	}
	// The correlation id is what pairs this start with its own settle. A start
	// carrying a different one opens an occurrence the flush never closes.
	if got.CorrelationID == nil || *got.CorrelationID != corr {
		t.Errorf("the start carried correlation %v, want %s — the settle would land on a different occurrence", got.CorrelationID, corr)
	}
}

// A structured call is ONE piece of work however many walks it takes, and the
// production path is what has to know that. Driven through CompleteStructured
// with a validator that never accepts, so all maxLadderWalks walks really run.
func TestAStructuredCallAnnouncesOneStartAcrossEveryWalk(t *testing.T) {
	starter := &countingStarter{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "answer"}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, starter,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-cheap"}},
		false, nil,
	)
	ctx := principal.WithCorrelationID(wsCtx(), ids.NewV7())
	rejectEverything := func(string) error { return errors.New("never valid") }

	if _, _, err := r.CompleteStructured(ctx, TaskSummarize, model.Request{}, rejectEverything); err == nil {
		t.Fatal("a validator that never accepts returned no error, so the retry and escalation did not run")
	}

	if len(starter.starts) != 1 {
		t.Fatalf("a structured call that walked the ladder every time announced %d starts, want 1 — "+
			"a second start outranks the first attempt's settle and reports one request as several", len(starter.starts))
	}
}

// The lease's worst case is arithmetic over maxLadderWalks, and this is what
// keeps that number honest: a fourth walk added to CompleteStructured without
// changing the constant would leave a healthy call rendering stalled before it
// finished. Counted from the calls the router actually recorded, so the number
// is observed rather than restated.
func TestStructuredWalksTheLadderNoMoreThanTheLeaseAssumes(t *testing.T) {
	starter := &countingStarter{}
	r := assembleRouter(
		map[Tier]model.Client{TierCheapCloud: stubClient{resp: model.Response{Text: "answer"}}},
		nil, ProfileCloudFrontier, stubMeter{}, unlimitedBudget{}, starter,
		map[Tier]routeMeta{TierCheapCloud: {provider: "openai", model: "gpt-cheap"}},
		false, nil,
	)
	ctx := principal.WithCorrelationID(wsCtx(), ids.NewV7())

	if _, _, err := r.CompleteStructured(ctx, TaskSummarize, model.Request{},
		func(string) error { return errors.New("never valid") }); err == nil {
		t.Fatal("a validator that never accepts returned no error")
	}

	// One rung, so each walk of the ladder is exactly one recorded attempt.
	if walks := len(starter.recorded); walks > maxLadderWalks {
		t.Errorf("CompleteStructured walked the ladder %d times and railLease is sized for %d — "+
			"a healthy call now outlives its own lease and renders stalled", walks, maxLadderWalks)
	}
}

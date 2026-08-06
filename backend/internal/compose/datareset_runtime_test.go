// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The reset's orchestration contract: purges run before the sweep, a drain that
// did not finish is reported rather than hidden, and the fleet always comes back
// up — from every failure, no earlier than the last purge, and even when the
// operator's client has already given up on the request.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// runPhase drives the runtime phase over a handler set with no injected
// surfaces: these cases assert the ResetRuntime ordering, nothing else.
func runPhase(rt ResetRuntime, sweep func(*resetCounts) error) (resetCounts, error) {
	return dataResetHandlers{}.runRuntimePhase(context.Background(), rt, ids.NewV7(), noOutbox, sweep)
}

// noOutbox stands in for the outbox drain in the cases that assert something
// else: these handler sets wire no pool, and the drain's own ordering has a
// test of its own.
func noOutbox() error { return nil }

// resetHandlers is a handler set whose only wiring is rt — the pause's lifetime
// belongs to runQuiesced, so every assertion about it goes through that.
func resetHandlers(rt ResetRuntime) dataResetHandlers {
	return dataResetHandlers{runtime: &rt, log: quietTestLogger()}
}

func TestPurgeFailureAbortsBeforeAnyDataIsSweptAndStillResumesTheFleet(t *testing.T) {
	resumed := false
	swept := false
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return true, nil },
		ResumeQueues:  func(context.Context) error { resumed = true; return nil },
		PurgeQueue:    func(context.Context, ids.UUID) (int, error) { return 0, errors.New("queue purge exploded") },
		PurgeBus:      func(context.Context) (int, int, error) { return 0, 0, nil },
		SignalReset:   func(context.Context, ids.UUID) error { return nil },
	}

	_, err := resetHandlers(rt).runQuiesced(context.Background(), ids.NewV7(), noOutbox, func(*resetCounts) error {
		swept = true
		return nil
	})

	if err == nil {
		t.Fatal("a failed purge must fail the reset; a half-purged install reported as clean is the worst outcome")
	}
	if swept {
		t.Error("data was swept after a purge failure; the purges run first so a failure leaves the data recoverable")
	}
	if !resumed {
		t.Error("the fleet stayed paused after a failure; resume is deferred precisely so this cannot happen")
	}
}

// TestTheOutboxIsDrainedBeforeTheStreamsArePurged: the outbox relay is not part
// of the job fleet the quiesce stops. A staged event still in event_outbox when
// the streams are emptied is shipped straight back into them, and a subscriber
// then works it against rows the sweep is deleting — the bus would clear itself
// only to re-fill from Postgres. Asserted through what each later step
// OBSERVES, not through a call log.
func TestTheOutboxIsDrainedBeforeTheStreamsArePurged(t *testing.T) {
	drained := false
	drainedAtBusPurge, drainedAtSweep := false, false
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return true, nil },
		ResumeQueues:  func(context.Context) error { return nil },
		PurgeBus: func(context.Context) (int, int, error) {
			drainedAtBusPurge = drained
			return 0, 0, nil
		},
	}

	_, err := resetHandlers(rt).runQuiesced(context.Background(), ids.NewV7(),
		func() error { drained = true; return nil },
		func(*resetCounts) error { drainedAtSweep = drained; return nil })
	if err != nil {
		t.Fatalf("runQuiesced: %v", err)
	}
	if !drainedAtBusPurge {
		t.Error("the streams were purged with events still staged in the outbox; the relay ships them into the streams moments later")
	}
	if !drainedAtSweep {
		t.Error("the sweep ran with events still staged for rows it is deleting")
	}
}

// TestAFailedOutboxDrainStopsTheResetBeforeTheBusIsPurged: purging streams the
// relay is about to re-fill would report a cleared bus that is not cleared, so
// the drain failing has to stop the reset where it stands — with the data still
// intact and the fleet lifted again.
func TestAFailedOutboxDrainStopsTheResetBeforeTheBusIsPurged(t *testing.T) {
	busPurged, swept, resumed := false, false, false
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return true, nil },
		ResumeQueues:  func(context.Context) error { resumed = true; return nil },
		PurgeBus:      func(context.Context) (int, int, error) { busPurged = true; return 0, 0, nil },
	}

	_, err := resetHandlers(rt).runQuiesced(context.Background(), ids.NewV7(),
		func() error { return errors.New("outbox drain exploded") },
		func(*resetCounts) error { swept = true; return nil })

	if err == nil {
		t.Fatal("a failed outbox drain must fail the reset rather than purge a bus that re-fills itself")
	}
	if busPurged {
		t.Error("the streams were purged although the outbox still holds the events that would re-fill them")
	}
	if swept {
		t.Error("data was swept after the outbox drain failed")
	}
	if !resumed {
		t.Error("the fleet stayed paused after the outbox drain failed")
	}
}

// TestAFailedQuiesceStillLiftsTheResetsOwnFleetPause: Quiesce pauses every
// queue and only THEN polls the drain, so it can fail — a cancelled request, a
// failed running-count read — with the pause live. Nothing else in the process
// is going to lift that pause, so this reset must, on the way out of a failure
// it is reporting rather than recovering from.
func TestAFailedQuiesceStillLiftsTheResetsOwnFleetPause(t *testing.T) {
	paused, resumed, swept := false, false, false
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) {
			paused = true
			return false, errors.New("counting running jobs failed after the pause landed")
		},
		ResumeQueues: func(context.Context) error { resumed, paused = true, false; return nil },
		PurgeQueue:   func(context.Context, ids.UUID) (int, error) { return 0, nil },
	}

	_, err := resetHandlers(rt).runQuiesced(context.Background(), ids.NewV7(), noOutbox, func(*resetCounts) error {
		swept = true
		return nil
	})

	if err == nil {
		t.Fatal("a failed quiesce must fail the reset; sweeping while a worker may be mid-transaction is what the pause prevents")
	}
	if swept {
		t.Error("data was swept although the fleet was never confirmed quiet")
	}
	if !resumed {
		t.Fatal("no resume was attempted after Quiesce failed — every queue in the installation stays paused with nobody left to lift it")
	}
	if paused {
		t.Error("the fleet is still paused after the reset returned")
	}
}

// TestTheFleetStaysPausedUntilEveryPostCommitPurgeHasRun: the surfaces no
// transaction can reach are cleared after the sweep commits, and a job that is
// working again in that window reads caches for data that is being deleted and
// can write objects the prefix sweep then removes. The pause therefore outlives
// the object sweep, the local cache flush and the fleet-wide announcement —
// asserted through what each of them OBSERVES, not through a call log.
func TestTheFleetStaysPausedUntilEveryPostCommitPurgeHasRun(t *testing.T) {
	paused := false
	pausedAtFlush, pausedAtAnnounce := false, false
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { paused = true; return true, nil },
		ResumeQueues:  func(context.Context) error { paused = false; return nil },
		SignalReset:   func(context.Context, ids.UUID) error { pausedAtAnnounce = paused; return nil },
	}
	blob := &pauseWatchingStore{Store: blobstore.NewMemory(), fleetPaused: &paused}
	h := resetHandlers(rt)
	h.blob = blob
	h.flush = func(ids.UUID) { pausedAtFlush = paused }

	if _, err := h.runQuiesced(context.Background(), ids.NewV7(), noOutbox, func(*resetCounts) error { return nil }); err != nil {
		t.Fatalf("runQuiesced: %v", err)
	}

	if !blob.sweptPrefix {
		t.Fatal("the object sweep never ran, so the ordering assertions below would prove nothing")
	}
	if !blob.pausedAtSweep {
		t.Error("the fleet was working again when the object bytes were swept; a job started in that window can write objects the sweep then deletes")
	}
	if !pausedAtFlush {
		t.Error("the fleet was working again before this process dropped its caches; a job would re-cache what was still being purged")
	}
	if !pausedAtAnnounce {
		t.Error("the fleet was working again before the reset was announced; a job would serve caches for data that no longer exists")
	}
	if paused {
		t.Error("the fleet is still paused after a clean reset returned")
	}
}

// pauseWatchingStore records what the reset's object sweep could observe about
// the job fleet at the moment it ran. Everything but the sweep is the real
// in-memory store's behaviour.
type pauseWatchingStore struct {
	blobstore.Store
	fleetPaused   *bool
	sweptPrefix   bool
	pausedAtSweep bool
}

func (s *pauseWatchingStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	s.sweptPrefix, s.pausedAtSweep = true, *s.fleetPaused
	return s.Store.DeletePrefix(ctx, prefix)
}

// TestTheResumeOutlivesTheAbandonedResetRequest: a reset is a bounded drain plus
// a full sweep and reseed, so an operator's client is exactly the sort to
// disconnect or time out partway. A resume riding that request context would be
// cancelled along with it and leave the fleet paused for the very reason it most
// needs lifting.
func TestTheResumeOutlivesTheAbandonedResetRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resumed := false
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return true, nil },
		ResumeQueues: func(resumeCtx context.Context) error {
			resumed = true
			if err := resumeCtx.Err(); err != nil {
				t.Errorf("the resume rode the request context (%v); the fleet would stay paused whenever the operator's client gives up", err)
			}
			if _, ok := resumeCtx.Deadline(); !ok {
				t.Error("the resume carries no deadline of its own; detaching it from the request must not make it unbounded")
			}
			return nil
		},
	}

	// The client gives up mid-sweep — the longest step, and the one it waits on.
	_, err := resetHandlers(rt).runQuiesced(ctx, ids.NewV7(), noOutbox, func(*resetCounts) error {
		cancel()
		return ctx.Err()
	})

	if err == nil {
		t.Fatal("an abandoned sweep must fail the reset rather than report a wipe it did not finish")
	}
	if !resumed {
		t.Fatal("no resume was attempted")
	}
}

func TestDrainTimeoutIsReportedAndDoesNotFailTheReset(t *testing.T) {
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return false, nil },
		ResumeQueues:  func(context.Context) error { return nil },
		PurgeQueue:    func(context.Context, ids.UUID) (int, error) { return 3, nil },
		PurgeBus:      func(context.Context) (int, int, error) { return 12, 41, nil },
		SignalReset:   func(context.Context, ids.UUID) error { return nil },
	}

	counts, err := runPhase(rt, func(*resetCounts) error { return nil })
	if err != nil {
		t.Fatalf("a drain timeout must not fail the reset: %v", err)
	}
	if !counts.DrainTimedOut {
		t.Error("DrainTimedOut = false; the operator would never learn a job was still running")
	}
	if counts.JobsDeleted != 3 || counts.StreamsPurged != 12 || counts.CacheKeys != 41 {
		t.Errorf("counts = %+v; every surface's tally must reach the response", counts)
	}
}

func TestAResetWithoutARuntimeStillSweepsPostgres(t *testing.T) {
	// A role that wired no runtime (no Redis, no River) resets what it can
	// reach rather than refusing to reset at all.
	swept := false
	counts, err := runPhase(ResetRuntime{}, func(*resetCounts) error {
		swept = true
		return nil
	})
	if err != nil {
		t.Fatalf("reset without a runtime: %v", err)
	}
	if !swept {
		t.Error("the Postgres sweep did not run")
	}
	if counts.JobsDeleted != 0 || counts.StreamsPurged != 0 {
		t.Errorf("counts = %+v, want zeros", counts)
	}
}

// TestResetSweepSeesThePurgeTalliesAndReportsItsOwn: the sweep is handed the
// counts the purges filled — that is how the audit row it writes inside the
// transaction can name them — and what it writes back comes out with them, so
// one struct is the single tally behind the response, the evidence and the log.
func TestResetSweepSeesThePurgeTalliesAndReportsItsOwn(t *testing.T) {
	rt := ResetRuntime{
		PurgeQueue: func(context.Context, ids.UUID) (int, error) { return 7, nil },
		PurgeBus:   func(context.Context) (int, int, error) { return 1, 2, nil },
	}

	counts, err := runPhase(rt, func(c *resetCounts) error {
		if c.JobsDeleted != 7 || c.StreamsPurged != 1 || c.CacheKeys != 2 {
			t.Errorf("sweep saw counts = %+v; the audit evidence it writes must name what the purges cleared", *c)
		}
		c.TablesCleared = 5
		return nil
	})
	if err != nil {
		t.Fatalf("runResetRuntimePhase: %v", err)
	}
	if counts.TablesCleared != 5 {
		t.Errorf("TablesCleared = %d, want 5", counts.TablesCleared)
	}
}

// TestResetRuntimeReachesTheHandlerInEitherOptionOrder: options run in the
// order the caller passed them, and a handler holding a COPY of the runtime
// would silently degrade the wipe to a table sweep whenever WithResetRuntime
// came second. Both orders must arrive at the same purge.
func TestResetRuntimeReachesTheHandlerInEitherOptionOrder(t *testing.T) {
	const purged = 9
	runtime := WithResetRuntime(ResetRuntime{
		PurgeQueue: func(context.Context, ids.UUID) (int, error) { return purged, nil },
	})
	reset := WithDataReset(nil, deployconfig.Seeds{}, runtimeenv.Development)

	for _, order := range []struct {
		name string
		opts []Option
	}{
		{"runtime first", []Option{runtime, reset}},
		{"reset first", []Option{reset, runtime}},
	} {
		t.Run(order.name, func(t *testing.T) {
			var s Server
			pool := &pgxpool.Pool{} // never dialed; the options only record it
			for _, opt := range order.opts {
				opt(&s, pool)
			}
			rt := s.runtime
			if rt == nil || rt.PurgeQueue == nil {
				t.Fatal("the handler cannot reach the wired runtime — the reset would sweep tables and leave the queue full")
			}
			n, err := rt.PurgeQueue(context.Background(), ids.NewV7())
			if err != nil || n != purged {
				t.Errorf("PurgeQueue = (%d, %v); want (%d, nil) — the handler reached a different runtime", n, err, purged)
			}
		})
	}
}

// TestTheDataResetReachesTheObjectStoreInEitherOptionOrder is the same trap on
// the other injected surface: object bytes outliving the rows that referenced
// them is exactly what this reset exists to prevent.
func TestTheDataResetReachesTheObjectStoreInEitherOptionOrder(t *testing.T) {
	store := blobstore.NewMemory()
	blob := WithBlobstore(store)
	reset := WithDataReset(nil, deployconfig.Seeds{}, runtimeenv.Development)

	for _, order := range []struct {
		name string
		opts []Option
	}{
		{"blobstore first", []Option{blob, reset}},
		{"reset first", []Option{reset, blob}},
	} {
		t.Run(order.name, func(t *testing.T) {
			// A fully assembled Server: WithBlobstore rewires handler sets that
			// must already exist, which is the composition every role boots.
			s := newServer(nil, quietTestLogger(), authHandlers{}, dealsHandlers{})
			for _, opt := range order.opts {
				opt(&s, nil)
			}
			if s.dataResetHandlers.blob != store {
				t.Error("the reset holds no object store — the swept rows' bytes would survive the wipe")
			}
		})
	}
}

// TestAFailedResumeDoesNotFailAnOtherwiseCleanReset: the fleet pause is this
// process's doing, so a resume failure is the operator's problem to see in the
// log — not a reason to report a completed reset as failed, which would send
// an admin to re-run a wipe that already succeeded.
func TestAFailedResumeDoesNotFailAnOtherwiseCleanReset(t *testing.T) {
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return true, nil },
		ResumeQueues:  func(context.Context) error { return errors.New("river is unreachable") },
	}

	h := resetHandlers(rt)
	if _, err := h.runQuiesced(context.Background(), ids.NewV7(), noOutbox, func(*resetCounts) error { return nil }); err != nil {
		t.Fatalf("resume failure must not fail the reset: %v", err)
	}
}

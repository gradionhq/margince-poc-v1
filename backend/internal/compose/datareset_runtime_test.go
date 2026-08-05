// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The reset's orchestration contract: purges run before the sweep, the fleet
// always comes back up, and a drain that did not finish is reported rather
// than hidden.

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

	_, err := runResetRuntimePhase(context.Background(), rt, ids.NewV7(), func(*resetCounts) error {
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

func TestDrainTimeoutIsReportedAndDoesNotFailTheReset(t *testing.T) {
	rt := ResetRuntime{
		QuiesceQueues: func(context.Context) (bool, error) { return false, nil },
		ResumeQueues:  func(context.Context) error { return nil },
		PurgeQueue:    func(context.Context, ids.UUID) (int, error) { return 3, nil },
		PurgeBus:      func(context.Context) (int, int, error) { return 12, 41, nil },
		SignalReset:   func(context.Context, ids.UUID) error { return nil },
	}

	counts, err := runResetRuntimePhase(context.Background(), rt, ids.NewV7(), func(*resetCounts) error { return nil })

	if err != nil {
		t.Fatalf("a drain timeout must not fail the reset: %v", err)
	}
	if !counts.DrainTimedOut {
		t.Error("DrainTimedOut = false; the operator would never learn a job was still running")
	}
	if counts.JobsCancelled != 3 || counts.StreamsPurged != 12 || counts.CacheKeys != 41 {
		t.Errorf("counts = %+v; every surface's tally must reach the response", counts)
	}
}

func TestAResetWithoutARuntimeStillSweepsPostgres(t *testing.T) {
	// A role that wired no runtime (no Redis, no River) resets what it can
	// reach rather than refusing to reset at all.
	swept := false
	counts, err := runResetRuntimePhase(context.Background(), ResetRuntime{}, ids.NewV7(), func(*resetCounts) error {
		swept = true
		return nil
	})
	if err != nil {
		t.Fatalf("reset without a runtime: %v", err)
	}
	if !swept {
		t.Error("the Postgres sweep did not run")
	}
	if counts.JobsCancelled != 0 || counts.StreamsPurged != 0 {
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

	counts, err := runResetRuntimePhase(context.Background(), rt, ids.NewV7(), func(c *resetCounts) error {
		if c.JobsCancelled != 7 || c.StreamsPurged != 1 || c.CacheKeys != 2 {
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
			rt := s.dataResetHandlers.runtime
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

	if _, err := runResetRuntimePhase(context.Background(), rt, ids.NewV7(), func(*resetCounts) error { return nil }); err != nil {
		t.Fatalf("resume failure must not fail the reset: %v", err)
	}
}

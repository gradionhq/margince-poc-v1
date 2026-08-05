// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The non-Postgres half of POST /admin/reset-data: the seam cmd injects the
// job-queue and event-bus purges through, the ordering that makes a failed
// reset recoverable, and the cache flush this process performs on itself.
// datareset.go holds the Postgres sweep and the HTTP transport.

import (
	"context"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ResetRuntime is the non-Postgres runtime a data reset must clear: the job
// queue, the event bus, and the cache-flush announcement. Each member is a
// func injected by cmd, which owns the Redis and River clients — compose names
// neither, and must not start (platform/overlaybudget/meter.go's RebindFrom
// records the same discipline).
//
// A zero value is legitimate: a role that wired no queue and no bus resets
// what it can reach instead of refusing to reset at all.
type ResetRuntime struct {
	// QuiesceQueues pauses the fleet and waits for running jobs, reporting
	// whether the drain completed.
	QuiesceQueues func(ctx context.Context) (drained bool, err error)
	// ResumeQueues lifts the pause. Called from a deferred path.
	ResumeQueues func(ctx context.Context) error
	// PurgeQueue deletes this workspace's queued work and the fleet
	// dispatchers, returning rows deleted.
	PurgeQueue func(ctx context.Context, ws ids.UUID) (int, error)
	// PurgeBus empties the event streams and the dedupe marks, returning
	// stream keys deleted and cache keys unlinked.
	PurgeBus func(ctx context.Context) (streams int, keys int, err error)
	// SignalReset announces the reset so every process drops its caches.
	SignalReset func(ctx context.Context, ws ids.UUID) error
}

// resetCounts is what a reset reports per surface — one tally, shared by the
// response, the audit evidence and the log line.
type resetCounts struct {
	TablesCleared  int
	JobsCancelled  int
	StreamsPurged  int
	CacheKeys      int
	ObjectsDeleted int
	DrainTimedOut  bool
}

// runRuntimePhase quiets the fleet, purges the queue, the bus and this
// workspace's budget counters, and then runs sweep — the Postgres half — with
// the fleet still paused. sweep receives the tally so far so the audit row it
// writes can name what the purges cleared, and writes its own back into it.
//
// The order is load-bearing. Purges run BEFORE the sweep so that a failure
// mid-purge leaves a safe partial state: the queue and bus are clear and the
// data is intact, which a re-run recovers. The reverse order would leave live
// events and queued jobs pointing at rows that no longer exist.
//
// Nothing here resumes the fleet. The caller owns the pause's whole lifetime
// (dataResetHandlers.runQuiesced) because a resume registered in this function
// would both miss a Quiesce that failed with the pause already applied and fire
// before the purges that run after the sweep commits.
func (h dataResetHandlers) runRuntimePhase(ctx context.Context, rt ResetRuntime, ws ids.UUID, sweep func(*resetCounts) error) (resetCounts, error) {
	var counts resetCounts
	if rt.QuiesceQueues != nil {
		drained, err := rt.QuiesceQueues(ctx)
		if err != nil {
			return counts, err
		}
		counts.DrainTimedOut = !drained
	}
	if rt.PurgeQueue != nil {
		n, err := rt.PurgeQueue(ctx, ws)
		if err != nil {
			return counts, err
		}
		counts.JobsCancelled = n
	}
	if rt.PurgeBus != nil {
		streams, keys, err := rt.PurgeBus(ctx)
		if err != nil {
			return counts, err
		}
		counts.StreamsPurged, counts.CacheKeys = streams, keys
	}
	// The overlay budget's counters are Redis keys exactly like the bus's dedupe
	// marks, they need nothing from the sweep's transaction, and purging them
	// here is what makes cache_keys_deleted one number: the audit row written
	// inside that transaction reports the same total as the response and the log
	// line. A meter with no Redis client purges nothing and reports zero.
	if h.budget != nil {
		n, err := h.budget.PurgeWorkspace(ctx, ws)
		if err != nil {
			return counts, err
		}
		counts.CacheKeys += n
	}
	if err := sweep(&counts); err != nil {
		return counts, err
	}
	return counts, nil
}

// resetResumeTimeout bounds the resume that lifts a reset's fleet pause. Modest
// on purpose: this is one queue-resume round trip, not a drain.
const resetResumeTimeout = 15 * time.Second

// resumeResetQueues lifts the fleet pause a reset took. It is called from a
// deferred path on EVERY exit — including a panic, and including a Quiesce that
// failed after its pause already landed — because a pause with nobody left to
// lift it wedges every queue in the installation.
//
// It deliberately does not run on the request context. A reset is a bounded
// drain plus a full sweep and reseed, which is precisely the request an
// operator's client abandons or times out on; a resume cancelled along with it
// would leave the fleet paused for the very reason it most needs lifting. So it
// runs detached, under its own deadline.
//
// A failure is logged rather than returned: a reset that already succeeded must
// not report as failed and send an admin to wipe the installation twice.
func resumeResetQueues(ctx context.Context, logger *slog.Logger, rt ResetRuntime) {
	if rt.ResumeQueues == nil {
		return
	}
	resumeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resetResumeTimeout)
	defer cancel()
	if err := rt.ResumeQueues(resumeCtx); err != nil {
		logger.Error("data reset: resuming the queues", "err", err)
	}
}

// FlushResetCaches drops this process's cached answers for ws after a reset.
//
// It covers what the Server itself holds: the per-workspace system-of-record
// mode and the auth lockout buckets. The model result cache is NOT here — no
// Server field carries a ModelPath (each role resolves its own), so the role
// that built the router composes that flush around this call.
func (s *Server) FlushResetCaches(ws ids.UUID) {
	if s.sorDispatch != nil {
		s.sorDispatch.Invalidate(ws)
	}
	s.ResetRateLimits()
}

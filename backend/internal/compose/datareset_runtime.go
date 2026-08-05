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

// runResetRuntimePhase quiets the fleet, purges the queue and the bus, and
// then runs sweep — the Postgres half — with the fleet still paused. sweep
// receives the tally so far so the audit row it writes can name what the
// purges cleared, and writes its own back into it.
//
// The order is load-bearing. Purges run BEFORE the sweep so that a failure
// mid-purge leaves a safe partial state: the queue and bus are clear and the
// data is intact, which a re-run recovers. The reverse order would leave live
// events and queued jobs pointing at rows that no longer exist. Resume is
// deferred so no failure path can leave the installation wedged.
func runResetRuntimePhase(ctx context.Context, rt ResetRuntime, ws ids.UUID, sweep func(*resetCounts) error) (resetCounts, error) {
	var counts resetCounts
	if rt.QuiesceQueues != nil {
		drained, err := rt.QuiesceQueues(ctx)
		if err != nil {
			return counts, err
		}
		counts.DrainTimedOut = !drained
	}
	if rt.ResumeQueues != nil {
		defer func() {
			// A pause this process took must not outlive it: logged rather than
			// returned, since a reset that already succeeded must not report as
			// failed and send an admin to wipe the installation twice.
			if err := rt.ResumeQueues(ctx); err != nil {
				slog.Default().Error("data reset: resuming the queues", "err", err)
			}
		}()
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
	if err := sweep(&counts); err != nil {
		return counts, err
	}
	return counts, nil
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
	s.authHandlers.ResetRateLimits()
}

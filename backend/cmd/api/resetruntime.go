// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/events"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// resetDrainWindow bounds how long a reset waits for running jobs before it
// proceeds and reports the timeout. A package constant, not an operator dial:
// this is a non-production convenience, and a knob nobody turns is a knob that
// rots.
const resetDrainWindow = 10 * time.Second

// resetDrainPoll is how often the drain re-reads the running count. Positive by
// construction — jobs.Quiescer tickers on it, and time.NewTicker panics on a
// non-positive interval.
const resetDrainPoll = 250 * time.Millisecond

// newResetRuntime assembles the non-Postgres purges for POST
// /admin/reset-data. It lives in cmd because it is where the River client and
// the Redis client are known — compose takes method values so that layer names
// neither.
func newResetRuntime(runner *jobs.Runner, pool *pgxpool.Pool, rdb *redis.Client) compose.ResetRuntime {
	q := jobs.Quiescer{
		Runner: runner, Pool: pool,
		Timeout: resetDrainWindow, Interval: resetDrainPoll, Now: time.Now,
	}
	return compose.ResetRuntime{
		QuiesceQueues: q.Quiesce,
		ResumeQueues:  q.Resume,
		PurgeQueue: func(ctx context.Context, ws ids.UUID) (int, error) {
			return jobs.PurgeWorkspace(ctx, pool, ws)
		},
		PurgeBus: func(ctx context.Context) (int, int, error) {
			streams, err := events.PurgeStreams(ctx, rdb, kevents.Groups())
			if err != nil {
				return 0, 0, err
			}
			keys, err := events.PurgeDedupe(ctx, rdb)
			if err != nil {
				return streams, 0, err
			}
			return streams, keys, nil
		},
		SignalReset: func(ctx context.Context, ws ids.UUID) error {
			return events.PublishReset(ctx, rdb, ws)
		},
	}
}

// resetLane is this role's whole participation in a data reset: the compose
// options that give POST /admin/reset-data its non-Postgres purges, and the
// control-channel listener that drops this process's caches whenever ANY
// process announces a reset — its own endpoint, another replica, or the worker.
//
// The zero value is the production lane: no options, and a listener that does
// not run.
type resetLane struct {
	opts []compose.Option
	rdb  *redis.Client
	log  *slog.Logger
	// serverFlush is Server.FlushResetCaches, taken while compose.New applies
	// opts: New returns an http.Handler and keeps the Server private, so the
	// option seam is the only place a role can hold that method value.
	serverFlush func(ids.UUID)
}

// newResetLane assembles the lane, or nothing at all in production. The
// endpoint's 404 there is the contract; a production process that holds no
// queue-pausing, stream-deleting machinery at all is the stronger guarantee
// behind it. env is the posture its caller already read for the endpoint
// itself — one read, so the two cannot disagree about which one is live.
//
// rdb is this role's shared Redis handle (sharedRedisClient): the reset purges
// the bus and announces itself over that one connection, never a new one.
func newResetLane(env runtimeenv.Environment, pool *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger) (*resetLane, error) {
	if !env.IsNonProduction() {
		return &resetLane{}, nil
	}
	// Insert-only, like every other River client this role builds: the reset
	// pauses queues, counts running jobs and deletes rows — it works none.
	runner, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return nil, err
	}
	lane := &resetLane{rdb: rdb, log: logger}
	lane.opts = []compose.Option{
		compose.WithResetRuntime(newResetRuntime(runner, pool, rdb)),
		// A bare Option rather than a With* constructor: what it needs is the
		// Server itself, which no compose entry point returns.
		func(s *compose.Server, _ *pgxpool.Pool) { lane.serverFlush = s.FlushResetCaches },
	}
	return lane, nil
}

// listen starts the reset control-channel subscriber for this process. It runs
// for the process's lifetime, so it is started once compose.New has applied the
// lane's options — that is when the Server's own flush exists — and returns
// immediately on the production lane, which assembled none.
func (l *resetLane) listen(ctx context.Context, modelPath *compose.ModelPath) {
	if l.rdb == nil {
		return
	}
	flush := l.flush(modelPath)
	go func() {
		// A subscription that ends is not cosmetic: this process would serve
		// pre-reset cached answers until it restarts. The filter is ctx.Err()
		// rather than the returned error, so a shutdown cancellation is the ONLY
		// quiet case — the subscription-closed sentinel stays loud.
		if err := events.SubscribeReset(ctx, l.rdb, l.log, flush); err != nil && ctx.Err() == nil {
			l.log.Error("data reset: the control channel stopped; this process serves stale caches until it restarts", "err", err)
		}
	}()
}

// flush is what this process drops when a reset is announced: what the Server
// holds (the system-of-record dispatch, the auth lockout buckets) plus the
// model result cache, which no Server field carries because each role resolves
// its own path.
//
// Both halves are optional for honest reasons — a role that resolved no model
// path has no cache to drop, one that built no router has no invalidation, and
// serverFlush exists only once compose.New has run — and a reset must drop what
// it can reach rather than take the process down over a cache.
func (l *resetLane) flush(modelPath *compose.ModelPath) func(ids.UUID) {
	return func(ws ids.UUID) {
		if l.serverFlush != nil {
			l.serverFlush(ws)
		}
		if modelPath != nil && modelPath.InvalidateCache != nil {
			modelPath.InvalidateCache(ids.From[ids.WorkspaceKind](ws))
		}
	}
}

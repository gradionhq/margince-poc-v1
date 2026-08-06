// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The api role's half of POST /admin/reset-data: the assembly of the purges
// compose calls through, the posture that decides whether any of it is built
// at all, and the cache flush the control channel runs. Every case here is a
// unit test — the closures are assembled, never invoked against a real queue
// or bus.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

func TestNewResetRuntimeWiresEverySurface(t *testing.T) {
	// Zero-value dependencies: this asserts the ASSEMBLY, and every member is a
	// closure that is not called here. Taking an already-built *jobs.Runner is
	// what keeps this a unit test — constructing one needs a real pool.
	rt := newResetRuntime(&jobs.Runner{}, nil, nil)

	// Every member must be non-nil: a nil one is a surface the reset silently
	// skips, and a skipped surface looks exactly like a cleared one.
	if rt.QuiesceQueues == nil || rt.ResumeQueues == nil || rt.PurgeQueue == nil ||
		rt.PurgeBus == nil || rt.SignalReset == nil {
		t.Errorf("ResetRuntime has a nil member: %+v", rt)
	}
}

// TestResetDrainWindowIsPositive: jobs.Quiescer tickers on Interval and
// deadlines on Timeout, and time.NewTicker PANICS on a non-positive interval.
// The struct validates neither, so this caller is what guarantees it.
func TestResetDrainWindowIsPositive(t *testing.T) {
	if resetDrainPoll <= 0 || resetDrainWindow <= 0 {
		t.Fatalf("drain window %v / poll %v must both be positive; a non-positive poll panics the reset mid-request",
			resetDrainWindow, resetDrainPoll)
	}
	if resetDrainPoll > resetDrainWindow {
		t.Errorf("poll %v outlasts the window %v; the drain would never re-read the running count", resetDrainPoll, resetDrainWindow)
	}
}

// TestNoResetLaneInProduction: the endpoint's own 404 is the contract, and a
// production process that holds no queue-pausing, stream-deleting machinery at
// all is the guarantee behind it. Nil dependencies prove nothing was
// constructed — anything that reached the pool or the bus would fail here.
func TestNoResetLaneInProduction(t *testing.T) {
	lane, err := newResetLane(runtimeenv.Production, nil, nil, discardLogger())
	if err != nil {
		t.Fatalf("newResetLane: %v", err)
	}
	if len(lane.opts) != 0 {
		t.Errorf("production lane contributes %d compose options; it must contribute none", len(lane.opts))
	}
	// Nothing to listen with either: a lane that subscribed would hold a bus
	// connection a production process never asked for.
	lane.listen(t.Context(), nil)
}

// TestResetLaneCapturesTheComposedServersOwnFlush: the api flushes its caches
// on every announcement, not only the one its own endpoint made (a worker's
// reset must reach it too), and Server.FlushResetCaches is reachable only
// while the options run — compose.New returns an http.Handler.
func TestResetLaneCapturesTheComposedServersOwnFlush(t *testing.T) {
	lane, err := newResetLane(runtimeenv.Development, nil, nil, discardLogger())
	if err != nil {
		t.Fatalf("newResetLane: %v", err)
	}
	var srv compose.Server
	for _, opt := range lane.opts {
		opt(&srv, nil)
	}
	if lane.serverFlush == nil {
		t.Fatal("no Server flush captured; an announced reset would leave this process serving pre-reset answers")
	}
	// A Server carrying no dispatcher and no lockout buckets must flush
	// nothing rather than panic: the caller is a goroutine, so a nil deref here
	// takes the whole process down.
	lane.flush(nil)(ids.NewV7())
}

// TestResetFlushDropsTheServersCachesAndTheModelCache: two owners, one
// announcement. The model result cache is NOT a Server field — each role
// resolves its own path — so a flush that only called the Server's would serve
// pre-reset completions for the rest of the cache TTL.
func TestResetFlushDropsTheServersCachesAndTheModelCache(t *testing.T) {
	ws := ids.NewV7()
	var gotServer, gotModel ids.UUID
	lane := &resetLane{serverFlush: func(w ids.UUID) { gotServer = w }}
	modelPath := &compose.ModelPath{InvalidateCache: func(w ids.WorkspaceID) { gotModel = w.UUID }}

	lane.flush(modelPath)(ws)

	if gotServer != ws {
		t.Errorf("the Server's caches were flushed for %v, want %v", gotServer, ws)
	}
	if gotModel != ws {
		t.Errorf("the model cache was flushed for %v, want %v", gotModel, ws)
	}
}

// TestResetFlushWithoutARouterStillFlushesTheServer: a role that resolved no
// model path (neither declared routing nor --ai-fake) has a nil path, and one
// that built no router has a nil InvalidateCache. Neither may cost the reset
// the flush it CAN perform.
func TestResetFlushWithoutARouterStillFlushesTheServer(t *testing.T) {
	for name, modelPath := range map[string]*compose.ModelPath{
		"no model path": nil,
		"no router":     {},
	} {
		t.Run(name, func(t *testing.T) {
			ws := ids.NewV7()
			var got ids.UUID
			lane := &resetLane{serverFlush: func(w ids.UUID) { got = w }}

			lane.flush(modelPath)(ws)

			if got != ws {
				t.Errorf("the Server's caches were flushed for %v, want %v", got, ws)
			}
		})
	}
}

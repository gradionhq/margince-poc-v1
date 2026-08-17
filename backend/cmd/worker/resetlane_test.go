// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The worker's stake in the data reset is one subscriber: the cache flush an
// announced reset triggers. It exists only to serve that reset, so an
// installation that never armed the reset holds no subscriber either — and
// must not, because the announcement channel is reachable by anyone who can
// publish to the bus, and a subscriber nobody asked for is a way to force cache
// misses on this role indefinitely.

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

func TestTheWorkerSubscribesToResetsOnlyWhenTheResetIsArmed(t *testing.T) {
	// Nil dependencies are the assertion: startResetLane reaches the Redis
	// handle the moment it decides to subscribe, so anything that got past the
	// gate would panic here rather than pass quietly.
	var background sync.WaitGroup
	startResetLane(t.Context(), false, nil, compose.ModelPath{}, &background, slog.New(slog.DiscardHandler))
	background.Wait()
}

func TestAnUnarmedWorkerIsTheDefaultInEveryPosture(t *testing.T) {
	// The switch travels on workerConfig, whose zero value is what a deployment
	// that said nothing gets. Fail-closed is not a posture question: a dev
	// installation that never armed the reset holds no subscriber either.
	var cfg workerConfig
	if cfg.allowDataReset {
		t.Fatal("a deployment that configured nothing armed the data reset")
	}
}

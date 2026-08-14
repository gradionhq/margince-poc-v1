// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The scheduling pass surfaces its own failure rather than reporting a green
// row over it.
//
// This is what the per-tenant guard used to prove and no longer can. That guard
// drove every workspace-role worker with args naming no workspace and required
// a refusal; a collapsed pass carries no workspace to omit (ADR-0103 §1), so
// the question it asked has no subject. The question underneath it survives
// intact: a pass whose store cannot be reached must FAIL, because a pass that
// returned nil would leave every due brief unseeded and unclaimed with a
// completed job row saying otherwise.
//
// The service is built with a nil handle on purpose — the same shape that guard
// used. A worker that reached past its store before checking would panic here
// rather than fail, so this pins the order too.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
)

func TestTheSchedulingPassFailsWhenItsStoreCannotBeReached(t *testing.T) {
	worker := &agentSchedulerWorker{
		svc: &RunnerService{
			store: runner.NewStore(nil),
			log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		now: func() time.Time { return time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC) },
	}

	err := worker.Work(context.Background(), &river.Job[AgentSchedulerArgs]{})
	if err == nil {
		t.Fatal("the pass reported success with no database behind it — every due brief goes unseeded and unclaimed, and the job row says the schedule ran")
	}
}

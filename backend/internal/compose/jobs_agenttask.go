// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//nolint:dupl // per-kind River wiring; see below for why the shape cannot be factored out
package compose

// River wiring for the MCP task-retention pass: a dispatcher over EVERY
// workspace and a worker that purges one. It sits beside the other per-concern
// job files rather than in jobs.go, which owns the runner's assembly.
//
// It reads almost exactly like jobs_retention.go, and that similarity cannot be
// factored away — hence the file-wide waiver above. addDeclaredWorker is generic
// over the CLOSED set of declared args types, so each kind needs its own
// concrete pair for the registration to compile at all; a shared helper would
// have to be generic over both the args type and its sweeper, and would then be
// one indirection wrapping two lines. It is why every jobs_*.go file in this
// package repeats the shape.

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// AgentTaskRetentionArgs schedules one purge of MCP tasks past their expiry.
// Always-on: a completed task stores a verbatim record read-back, so retaining
// it past the poll it exists to answer is subject data kept for no purpose.
type AgentTaskRetentionArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (AgentTaskRetentionArgs) Kind() string { return "agent_task_retention" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own. Held
// equal to api/jobs.yaml by TestArgsOwnedAttemptCapsMatchTheirDeclaration.
func (AgentTaskRetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// agentTaskRetentionWorker purges the expired tasks.
//
// It used to be a dispatcher enumerating EVERY workspace, archived ones
// included, because archiving does not un-store the results inside one. That
// reason is served by the delete itself now: agent_task carries no tenant
// column (ADR-0091 §8 phase D), so one pass reaches every row a fan-out would
// have visited tenant by tenant.
type agentTaskRetentionWorker struct {
	sweeper  *AgentTaskRetentionSweeper
	identity *identity.Service
}

func (w *agentTaskRetentionWorker) Work(ctx context.Context, _ *river.Job[AgentTaskRetentionArgs]) error {
	// The installation is bound because the sweep's audit and system_log writes
	// still stamp a workspace from the context; it retires with
	// storekit.MustWorkspace (ADR-0091 §5).
	passCtx, err := installationJobCtx(ctx, w.identity)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.sweeper.Sweep(passCtx))
}

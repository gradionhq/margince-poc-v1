// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the Surface-B agent scheduler (architecture/07): a
// dispatcher over every LIVE workspace and a worker that seeds and executes
// one tenant's due agent jobs. It registers itself — workers and periodic
// schedule together — so jobs.go, which owns the runner's assembly, grows one
// line as this surface does.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const (
	// agentSchedulerQueue keeps the scheduler off the default queue. One
	// workspace's pass executes up to claimBatch agent runs back to back, each
	// entitled to the full RunWallClock, so a fanned-out fleet landing on
	// default would hold its five workers for hours and stall every short
	// maintenance job beside them.
	agentSchedulerQueue = "agent_scheduler"
	// Two workers, matching the other model-bound queues (deep read, the
	// AI-backed captures): the same species of work — long, model-bound, and
	// fine to run behind the short maintenance jobs — while still keeping one
	// tenant's hour-long batch from being the whole fleet's scheduling latency.
	agentSchedulerMaxWorkers = 2
	// agentSchedulerPassTimeout is the arithmetic behind
	// agent_scheduler_workspace's declared timeout: a pass executes up to
	// claimBatch runs sequentially and RunWallClock is each run's own ceiling,
	// and the margin covers the seed/claim/finish round trips between them,
	// which the pool bounds rather than this cap.
	//
	// api/jobs.yaml carries the value River is actually handed, so moving this
	// number alone moves no wall clock; the declaration names this constant in
	// its derived timeout and TestEveryTranscribedTimeoutStillEqualsItsGoConstant
	// keeps the two equal.
	agentSchedulerPassTimeout = claimBatch*RunWallClock + 5*time.Minute
)

// AgentSchedulerConfig is the agent scheduler's slice of the runner's boot
// configuration.
type AgentSchedulerConfig struct {
	// Interval is the dispatcher's cadence — the operator-facing
	// --runner-interval, taken verbatim as the River schedule. It paces the
	// FLEET fan-out, not an agent's own schedule: the catalog's daily due hour
	// is what decides when a brief runs, and this dial only decides how
	// promptly a due occurrence is noticed and how often a claimable backlog is
	// drained.
	//
	// Non-positive schedules no agent dispatch; api/jobs.yaml declares it.
	Interval time.Duration
	// Service is the assembled Surface-B runner one workspace's pass ticks —
	// the SAME instance the role's cg:overnight-agent consumer resumes parked
	// runs through, so a deployment holds one governed registry and one brain
	// rather than two that could drift apart.
	//
	// Nil is a role with no declared model, and there is then no brain to run a
	// brief with: absent by omission, the posture JobRunnerConfig states.
	Service *RunnerService
	// Now is the clock a workspace pass reads due-ness from. Nil takes the wall
	// clock, which is what every process role passes; the acceptance suites pin
	// it, because a catalog occurrence falls due at a fixed UTC hour and a
	// suite left on the wall clock would assert nothing at all for the hours of
	// the day when nothing is due.
	Now func() time.Time
}

// addAgentSchedulerJobs registers the scheduler workers and returns the
// dispatcher's periodic schedule for the caller to append. A non-positive
// interval registers the workers but no schedule — the posture the declaration
// states and jobschedule.go resolves.
func addAgentSchedulerJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig) []*river.PeriodicJob {
	if cfg.AgentScheduler.Service == nil {
		return nil
	}
	now := cfg.AgentScheduler.Now
	if now == nil {
		now = time.Now
	}
	addDeclaredWorker[AgentSchedulerArgs](reg, &agentSchedulerWorker{pool: pool})
	addDeclaredWorker[AgentSchedulerWorkspaceArgs](reg, &agentSchedulerWorkspaceWorker{svc: cfg.AgentScheduler.Service, now: now})
	return periodicFor(cfg, AgentSchedulerArgs{})
}

// AgentSchedulerArgs schedules one fleet-wide agent-scheduling pass.
type AgentSchedulerArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (AgentSchedulerArgs) Kind() string { return "agent_scheduler" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (AgentSchedulerArgs) FleetWide() {}

// agentSchedulerWorker is the dispatcher. It enumerates the LIVE workspaces
// only: an archived tenant has nobody to read a morning brief, so seeding one
// would spend model budget producing a report no one will open.
type agentSchedulerWorker struct {
	pool *pgxpool.Pool
}

func (w *agentSchedulerWorker) Work(ctx context.Context, _ *river.Job[AgentSchedulerArgs]) error {
	return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
		workspaceSweepOpts(AgentSchedulerWorkspaceArgs{}.Kind()),
		func(ws ids.UUID) river.JobArgs { return AgentSchedulerWorkspaceArgs{Workspace: ws} }))
}

// AgentSchedulerWorkspaceArgs seeds and executes one workspace's due agent jobs.
type AgentSchedulerWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (AgentSchedulerWorkspaceArgs) Kind() string { return "agent_scheduler_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a AgentSchedulerWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// agentSchedulerWorkspaceWorker ticks one workspace.
type agentSchedulerWorkspaceWorker struct {
	svc *RunnerService
	now func() time.Time
}

// Work binds the tenant and nothing else: each claimed job resolves its own
// passport and mints its own correlation id inside TickWorkspace, so binding a
// pass-level actor here would relabel every run's audit rows as the scheduler's
// rather than the agent's.
func (w *agentSchedulerWorkspaceWorker) Work(ctx context.Context, job *river.Job[AgentSchedulerWorkspaceArgs]) error {
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.svc.TickWorkspace(wsCtx, w.now()))
}

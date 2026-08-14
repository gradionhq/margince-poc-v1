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
	// its derived timeout and the job census keeps the two equal.
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
	addDeclaredWorker[AgentSchedulerArgs](reg, &agentSchedulerWorker{svc: cfg.AgentScheduler.Service, now: now})
	return periodicFor(cfg, AgentSchedulerArgs{})
}

// AgentSchedulerArgs seeds and executes the due agent jobs.
//
// It used to be a dispatcher that enumerated live workspaces and enqueued one
// child per tenant. Under one installation that enumeration reads a single row,
// so the pair collapsed into this (ADR-0091 §5, ADR-0103 §1).
type AgentSchedulerArgs struct{}

// Kind is the stable job identifier River persists in river_job. It is the
// DISPATCHER's old kind, deliberately: it is the name operators alert on.
func (AgentSchedulerArgs) Kind() string { return "agent_scheduler" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own. One
// attempt is the declared posture and the reason is in api/jobs.yaml: a second
// rung would spend a fresh batch of model budget on work the next tick repeats,
// and a backing-off row suppresses that tick. Held equal to the declaration by
// TestArgsOwnedAttemptCapsMatchTheirDeclaration.
func (AgentSchedulerArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       agentSchedulerQueue,
		MaxAttempts: 1,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// agentSchedulerWorker seeds and executes the due jobs.
type agentSchedulerWorker struct {
	svc *RunnerService
	now func() time.Time
}

// Work binds no pass-level actor: each claimed job resolves its own passport
// and mints its own correlation id inside the tick, so an actor bound here
// would relabel every run's audit rows as the scheduler's rather than the
// agent's.
func (w *agentSchedulerWorker) Work(ctx context.Context, _ *river.Job[AgentSchedulerArgs]) error {
	return jobs.FaultContext(ctx, w.svc.Tick(ctx, w.now()))
}

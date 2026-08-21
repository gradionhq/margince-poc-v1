// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The Surface-B runner, assembled: catalog seeding, job claiming, run
// execution and approval-decision resume — composed here because the
// pieces span three modules (agents/runner drives, identity resolves
// the passport, ai routes the brain) that never import each other.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// RunWallClock is the §4 wall-clock guarantee (RATIFY default 15 min):
// the third bound alongside steps and output tokens.
const RunWallClock = 15 * time.Minute

// claimBatch bounds how many due jobs one tick executes per workspace.
const claimBatch = 4

// RunnerService drives scheduled Surface-B runs. It is the WORKER's
// entry point: TickWorkspace seeds + executes one tenant's due jobs,
// HandleEvent resumes suspended runs when their approval is decided.
type RunnerService struct {
	store     *runner.Store
	runner    *runner.Runner
	identity  *identity.Service
	retriever retrieval.Retriever
	log       *slog.Logger
	// specByName resolves a stored job's catalog entry. It defaults to
	// runner.SpecByName — the catalog IS code and this does not make it
	// configuration.
	//
	// It is a seam because the integration lane needs a spec that can stage
	// an approval, and no shipped agent has one: every tool both catalog
	// entries name is auto-execute, so the runner's suspend/approve/resume
	// path is unreachable from the catalog. Before the allowlist bound, that
	// path was reached by driving the sweep to archive_record — an action its
	// own goal forbids — so the coverage was only ever there by way of a
	// prohibited call. Rather than lose it, the lane reaches for the wiring.
	specByName func(string) (runner.AgentSpec, bool)
}

// RunnerOption adjusts a RunnerService at construction.
type RunnerOption func(*RunnerService)

// WithSpecResolver replaces the catalog lookup. ONLY the integration lane
// passes it: production always resolves against runner.Catalog, and a
// deployment cannot reach this.
func WithSpecResolver(resolve func(string) (runner.AgentSpec, bool)) RunnerOption {
	return func(s *RunnerService) { s.specByName = resolve }
}

// NewRunnerService assembles the runner over the SAME governed registry
// every other agent surface dispatches through — the two-directions
// invariant is a property of this constructor: there is no other
// registry to hand it. resolveIncumbent is the per-workspace live-incumbent
// resolver the overlay write-back path reaches HubSpot through when a
// Surface-B run's agent tool writes a record; the worker passes a FromEnv
// vault-backed resolver, and nil degrades write-back to errNoWriteIncumbent
// (reads and non-SoR tools are unaffected).
func NewRunnerService(pool *pgxpool.Pool, brain runner.Brain, draftBrain completer, retriever retrieval.Retriever, log *slog.Logger, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath, opts ...RunnerOption) *RunnerService {
	svc := &RunnerService{
		store:      runner.NewStore(InstallationDB(pool)),
		runner:     runner.New(registryWithDraftBrain(pool, draftBrain, resolveIncumbent, send), brain),
		identity:   identity.NewService(pool),
		specByName: runner.SpecByName,
		retriever:  retriever,
		log:        log,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Tick is the installation's scheduler pass: close the runs abandoned since
// last time, seed the catalog occurrences due at now, then execute the jobs it
// can claim. ctx must already carry the workspace — the caller binds it, so
// this pass can only ever touch the tenant it was handed.
//
// Three failures, three different destinations, because each belongs to a
// different row. A seeding or claiming failure is returned, and returning it is
// the point: it is this tenant's pass that could not run, and its own job row is
// where that has to land. Execution failures do NOT come back here — executeJob
// records each on the job row it belongs to, because a brief that never ran must
// say why on the row an operator reads, not take the whole pass down with it. The
// sweep's own failure is only logged: it owns no row of this pass, and a tenant's
// schedule must still run when the accounting for last week's crash cannot.
func (s *RunnerService) Tick(ctx context.Context, now time.Time) error {
	now = now.UTC()
	// The pass's own identity, bound once for everything it does.
	//
	// It is not decoration: seeding, sweeping and finishing all announce their
	// occurrence to the AI-activity projection now, and an announcement carries
	// the write shape — a ledger row and an outbox row, both of which take their
	// actor from the context. A pass with no actor bound could not write either,
	// and the rail would silently never learn that the 06:00 brief was queued.
	ctx = schedulerContext(ctx)
	s.reapAbandonedRuns(ctx)
	for _, spec := range runner.Catalog() {
		if due := spec.DueAt(now); !now.Before(due) {
			// Cron-seeded jobs carry no passport yet: execution fails
			// loudly rather than running with ambient authority.
			if err := s.store.EnqueueJob(ctx, spec.Name, spec.TriggerRef(now), nil, due); err != nil {
				return err
			}
		}
	}
	jobs, err := s.store.ClaimDueJobs(ctx, claimBatch)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		s.executeJob(ctx, job)
	}
	return nil
}

// schedulerActor is who the scheduling pass is. The runs it seeds are executed
// under their own passports later; this names only the pass that placed them.
const schedulerActor = "system:agent_scheduler"

// resumeActor is who closes or resumes a parked run when an approval is
// decided. The resumed leg itself runs under the run's own passport; this names
// only the consumer that reacted to the decision.
const resumeActor = "system:agent_resume"

// schedulerContext binds the pass's actor and one correlation id, so every row
// a single tick writes groups under the tick that wrote it.
func schedulerContext(ctx context.Context) context.Context {
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: schedulerActor,
	})
}

// stuckRunGrace is how far past its wall clock a 'running' row must be before
// the sweep calls it abandoned.
//
// TWICE RunWallClock, not once, because updated_at is stamped when the run starts
// and nothing bumps it while the loop runs: a live run's row ages at wall-clock
// speed, so a run that spends its whole budget is already a full RunWallClock old
// when it writes its outcome. One wall clock of grace would put that write inside
// the window. SaveOutcome would then correct the status — it guards on id, not on
// status — but an operator had already been told a run was abandoned, and for a
// resumed run that is a lie about a mutation a human approved.
const stuckRunGrace = 2 * RunWallClock

// abandonedRunReason states what the sweep OBSERVED, and never why. The predicate
// is a status and an age; it cannot tell a died-mid-resume from a first-leg run
// whose process was killed, so naming either mechanism would put an unverifiable
// claim on the only record an operator gets. What it can say is the part that
// changes what they do next: the run's tools write as they execute, and trace is
// only persisted at SaveOutcome, so a swept row is empty and yet its writes may
// well have landed.
const abandonedRunReason runner.FailureReason = "abandoned: still running past twice the run wall clock, so no process is " +
	"coming back for it. Its tools may already have written — check the audit log for this run id " +
	"before assuming nothing landed."

// reapAbandonedRuns closes the runs nothing will ever finish, in the tenant ctx
// is bound to. The invariant that makes them unrecoverable belongs to
// runner.Store.FailStuckRuns; what is decided HERE is only where the sweep runs
// and what happens when it fails.
//
// It rides the scheduling pass because that is the tenant loop that already
// exists and is already bound to one workspace. Best-effort inside it: a sweep
// failure must not take down the pass, whose actual job is this tenant's
// schedule. It is reported instead, because a sweep that keeps failing means runs
// are piling up in 'running' where nothing will read them.
func (s *RunnerService) reapAbandonedRuns(ctx context.Context) {
	swept, err := s.store.FailStuckRuns(ctx, stuckRunGrace, abandonedRunReason)
	if err != nil {
		s.log.Error("runner: sweeping abandoned runs", "err", err)
		return
	}
	if len(swept) > 0 {
		// The ids, not just a count: each of these may be a run a human approved
		// and never saw the end of, and an operator who cannot name one cannot go
		// read its audit trail to find out whether its writes landed. One line
		// carrying them all rather than a line each, because the occasion for this
		// log is a crash that stranded everything at once.
		s.log.Warn("runner: closed abandoned runs",
			"count", len(swept), "runs", swept, "stale_for", stuckRunGrace)
	}
}

// executeJob runs one claimed job to its outcome. Failures land on the
// job row — a brief that never ran must say why, not vanish.
func (s *RunnerService) executeJob(ctx context.Context, job runner.QueuedJob) {
	spec, known := s.specByName(job.SpecName)
	if !known {
		s.finishJob(ctx, job.ID, nil, fmt.Sprintf("agent spec %q is not in the catalog", job.SpecName))
		return
	}
	if job.PassportID == nil {
		s.finishJob(ctx, job.ID, nil,
			"no passport bound: mint one (POST /v1/passports) and bind it to the job before the run can act")
		return
	}
	agentIdentity, err := s.identity.AuthenticateAgentByID(ctx, *job.PassportID)
	if err != nil {
		// The cause goes to the operator, never onto the row: this reason is
		// announced to the AI-activity rail, where an ordinary rep reads it, and
		// identity's own message names internals they cannot act on.
		s.log.Warn("runner: a job's passport would not resolve",
			"job", job.ID, "trigger_ref", job.TriggerRef, "cause", err)
		s.finishJob(ctx, job.ID, nil, string(runner.FailureCouldNotStart))
		return
	}
	// One correlation id per run: every event the run's writes emit
	// groups under it (events.md — "one originating request/agent-run").
	runCtx := principal.WithCorrelationID(principal.WithActor(ctx, agentIdentity.Principal()), ids.NewV7())

	runID, created, err := s.store.StartRun(runCtx, spec, job.TriggerRef, *job.PassportID)
	if err != nil {
		// Same rule as the resolution failure above: a write error carries the
		// driver's words, which must not reach the column a rep reads.
		s.log.Warn("runner: a run row could not be written",
			"job", job.ID, "trigger_ref", job.TriggerRef, "cause", err)
		s.finishJob(ctx, job.ID, nil, string(runner.FailureCouldNotStart))
		return
	}
	if !created {
		// This occurrence already ran (or is suspended) — the job was a
		// duplicate trigger and idempotency absorbed it.
		s.finishJob(ctx, job.ID, nil, "")
		return
	}
	// Every ai_call the run's model lane makes stamps this run — the
	// trace that ties a routed model call back to the Surface-B run it
	// served.
	runCtx = principal.WithAgentRunID(runCtx, runID)

	bounded, cancel := context.WithTimeout(runCtx, RunWallClock)
	defer cancel()
	// Grounding runs under the run's OWN deadline: RunWallClock has to bound
	// everything the run does, or it does not bound the run. On the retriever's
	// own timeout it would otherwise age the row toward the abandoned sweep while
	// the loop had not started a single step — and grounding already degrades to
	// an ungrounded run rather than failing one.
	grounding := s.seedGrounding(bounded, spec.Goal)
	res, err := s.runner.Run(bounded, runner.Job{
		Goal:       spec.Goal,
		TriggerRef: job.TriggerRef,
		Budget:     spec.Budget,
		Tools:      spec.Tools,
		Grounding:  grounding,
	})
	s.landOutcome(runCtx, runID, job.TriggerRef, res, err)
	s.finishJob(ctx, job.ID, &runID, "")
}

// HandleEvent is the cg:overnight-agent consumer: an approval decision
// on a runner staging resumes the parked run with the human's answer.
// Every other event on the group's streams is not ours — nil, not an
// error, so the group keeps flowing.
func (s *RunnerService) HandleEvent(ctx context.Context, env kevents.Envelope) error {
	if env.Type != "approval.decided" {
		return nil
	}
	approvalID := ids.From[ids.ApprovalKind](env.Entity.ID)
	// The envelope carries no tenant (ADR-0091 §6): this consumer resolves the
	// installation, exactly as the request paths beside it do.
	ws, err := s.identity.InstallationWorkspace(ctx)
	if err != nil {
		return err
	}
	ctx = principal.WithWorkspaceID(ctx, ws.UUID)
	// The resume path's own actor, for the same reason Tick binds one: every
	// terminal write below announces the occurrence to the AI-activity
	// projection, and an announcement carries the write shape — a ledger row and
	// an outbox row, both of which take their actor from the context. Without it
	// MarkFailed rolls back, the claim is not undone, and the run is parked
	// forever in a state no redelivery can close.
	ctx = principal.WithCorrelationID(ctx, env.EventID)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: resumeActor,
	})

	// The payload is read BEFORE the run is claimed: claiming is one-way, so
	// every step after it must end in a terminal status rather than in a
	// retriable error — a redelivery would find nothing to resume and leave
	// the run parked in 'running' forever.
	var payload struct {
		Verdict      string          `json:"verdict"`
		Edited       bool            `json:"edited"`
		EditedChange json.RawMessage `json:"edited_change"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return fmt.Errorf("runner: approval.decided payload: %w", err)
	}

	// Claim, don't just look: the bus is at-least-once and a resumed run is
	// a fresh loop with a fresh budget, not an idempotent effect.
	suspended, found, err := s.store.ClaimSuspendedByApproval(ctx, approvalID)
	if err != nil {
		return err
	}
	if !found {
		return nil // a human-surface approval, or a decision already resumed
	}
	// Modify-then-approve (ADR-0036 §4): the authority now binds to the
	// HUMAN's version of the call, so the resumed run must re-present
	// exactly that — the originally staged args no longer redeem.
	if payload.Verdict == "approved" && payload.Edited {
		if len(payload.EditedChange) == 0 {
			return s.store.MarkFailed(ctx, suspended.RunID, runner.FailureEditedApprovalCarriedNoChange)
		}
		suspended.Pending.Args = payload.EditedChange
	}

	agentIdentity, err := s.identity.AuthenticateAgentByID(ctx, suspended.PassportID)
	if err != nil {
		// The passport died while the run was parked (revoked, expired,
		// human deactivated). The run cannot act anymore — close it. WHICH of
		// those happened is the identity module's own message, so it goes to the
		// operator and not to the column the person reads.
		s.log.Warn("runner: a suspended run's authority died before it could resume",
			"trigger_ref", suspended.TriggerRef, "run", suspended.RunID, "cause", err)
		return s.store.MarkFailed(ctx, suspended.RunID, runner.FailurePassportNoLongerValid)
	}
	// The resumed leg is the SAME logical run but a new causal moment;
	// it groups its writes under a fresh correlation id.
	runCtx := principal.WithCorrelationID(principal.WithActor(ctx, agentIdentity.Principal()), ids.NewV7())
	runCtx = principal.WithAgentRunID(runCtx, suspended.RunID)

	spec, known := s.specByName(suspended.SpecName)
	if !known {
		s.log.Warn("runner: a suspended run's agent left the catalog",
			"trigger_ref", suspended.TriggerRef, "run", suspended.RunID, "spec", suspended.SpecName)
		return s.store.MarkFailed(ctx, suspended.RunID, runner.FailureSpecLeftTheCatalog)
	}

	bounded, cancel := context.WithTimeout(runCtx, RunWallClock)
	defer cancel()
	// Tools rides the CURRENT catalog entry, beside the current budget and
	// for the same reason: a suspended run resumes under the authority the
	// entry states now, never the one it stated when the call was staged.
	res, err := s.runner.Resume(bounded, runner.Job{
		Goal:       suspended.Goal,
		TriggerRef: suspended.TriggerRef,
		Budget:     spec.Budget,
		Tools:      spec.Tools,
	}, runner.Decision{
		Pending:  suspended.Pending,
		Approved: payload.Verdict == "approved",
	})
	s.landOutcome(runCtx, suspended.RunID, suspended.TriggerRef, res, err)
	return nil
}

// landOutcome persists how a run ended. triggerRef names the occurrence for the
// operator log, which is where a fault's cause goes: the run's own error is a
// wrapped internal one, and agent_run.degrade_reason is read by the human the run
// acted for.
func (s *RunnerService) landOutcome(ctx context.Context, runID ids.UUID, triggerRef string, res runner.Result, runErr error) {
	if runErr != nil {
		s.log.Error("runner: a run faulted outside its own degrade path",
			"trigger_ref", triggerRef, "run", runID, "cause", runErr)
		if err := s.store.MarkFailed(ctx, runID, runner.FailureRunFaulted); err != nil {
			s.log.Error("runner: marking run failed", "run", runID, "err", err)
		}
		return
	}
	if err := s.store.SaveOutcome(ctx, runID, res); err != nil {
		s.log.Error("runner: saving outcome", "run", runID, "err", err)
	}
}

func (s *RunnerService) finishJob(ctx context.Context, jobID ids.UUID, runID *ids.UUID, failReason string) {
	if failReason != "" {
		s.log.Warn("runner: job failed", "job", jobID, "reason", failReason)
	}
	if err := s.store.FinishJob(ctx, jobID, runID, failReason); err != nil {
		s.log.Error("runner: finishing job", "job", jobID, "err", err)
	}
}

// seedGrounding retrieves T2 seed context for the run's goal under the
// AGENT's own principal — the run grounds on exactly what its passport
// may see, and a retrieval failure degrades to an ungrounded run
// rather than blocking the brief.
func (s *RunnerService) seedGrounding(ctx context.Context, goal string) []runner.Grounding {
	if s.retriever == nil {
		return nil
	}
	found, err := s.retriever.Search(ctx, retrieval.Query{Text: goal, Limit: 5})
	if err != nil {
		s.log.Warn("runner: seed retrieval failed — running ungrounded", "err", err)
		return nil
	}
	// The ranking's kind is deliberately not threaded into the grounding: a
	// seed enters the run at T2 whichever lane surfaced it, and the run's own
	// answer carries that tier. A lexically-ranked seed is a less RELEVANT
	// seed, not a less trustworthy one.
	grounding := make([]runner.Grounding, 0, len(found.Hits))
	for _, hit := range found.Hits {
		for _, ev := range hit.Evidence {
			grounding = append(grounding, runner.Grounding{
				SourceID:  ev.Source,
				TrustTier: "T2",
				Content:   ev.Snippet,
			})
		}
	}
	return grounding
}

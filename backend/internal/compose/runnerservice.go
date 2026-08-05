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
}

// NewRunnerService assembles the runner over the SAME governed registry
// every other agent surface dispatches through — the two-directions
// invariant is a property of this constructor: there is no other
// registry to hand it. resolveIncumbent is the per-workspace live-incumbent
// resolver the overlay write-back path reaches HubSpot through when a
// Surface-B run's agent tool writes a record; the worker passes a FromEnv
// vault-backed resolver, and nil degrades write-back to errNoWriteIncumbent
// (reads and non-SoR tools are unaffected).
func NewRunnerService(pool *pgxpool.Pool, brain runner.Brain, draftBrain completer, retriever retrieval.Retriever, log *slog.Logger, resolveIncumbent func(context.Context) (overlay.Incumbent, error), send SendPath) *RunnerService {
	return &RunnerService{
		store:     runner.NewStore(pool),
		runner:    runner.New(registryWithDraftBrain(pool, draftBrain, resolveIncumbent, send), brain),
		identity:  identity.NewService(pool),
		retriever: retriever,
		log:       log,
	}
}

// TickWorkspace is ONE tenant's scheduler pass: close the runs abandoned since
// last time, seed the catalog occurrences due at now, then execute the jobs it
// can claim. wsCtx must already carry the workspace — the caller binds it, so
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
func (s *RunnerService) TickWorkspace(wsCtx context.Context, now time.Time) error {
	now = now.UTC()
	s.reapAbandonedRuns(wsCtx)
	for _, spec := range runner.Catalog() {
		if due := spec.DueAt(now); !now.Before(due) {
			// Cron-seeded jobs carry no passport yet: execution fails
			// loudly rather than running with ambient authority.
			if err := s.store.EnqueueJob(wsCtx, spec.Name, spec.TriggerRef(now), nil, due); err != nil {
				return err
			}
		}
	}
	jobs, err := s.store.ClaimDueJobs(wsCtx, claimBatch)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		s.executeJob(wsCtx, job)
	}
	return nil
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
const abandonedRunReason = "abandoned: still running past twice the run wall clock, so no process is " +
	"coming back for it. Its tools may already have written — check the audit log for this run id " +
	"before assuming nothing landed."

// reapAbandonedRuns closes the runs nothing will ever finish, in the tenant wsCtx
// is bound to. The invariant that makes them unrecoverable belongs to
// runner.Store.FailStuckRuns; what is decided HERE is only where the sweep runs
// and what happens when it fails.
//
// It rides the scheduling pass because that is the tenant loop that already
// exists and is already bound to one workspace. Best-effort inside it: a sweep
// failure must not take down the pass, whose actual job is this tenant's
// schedule. It is reported instead, because a sweep that keeps failing means runs
// are piling up in 'running' where nothing will read them.
func (s *RunnerService) reapAbandonedRuns(wsCtx context.Context) {
	swept, err := s.store.FailStuckRuns(wsCtx, stuckRunGrace, abandonedRunReason)
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
func (s *RunnerService) executeJob(wsCtx context.Context, job runner.QueuedJob) {
	spec, known := runner.SpecByName(job.SpecName)
	if !known {
		s.finishJob(wsCtx, job.ID, nil, fmt.Sprintf("agent spec %q is not in the catalog", job.SpecName))
		return
	}
	if job.PassportID == nil {
		s.finishJob(wsCtx, job.ID, nil,
			"no passport bound: mint one (POST /v1/passports) and bind it to the job before the run can act")
		return
	}
	agentIdentity, err := s.identity.AuthenticateAgentByID(wsCtx, *job.PassportID)
	if err != nil {
		s.finishJob(wsCtx, job.ID, nil, "passport resolution failed: "+err.Error())
		return
	}
	// One correlation id per run: every event the run's writes emit
	// groups under it (events.md — "one originating request/agent-run").
	runCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, agentIdentity.Principal()), ids.NewV7())

	runID, created, err := s.store.StartRun(runCtx, spec, job.TriggerRef, *job.PassportID)
	if err != nil {
		s.finishJob(wsCtx, job.ID, nil, err.Error())
		return
	}
	if !created {
		// This occurrence already ran (or is suspended) — the job was a
		// duplicate trigger and idempotency absorbed it.
		s.finishJob(wsCtx, job.ID, nil, "")
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
		Grounding:  grounding,
	})
	s.landOutcome(runCtx, runID, res, err)
	s.finishJob(wsCtx, job.ID, &runID, "")
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
	wsCtx := principal.WithWorkspaceID(ctx, env.WorkspaceID)

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
	suspended, found, err := s.store.ClaimSuspendedByApproval(wsCtx, approvalID)
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
			return s.store.MarkFailed(wsCtx, suspended.RunID, "approval was edited but the decision event carries no edited_change")
		}
		suspended.Pending.Args = payload.EditedChange
	}

	agentIdentity, err := s.identity.AuthenticateAgentByID(wsCtx, suspended.PassportID)
	if err != nil {
		// The passport died while the run was parked (revoked, expired,
		// human deactivated). The run cannot act anymore — close it.
		return s.store.MarkFailed(wsCtx, suspended.RunID, "passport no longer valid at resume: "+err.Error())
	}
	// The resumed leg is the SAME logical run but a new causal moment;
	// it groups its writes under a fresh correlation id.
	runCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, agentIdentity.Principal()), ids.NewV7())
	runCtx = principal.WithAgentRunID(runCtx, suspended.RunID)

	spec, known := runner.SpecByName(suspended.SpecName)
	if !known {
		return s.store.MarkFailed(wsCtx, suspended.RunID, fmt.Sprintf("agent spec %q left the catalog while suspended", suspended.SpecName))
	}

	bounded, cancel := context.WithTimeout(runCtx, RunWallClock)
	defer cancel()
	res, err := s.runner.Resume(bounded, runner.Job{
		Goal:       suspended.Goal,
		TriggerRef: suspended.TriggerRef,
		Budget:     spec.Budget,
	}, runner.Decision{
		Pending:  suspended.Pending,
		Approved: payload.Verdict == "approved",
	})
	s.landOutcome(runCtx, suspended.RunID, res, err)
	return nil
}

func (s *RunnerService) landOutcome(ctx context.Context, runID ids.UUID, res runner.Result, runErr error) {
	if runErr != nil {
		if err := s.store.MarkFailed(ctx, runID, runErr.Error()); err != nil {
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
	hits, err := s.retriever.Search(ctx, retrieval.Query{Text: goal, Limit: 5})
	if err != nil {
		s.log.Warn("runner: seed retrieval failed — running ungrounded", "err", err)
		return nil
	}
	grounding := make([]runner.Grounding, 0, len(hits))
	for _, hit := range hits {
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

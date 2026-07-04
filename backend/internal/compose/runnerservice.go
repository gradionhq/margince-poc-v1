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
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// runWallClock is the §4 wall-clock guarantee (RATIFY default 15 min):
// the third bound alongside steps and output tokens.
const runWallClock = 15 * time.Minute

// claimBatch bounds how many due jobs one tick executes per workspace.
const claimBatch = 4

// RunnerService drives scheduled Surface-B runs. It is the WORKER's
// entry point: Tick seeds + executes due jobs, HandleEvent resumes
// suspended runs when their approval is decided.
type RunnerService struct {
	pool      *pgxpool.Pool
	store     *runner.Store
	runner    *runner.Runner
	identity  *identity.Service
	retriever retrieval.Retriever
	log       *slog.Logger
}

// NewRunnerService assembles the runner over the SAME governed registry
// every other agent surface dispatches through — the two-directions
// invariant is a property of this constructor: there is no other
// registry to hand it.
func NewRunnerService(pool *pgxpool.Pool, brain runner.Brain, retriever retrieval.Retriever, log *slog.Logger) *RunnerService {
	return &RunnerService{
		pool:      pool,
		store:     runner.NewStore(pool),
		runner:    runner.New(NewRegistry(pool), brain),
		identity:  identity.NewService(pool),
		retriever: retriever,
		log:       log,
	}
}

// Tick is one scheduler pass: per live workspace, seed today's due
// catalog occurrences and execute claimed jobs. Tenancy is honest —
// each workspace's work happens under its own GUC-bound context.
func (s *RunnerService) Tick(ctx context.Context) error {
	workspaces, err := s.liveWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
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
	}
	return nil
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

	bounded, cancel := context.WithTimeout(runCtx, runWallClock)
	defer cancel()
	res, err := s.runner.Run(bounded, runner.Job{
		Goal:       spec.Goal,
		TriggerRef: job.TriggerRef,
		Budget:     spec.Budget,
		Grounding:  s.seedGrounding(runCtx, spec.Goal),
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
	approvalID := env.Entity.ID
	wsCtx := principal.WithWorkspaceID(ctx, env.WorkspaceID)

	suspended, found, err := s.store.FindSuspendedByApproval(wsCtx, approvalID)
	if err != nil {
		return err
	}
	if !found {
		return nil // a human-surface approval, not a parked run
	}

	var payload struct {
		Verdict string `json:"verdict"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return fmt.Errorf("runner: approval.decided payload: %w", err)
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

	spec, known := runner.SpecByName(suspended.SpecName)
	if !known {
		return s.store.MarkFailed(wsCtx, suspended.RunID, fmt.Sprintf("agent spec %q left the catalog while suspended", suspended.SpecName))
	}

	bounded, cancel := context.WithTimeout(runCtx, runWallClock)
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

// liveWorkspaces lists tenants to schedule for. The workspace table is
// deliberately outside RLS (it IS the tenant root), so this is the one
// legitimate cross-tenant read the scheduler makes.
func (s *RunnerService) liveWorkspaces(ctx context.Context) ([]ids.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("runner: listing workspaces: %w", err)
	}
	defer rows.Close()
	var out []ids.UUID
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

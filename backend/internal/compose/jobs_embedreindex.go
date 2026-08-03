// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the fleet-wide embed reindex (ADR-0068 design §5.6-swap): a
// dispatcher over every LIVE workspace and a worker that re-embeds one tenant's
// corpus. It registers itself, so jobs.go — which owns the runner's assembly —
// grows one line as this surface does. There is no periodic entry: a reindex is
// a human's confirm at the transport beside this file, never a cadence.
//
// The binding marker is what makes the run one run. The confirm claims it under a
// freshly minted run id, this dispatcher seeds the marker's pending set with the
// fleet and enqueues the children in the same transaction, and each child leaves
// that set when it reaches a terminal outcome. The child that empties it hands
// the marker back. So the marker is held for as long as the RUN has outstanding
// work, not for as long as any one job row is alive — and every MARKER write
// fences on the run id the job carries, so a straggler of a finished run cannot
// move the marker. It still does its own embedding work; only its bookkeeping is
// neutered (search.ReembedClaim.StealAfter states what that costs).

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// embedReindexMaxAttempts is the whole retry budget of both halves of a run.
// Unlike every periodic pass beside it, this kind has NO tick to fall back on:
// nothing re-enqueues a lost workspace until a human confirms a reindex again,
// so sweepWorkspaceMaxAttempts — a number chosen because the dispatcher's tick
// IS the real retry cadence — would quietly cost that tenant its pass. Five
// attempts on River's attempt⁴ backoff spans roughly six minutes, enough to ride
// out an embed provider's blip or a database restart, and a workspace still
// failing after that is a defect a human should see rather than model budget the
// run keeps spending. The marker is released either way, so the operator's
// re-confirm is available immediately rather than an hour later.
const embedReindexMaxAttempts = 5

// addEmbedReindexJobs registers the reindex dispatcher and its workspace worker.
// Both register even with a nil embedder: a row queued on a worker role with no
// embed lane then fails with an actionable message (and leaves the run's pending
// set on its last attempt) instead of sitting queued forever behind a job no one
// can work — the deep-read worker's posture.
func addEmbedReindexJobs(workers *river.Workers, pool *pgxpool.Pool, embedder search.Embedder) {
	store := search.NewStore(pool)
	river.AddWorker(workers, &embedReindexWorker{pool: pool, store: store})
	river.AddWorker(workers, &embedReindexWorkspaceWorker{store: store, embedder: embedder})
}

// EmbedReindexArgs schedules one fleet-wide re-embed. Identity is the embed
// binding in force when the confirm claimed the marker, so a mid-flight config
// change is detectable as drift downstream (search.ErrIdentityDrift) rather than
// the fleet silently re-embedding under whatever it now reports; Run names the
// claim this row is allowed to act on.
type EmbedReindexArgs struct {
	Run      ids.UUID `json:"run_id"`
	Identity string   `json:"identity"`
}

// Kind is the stable job identifier River persists in river_job.
func (EmbedReindexArgs) Kind() string { return "embed_reindex" }

// FleetWide marks this a dispatcher: it enumerates and enqueues,
// and does no tenant work of its own (jobs.FleetWide).
func (EmbedReindexArgs) FleetWide() {}

// embedReindexWorker is the dispatcher. It enumerates the LIVE workspaces only:
// an archived tenant's records are not searched, so re-embedding them spends
// model budget building an index nobody queries.
type embedReindexWorker struct {
	river.WorkerDefaults[EmbedReindexArgs]
	pool  *pgxpool.Pool
	store *search.Store
}

func (w *embedReindexWorker) Work(ctx context.Context, job *river.Job[EmbedReindexArgs]) error {
	err := w.fanOut(ctx, job.Args)
	if errors.Is(err, search.ErrReembeddingSuperseded) {
		// Either a later run holds the marker or this run has already fanned
		// out, so this row's work is nobody's to do: a permanent condition, not
		// one more attempts would clear.
		return river.JobCancel(err)
	}
	if err != nil && job.Attempt >= job.MaxAttempts {
		// A run holds the marker only while it has outstanding work. This one
		// never got any queued and has no attempt left to, so it gives it back
		// rather than leaving every later confirm refused by a run that ended.
		// The release refuses itself if the set is not empty after all.
		return jobs.FaultContext(ctx, errors.Join(err, w.store.ReleaseReembedding(ctx, job.Args.Run)))
	}
	return jobs.FaultContext(ctx, err)
}

// fanOut seeds the run's pending set and enqueues one child per workspace in it,
// as ONE transaction: a fan-out whose insert failed leaves no pending set for
// the retry to double-count, and a seeded set always has the children that will
// empty it.
func (w *embedReindexWorker) fanOut(ctx context.Context, args EmbedReindexArgs) error {
	workspaces, err := enumerateWorkspaces(ctx, w.pool)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		// A run with no tenant to cover has no outstanding work the moment it
		// starts, and a marker held past that refuses every later confirm.
		return w.store.ReleaseReembedding(ctx, args.Run)
	}
	pending := make([]ids.WorkspaceID, 0, len(workspaces))
	for _, ws := range workspaces {
		pending = append(pending, ids.From[ids.WorkspaceKind](ws))
	}
	return w.store.SeedReembeddingFleet(ctx, args.Run, pending, func(tx pgx.Tx) error {
		return dispatchWith(ctx, workspaces, txInsertMany(tx),
			workspaceSweepOpts(aiCaptureQueue, embedReindexMaxAttempts),
			func(ws ids.UUID) river.JobArgs {
				return EmbedReindexWorkspaceArgs{Workspace: ws, Run: args.Run, Identity: args.Identity}
			})
	})
}

// txInsertMany binds the fan-out to the caller's transaction rather than to the
// client's own, so the insert commits with the pending set it is seeding or with
// neither. clientInsertMany's own reasoning applies to resolving the client
// inside the closure.
func txInsertMany(tx pgx.Tx) insertManyFunc {
	return func(ctx context.Context, params []river.InsertManyParams) error {
		_, err := river.ClientFromContext[pgx.Tx](ctx).InsertManyTx(ctx, tx, params)
		return err
	}
}

// EmbedReindexWorkspaceArgs re-embeds one workspace's corpus under the run's
// target identity, and reports that workspace finished with the run it names.
type EmbedReindexWorkspaceArgs struct {
	Workspace ids.UUID `json:"workspace_id"`
	Run       ids.UUID `json:"run_id"`
	Identity  string   `json:"identity"`
}

// Kind is the stable job identifier River persists in river_job.
func (EmbedReindexWorkspaceArgs) Kind() string { return "embed_reindex_workspace" }

// WorkspaceID binds this pass to its tenant (jobs.WorkspaceScoped).
func (a EmbedReindexWorkspaceArgs) WorkspaceID() ids.UUID { return a.Workspace }

// embedReindexWorkspaceWorker re-embeds one workspace and reports that workspace
// finished with the run.
type embedReindexWorkspaceWorker struct {
	river.WorkerDefaults[EmbedReindexWorkspaceArgs]
	store    *search.Store
	embedder search.Embedder
}

// Timeout disables River's 1-minute default: re-embedding a workspace is a
// resumable batch bounded by corpus size, not by a wall-clock budget, so a large
// corpus (or a slow embed provider) must not be cancelled mid-pass and forced to
// burn its attempts re-walking work the skip-compare would anyway make free. It
// still stops on ctx cancellation at shutdown, cancels itself on identity drift,
// and each individual embed is bounded by the model lane's own per-call timeout —
// this only removes the whole-job wall.
//
// It also puts this row outside River's rescuer for good — job_rescuer.go
// ignores a stuck job whose worker declares a negative timeout, at any age —
// which is the wedge search.ReembedClaim.StealAfter exists to answer.
func (w *embedReindexWorkspaceWorker) Timeout(*river.Job[EmbedReindexWorkspaceArgs]) time.Duration {
	return -1
}

func (w *embedReindexWorkspaceWorker) Work(ctx context.Context, job *river.Job[EmbedReindexWorkspaceArgs]) error {
	passErr := w.reembed(ctx, job.Args)
	drifted := errors.Is(passErr, search.ErrIdentityDrift)
	if passErr == nil || drifted || job.Attempt >= job.MaxAttempts {
		// The run will not come back to this workspace, whichever of the three
		// happened, so it leaves the pending set — and releases the marker if it
		// was the last one out. River offers no post-discard hook, which is why
		// the exhausted attempt does this before returning its error.
		if err := w.store.FinishWorkspaceReembedding(ctx, job.Args.Run,
			ids.From[ids.WorkspaceKind](job.Args.Workspace)); err != nil {
			// Retryable even on the drift path, and deliberately: the drift
			// guard short-circuits before any model call, so an attempt spent
			// re-trying this write costs nothing, and it is the only thing that
			// will ever take this workspace out of the run's set.
			return jobs.FaultContext(ctx, errors.Join(passErr, err))
		}
	}
	if drifted {
		// Args naming an identity nothing serves anymore are a permanent defect:
		// what the fleet needs is a new confirm under the current config, not
		// this row's remaining attempts (jobs_overlay_refetch.go's posture).
		return river.JobCancel(passErr)
	}
	return jobs.FaultContext(ctx, passErr)
}

func (w *embedReindexWorkspaceWorker) reembed(ctx context.Context, args EmbedReindexWorkspaceArgs) error {
	// workspaceJobCtx earns its place here as the zero-workspace guard, not as
	// the binding that matters: ReembedWorkspace rebinds the same tenant under
	// the SYSTEM principal, because an index rebuilt through one caller's row
	// scope would silently omit what that caller cannot see.
	wsCtx, err := workspaceJobCtx(ctx, args)
	if err != nil {
		return err
	}
	if w.embedder == nil {
		return errors.New("embed_reindex_workspace: worker has no embed lane — configure --ai-routing (or --ai-fake) on the worker role")
	}
	return w.store.ReembedWorkspace(wsCtx,
		search.ReembedPass{Run: args.Run, Identity: args.Identity},
		ids.From[ids.WorkspaceKind](args.Workspace), w.embedder)
}

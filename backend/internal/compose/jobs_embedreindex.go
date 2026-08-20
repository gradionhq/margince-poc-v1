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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// embedReindexMaxAttempts is the retry budget of a run's DISPATCHER half; the
// workspace half declares the same number in api/jobs.yaml, which is what the
// fan-out reads, and the two are one decision stated in the two places their
// owners live (a confirm's insert here, the contract there).
//
// Unlike every periodic pass beside it, this kind has NO tick to fall back on:
// nothing re-enqueues a lost workspace until a human confirms a reindex again,
// so the three attempts its periodic siblings take — a number chosen because
// the dispatcher's tick IS the real retry cadence — would quietly cost that
// tenant its pass. Five attempts on River's attempt⁴ backoff spans roughly six
// minutes, enough to ride
// out an embed provider's blip or a database restart, and a workspace still
// failing after that is a defect a human should see rather than model budget the
// run keeps spending. Both halves hand the marker back on their way out of a
// terminal outcome, so the operator's re-confirm is normally available at once
// rather than an hour later — normally, not always: that hand-back is a write to
// the same database whose failure is the likeliest reason the attempt failed at
// all, and the exhausted attempt has nothing left to retry it with. The marker
// then stays held until a forced confirm takes it
// (search.ReembedClaim.StealAfter), which is a recovery an hour away rather than
// a wedge.
const embedReindexMaxAttempts = 5

// addEmbedReindexJobs registers the reindex pass. It registers even with a nil
// embedder: a row queued with no embed lane then fails with an actionable
// message instead of sitting queued forever behind a job no one can work — the
// deep-read worker's posture.
func addEmbedReindexJobs(reg *jobRegistry, pool *pgxpool.Pool, embedder search.Embedder) {
	store := search.NewStore(InstallationDB(pool))
	addDeclaredWorker[EmbedReindexArgs](reg, &embedReindexWorker{store: store, embedder: embedder})
}

// EmbedReindexArgs schedules one re-embed of the installation's corpus. Identity
// is the embed binding in force when the confirm claimed the marker, so a
// mid-flight config change is detectable as drift downstream
// (search.ErrIdentityDrift) rather than the corpus silently re-embedding under
// whatever it now reports; Run names the claim this row is allowed to act on.
type EmbedReindexArgs struct {
	Run      ids.UUID `json:"run_id"`
	Identity string   `json:"identity"`
}

// Kind is the stable job identifier River persists in river_job.
func (EmbedReindexArgs) Kind() string { return "embed_reindex" }

// FleetWide marks this as owning no tenant of its own (jobs.FleetWide): since
// ADR-0091 §8 phase D no embeddable entity carries a workspace, so the corpus
// this rebuilds is the installation's.
func (EmbedReindexArgs) FleetWide() {}

// embedReindexWorker rebuilds the installation's corpus under the run's target
// identity and hands the marker back when it is done.
//
// It used to be a dispatcher enqueuing one child per live workspace. Phase D
// took the tenant column off every embeddable entity, so those children all
// walked the SAME rows and all but the first found every one already fresh at
// the run's identity — N-1 jobs whose only effect was to remove themselves from
// a set. One pass rebuilds it, and the set went with them.
type embedReindexWorker struct {
	store    *search.Store
	embedder search.Embedder
}

func (w *embedReindexWorker) Work(ctx context.Context, job *river.Job[EmbedReindexArgs]) error {
	passErr := w.reembed(ctx, job.Args)
	// Both of these are permanent for this row: the identity it names is not the
	// one served anymore, or the installation has no live workspace to bind a
	// pass to (its only one is archived). Neither improves with another attempt,
	// and both must still hand the marker back — a run that held it while
	// retrying itself to exhaustion refuses every later confirm with no job left
	// anywhere to explain why.
	permanent := errors.Is(passErr, search.ErrIdentityDrift) || errors.Is(passErr, search.ErrNoLiveWorkspace)
	if passErr == nil || permanent || job.Attempt >= job.MaxAttempts {
		// The run will not come back, whichever of the three happened, so it
		// gives the marker back. River's discard is observable (its failure
		// event carries the row) but there is no hook that can RETRY, and none
		// that runs inside the row's own transaction, which is why the
		// exhausted attempt does this before returning its error.
		if err := w.store.ReleaseReembedding(ctx, job.Args.Run); err != nil {
			// Retried on every attempt but the last, and on the permanent paths
			// too, deliberately: both of those guards short-circuit before any
			// model call, so an attempt spent re-trying this write costs
			// nothing, and it is the only thing that will ever hand the marker
			// back.
			//
			// On the LAST attempt there is no retry — River discards a row that
			// has run out — so a failure here, most naturally the same outage
			// that failed the pass, leaves the marker held for good. A forced
			// confirm's steal is the way back (search.ReembedClaim.StealAfter).
			return jobs.FaultContext(ctx, errors.Join(passErr, err))
		}
	}
	if permanent {
		// What this needs is a new confirm — under the current config, or once
		// the installation has a live workspace again — not this row's remaining
		// attempts (jobs_overlay_refetch.go's posture).
		return river.JobCancel(passErr)
	}
	return jobs.FaultContext(ctx, passErr)
}

func (w *embedReindexWorker) reembed(ctx context.Context, args EmbedReindexArgs) error {
	if w.embedder == nil {
		return errors.New("embed_reindex: worker has no embed lane — configure --ai-routing (or --ai-fake) on the worker role")
	}
	// The pass runs as the SYSTEM principal inside Reembed: an index
	// rebuilt through one caller's row scope would silently omit what that
	// caller cannot see.
	return w.store.Reembed(ctx, search.ReembedPass{Run: args.Run, Identity: args.Identity}, w.embedder)
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The worker half of reading a transcript: the job the api queued, and the
// principal it runs as.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The reading gets its own bounded pool for the deep-read reason: it is one
// long model call, and a fan-out of them must not evict the short maintenance
// jobs from the default queue.
const (
	transcriptReadQueue      = "transcript_read"
	transcriptReadMaxWorkers = 2
)

// TranscriptProposeArgs is one queued reading.
type TranscriptProposeArgs struct {
	Workspace        ids.UUID `json:"workspace_id"`
	ActivityID       ids.UUID `json:"activity_id"`
	TranscriptReadID ids.UUID `json:"transcript_read_id"`
	// RequestedBy is the human who asked, so what the reading stages carries
	// their name rather than the worker's. Provenance is written once and
	// never re-derived.
	RequestedBy string `json:"requested_by"`
}

// Kind is the string River persists for this job.
func (TranscriptProposeArgs) Kind() string { return "transcript_propose" }

// WorkspaceID declares the one tenant this job's work belongs to, and is what
// binds the worker's context — a worker that picked its own could claim one
// workspace and work in another.
func (a TranscriptProposeArgs) WorkspaceID() ids.UUID { return a.Workspace }

// transcriptProposeInsertOpts routes the job to its own queue and deduplicates
// by args: the read id is unique per reading, so a re-submitted enqueue of the
// SAME reading collapses while a fresh reading always queues.
func transcriptProposeInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:      transcriptReadQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

// transcriptProposeWorker runs one queued reading. It is always registered on
// the worker role — with no model lane it FAILS the reading with a reason the
// rep can see, rather than leaving it queued forever behind a worker that
// cannot read.
type transcriptProposeWorker struct {
	activities *activities.Store
	proposer   *TranscriptProposer
	log        *slog.Logger
}

func (w *transcriptProposeWorker) Work(ctx context.Context, job *river.Job[TranscriptProposeArgs]) error {
	args := job.Args
	wsCtx, err := workspaceJobCtx(ctx, args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	wsCtx = withClaimedRequester(wsCtx, args.RequestedBy, args.TranscriptReadID)
	if w.proposer == nil {
		return jobs.FaultContext(ctx, w.declineUnread(wsCtx, args))
	}
	err = w.proposer.Read(wsCtx, w.activities, args.TranscriptReadID, ids.From[ids.ActivityKind](args.ActivityID))
	if errors.Is(err, apperrors.ErrConflict) {
		// The CAS miss: the reading is no longer queued — a rival replica took
		// it, or it was already closed. Nothing to do and nothing wrong.
		w.log.InfoContext(wsCtx, "transcript reading already claimed",
			"transcript_read_id", args.TranscriptReadID)
		return nil
	}
	return jobs.FaultContext(ctx, err)
}

// declineUnread closes a reading no configured lane can perform. Failing it
// visibly is the point: a queued row nobody will ever pick up is
// indistinguishable, to the rep watching it, from one still being worked.
func (w *transcriptProposeWorker) declineUnread(ctx context.Context, args TranscriptProposeArgs) error {
	w.log.WarnContext(ctx, "transcript reading declined: no model lane configured",
		"transcript_read_id", args.TranscriptReadID)
	if _, err := w.activities.BeginTranscriptRead(ctx, args.TranscriptReadID); err != nil {
		if errors.Is(err, apperrors.ErrConflict) {
			return nil
		}
		return err
	}
	return w.activities.FinishTranscriptRead(ctx, args.TranscriptReadID,
		activities.TranscriptReadOutcome{
			Status: activities.TranscriptReadFailed,
			Detail: "this installation has no AI model configured for reading transcripts",
		})
}

// newTranscriptProposeWorker assembles the worker-role reading. brain may be
// nil — a picked-up reading then finishes failed with an actionable message
// rather than sitting queued behind a worker that cannot read it.
func newTranscriptProposeWorker(pool *pgxpool.Pool, brain completer, log *slog.Logger) *transcriptProposeWorker {
	worker := &transcriptProposeWorker{
		activities: activities.NewStore(InstallationDB(pool)),
		log:        log,
	}
	if brain != nil {
		worker.proposer = NewTranscriptProposer(
			pool, brain, approvalsServiceWithEffects(pool), time.Now, log)
	}
	return worker
}

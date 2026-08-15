// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The worker half of reading a document: the job the api queued, and the
// principal it runs as.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// documentExtractActor is the agent every write of a reading is attributed to.
// Its own name, not the transcript reader's: an inbox that told a rep the
// transcript reader had read their invoice would be naming something that never
// ran. Provenance is written once and never re-derived, which is exactly why it
// cannot be borrowed from a neighbour.
const documentExtractActor = "agent:document-extractor"

// DocumentExtractArgs is one queued reading.
type DocumentExtractArgs struct {
	Workspace        ids.UUID `json:"workspace_id"`
	AttachmentID     ids.UUID `json:"attachment_id"`
	ExtractionReadID ids.UUID `json:"attachment_extraction_id"`
	// RequestedBy is the human who asked, so what the reading records carries
	// their name rather than the worker's.
	RequestedBy string `json:"requested_by"`
}

// Kind is the string River persists for this job.
func (DocumentExtractArgs) Kind() string { return "document_extract" }

// WorkspaceID declares the one tenant this job's work belongs to, and is what
// binds the worker's context — a worker that picked its own could claim one
// workspace and work in another.
func (a DocumentExtractArgs) WorkspaceID() ids.UUID { return a.Workspace }

// documentExtractInsertOpts routes the job to the readings' own queue and
// deduplicates by args: the reading id is unique per reading, so a re-submitted
// enqueue of the SAME reading collapses while a fresh reading always queues.
func documentExtractInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:      transcriptReadQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	}
}

// withDocumentReader stamps the principal every write of this reading is
// attributed to: the reader as the acting agent, the human who asked as the
// owner of what it produces.
func withDocumentReader(ctx context.Context, requestedBy string, readID ids.UUID) context.Context {
	requester := requestedByUserID(requestedBy)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:       principal.PrincipalSystem,
		ID:         documentExtractActor,
		UserID:     requester,
		OnBehalfOf: requester,
	})
	return principal.WithCorrelationID(ctx, readID)
}

// documentExtractWorker runs one queued reading. It is always registered on the
// worker role — with no model lane it FAILS the reading with a reason the rep
// can see, rather than leaving it queued forever behind a worker that cannot
// read.
type documentExtractWorker struct {
	// activities is the run record's movements, named as the seam the engine
	// already takes rather than the concrete store: the worker's whole
	// relationship with the module is claim, open, finish.
	activities documentReadStore
	extractor  *DocumentExtractor
	log        *slog.Logger
}

func (w *documentExtractWorker) Work(ctx context.Context, job *river.Job[DocumentExtractArgs]) error {
	args := job.Args
	wsCtx, err := workspaceJobCtx(ctx, args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	wsCtx = withDocumentReader(wsCtx, args.RequestedBy, args.ExtractionReadID)
	if w.extractor == nil {
		return jobs.FaultContext(ctx, w.declineUnread(wsCtx, args))
	}
	err = w.extractor.Read(wsCtx, w.activities, args.ExtractionReadID, args.AttachmentID)
	if errors.Is(err, apperrors.ErrConflict) {
		// The CAS miss: the reading is no longer queued — a rival replica took
		// it, or it was already closed. Nothing to do and nothing wrong.
		w.log.InfoContext(wsCtx, "document reading already claimed",
			"attachment_extraction_id", args.ExtractionReadID)
		return nil
	}
	return jobs.FaultContext(ctx, err)
}

// declineUnread closes a reading no configured lane can perform. Failing it
// visibly is the point: a queued row nobody will ever pick up is
// indistinguishable, to the rep watching it, from one still being worked.
func (w *documentExtractWorker) declineUnread(ctx context.Context, args DocumentExtractArgs) error {
	w.log.WarnContext(ctx, "document reading declined: no model lane configured",
		"attachment_extraction_id", args.ExtractionReadID)
	if _, err := w.activities.BeginExtractionRead(ctx, args.ExtractionReadID, activities.ExtractionReadLease); err != nil {
		if errors.Is(err, apperrors.ErrConflict) {
			return nil
		}
		return err
	}
	return w.activities.FinishExtractionRead(ctx, args.ExtractionReadID,
		activities.ExtractionReadOutcome{
			Status: activities.ExtractionReadFailed,
			Detail: "this installation has no AI model configured for reading documents",
		})
}

// newDocumentExtractWorker assembles the worker-role reading. brain may be nil
// — a picked-up reading then finishes failed with an actionable message rather
// than sitting queued behind a worker that cannot read it.
func newDocumentExtractWorker(pool *pgxpool.Pool, brain documentCompleter, log *slog.Logger) *documentExtractWorker {
	worker := &documentExtractWorker{
		activities: activities.NewStore(InstallationDB(pool)),
		log:        log,
	}
	if brain != nil {
		worker.extractor = NewDocumentExtractor(pool, brain, log)
	}
	return worker
}

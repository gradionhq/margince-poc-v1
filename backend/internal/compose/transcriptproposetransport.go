// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP half of reading a transcript: queue the reading and serve the poll.
//
// The api role never calls a model in-request — it inserts the job and answers
// 202 — so this file holds no reading logic at all. Without the option that
// wires a job runner, all three operations answer their explicit 501 rather
// than pretending to queue work nothing will pick up.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/riverqueue/river"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// transcriptReadEnqueuer is the slice of *jobs.Runner the start handler needs;
// tests fake it to count inserts.
type transcriptReadEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error
}

// transcriptReadEngine queues readings and answers polls about them.
type transcriptReadEngine struct {
	activities *activities.Store
	enqueue    transcriptReadEnqueuer
}

// start queues the reading and says how the request landed.
func (e *transcriptReadEngine) start(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	activityID := ids.From[ids.ActivityKind](ids.UUID(id))
	read, joined, err := e.activities.StartTranscriptReadQueued(r.Context(), activityID, requestedBy(r.Context()),
		func(ctx context.Context, tx pgx.Tx, read activities.TranscriptRead) error {
			return e.enqueue.EnqueueTx(ctx, tx, TranscriptProposeArgs{
				Workspace:        storekit.MustWorkspace(ctx),
				ActivityID:       activityID.UUID,
				TranscriptReadID: read.ID,
				RequestedBy:      read.RequestedBy,
			}, transcriptProposeInsertOpts())
		})
	if err != nil {
		// The row-scope 404, the not-a-transcript 422 and the blank-transcript
		// 422 all ride the sentinel / FieldFault mapping.
		httperr.Write(w, r, err)
		return
	}
	status := crmcontracts.TranscriptReadStartedStatusQueued
	if joined && read.Status == activities.TranscriptReadRunning {
		status = crmcontracts.TranscriptReadStartedStatusRunning
	}
	httperr.WriteJSON(w, http.StatusAccepted, crmcontracts.TranscriptReadStarted{
		ReadId: openapi_types.UUID(read.ID),
		Status: status,
	})
}

// report answers the SPA's poll with the reading as it stands.
func (e *transcriptReadEngine) report(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID) {
	read, err := e.activities.GetTranscriptRead(r.Context(),
		ids.From[ids.ActivityKind](ids.UUID(id)), ids.UUID(readID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, transcriptReadReport(read))
}

// latestReport answers with the newest reading of this transcript, for a page
// that holds no read id — which is every load after the one that started it.
func (e *transcriptReadEngine) latestReport(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	read, err := e.activities.LatestTranscriptRead(r.Context(), ids.From[ids.ActivityKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, transcriptReadReport(read))
}

// transcriptReadReport maps the run record onto the contract report. The
// proposal list is always concrete (empty, never null): a reading that found
// nothing is an explicit account of having found nothing.
func transcriptReadReport(read activities.TranscriptRead) crmcontracts.TranscriptReadReport {
	report := crmcontracts.TranscriptReadReport{
		ReadId:       openapi_types.UUID(read.ID),
		ActivityId:   openapi_types.UUID(read.ActivityID.UUID),
		Status:       crmcontracts.TranscriptReadReportStatus(read.Status),
		LineCount:    read.LineCount,
		ProposalIds:  make([]openapi_types.UUID, 0, len(read.ProposalIDs)),
		StatusDetail: read.StatusDetail,
		StartedAt:    read.StartedAt,
		FinishedAt:   read.FinishedAt,
		CreatedAt:    read.CreatedAt,
	}
	for _, id := range read.ProposalIDs {
		report.ProposalIds = append(report.ProposalIds, openapi_types.UUID(id))
	}
	return report
}

// WithTranscriptRead enables the transcript-reading transport on the api role:
// start queues the job through the insert-only runner (the api never reads a
// transcript in-request — the worker role does), report serves the poll.
// Without it all three operations stay their explicit 501.
func WithTranscriptRead(inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		engine := &transcriptReadEngine{
			activities: activities.NewStore(InstallationDB(pool)),
			enqueue:    inserter,
		}
		s.transcriptReadHandlers = transcriptReadHandlers{
			start: engine.start, report: engine.report, latest: engine.latestReport,
		}
	}
}

// transcriptReadHandlers shadows the generated stubs. Every field is nil until
// WithTranscriptRead wires the engine.
type transcriptReadHandlers struct {
	start  func(w http.ResponseWriter, r *http.Request, id openapi_types.UUID)
	report func(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID)
	latest func(w http.ResponseWriter, r *http.Request, id openapi_types.UUID)
}

func (h transcriptReadHandlers) ReadTranscriptForNextSteps(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if h.start == nil {
		httperr.NotImplemented(w, r, "readTranscriptForNextSteps (no job runner configured)")
		return
	}
	h.start(w, r, id)
}

func (h transcriptReadHandlers) GetTranscriptRead(w http.ResponseWriter, r *http.Request, id, readID openapi_types.UUID) {
	if h.report == nil {
		httperr.NotImplemented(w, r, "getTranscriptRead (no job runner configured)")
		return
	}
	h.report(w, r, id, readID)
}

func (h transcriptReadHandlers) GetLatestTranscriptRead(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if h.latest == nil {
		httperr.NotImplemented(w, r, "getLatestTranscriptRead (no job runner configured)")
		return
	}
	h.latest(w, r, id)
}

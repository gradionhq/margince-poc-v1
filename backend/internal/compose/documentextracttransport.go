// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The HTTP half of reading a document: queue the reading and answer 202.
//
// The api role never calls a model in-request — it inserts the job and answers
// 202 — so this file holds no reading logic at all. The POLL is not here: it is
// a plain read of the run record and lives with the row, in the activities
// module (getAttachmentExtraction). Without the option that wires a job runner,
// the start answers its explicit 501 rather than pretending to queue work
// nothing will pick up.

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// documentReadEngine queues readings.
type documentReadEngine struct {
	activities *activities.Store
	enqueue    transcriptReadEnqueuer
}

// start queues the reading and says how the request landed.
func (e *documentReadEngine) start(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	attachmentID := ids.UUID(id)
	read, joined, err := e.activities.StartExtractionReadQueued(r.Context(), attachmentID, requestedBy(r.Context()),
		func(ctx context.Context, tx pgx.Tx, read activities.ExtractionRead) error {
			return e.enqueue.EnqueueTx(ctx, tx, DocumentExtractArgs{
				Workspace:        storekit.MustWorkspace(ctx),
				AttachmentID:     attachmentID,
				ExtractionReadID: read.ID,
				RequestedBy:      read.RequestedBy,
			}, documentExtractInsertOpts())
		})
	if err != nil {
		// The row-scope 404 and the missing-authority 403 both ride the
		// sentinel / typed-fault mapping.
		httperr.Write(w, r, err)
		return
	}
	status := crmcontracts.AttachmentReadStartedStatusQueued
	if joined && read.Status == activities.ExtractionReadRunning {
		status = crmcontracts.AttachmentReadStartedStatusRunning
	}
	httperr.WriteJSON(w, http.StatusAccepted, crmcontracts.AttachmentReadStarted{
		Id:     openapi_types.UUID(read.ID),
		Status: status,
		Joined: joined,
	})
}

// WithDocumentRead enables the document-reading transport on the api role:
// start queues the job through the insert-only runner (the api never reads a
// document in-request — the worker role does). Without it the start stays its
// explicit 501.
func WithDocumentRead(inserter *jobs.Runner) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		engine := &documentReadEngine{
			activities: activities.NewStore(InstallationDB(pool)),
			enqueue:    inserter,
		}
		s.documentReadHandlers = documentReadHandlers{start: engine.start}
	}
}

// documentReadHandlers shadows the generated stub. start is nil until
// WithDocumentRead wires the engine.
type documentReadHandlers struct {
	start func(w http.ResponseWriter, r *http.Request, id openapi_types.UUID)
}

func (h documentReadHandlers) ReadAttachmentForFields(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	if h.start == nil {
		httperr.NotImplemented(w, r, "readAttachmentForFields (no job runner configured)")
		return
	}
	h.start(w, r, id)
}

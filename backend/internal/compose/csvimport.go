// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The migrate-in surface (IEM-WIRE-3/4/5/6 and the IEM-WIRE-8 upload):
// upload a file, read what its columns hold, map them, dry-run, approve.
//
// It lives in compose rather than in modules/migration for the same reason the
// flip does: driving the engine means constructing a Writers over people's
// stores, and a module may never import a sibling. The engine, the run record
// and the identity map are all the module's; this file is the door.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// maxImportUploadBytes is CAP-BODY, the 10 MB import-only request cap the
	// rate-limit chapter owns. Enforced with a MaxBytesReader so an oversized
	// file is a distinct refusal, never a truncated read that imports half a
	// customer's estate and reports success.
	maxImportUploadBytes = 10 << 20
	// importBlobKind namespaces uploaded sources inside the workspace's blob
	// prefix, beside attachments and logos.
	importBlobKind = "import"
	// importSourceProvenance is the `source` every run row carries: this
	// surface, not the connector, which lives in its own column.
	importSourceProvenance = "import_api"
	// importPredictPage bounds one prediction read; the same page size the
	// engine walks with, so the two make the same number of round trips.
	importPredictPage = 200
)

type importHandlers struct {
	db    *database.DB
	blobs blobstore.Store
}

// UploadImportSource stores a file and describes it (IEM-WIRE-8). Nothing is
// imported, validated against the estate, or written here — the response is
// the evidence a human makes the mapping on.
func (h importHandlers) UploadImportSource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := auth.RequireHuman(ctx); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if err := auth.Require(ctx, migration.ImportRunObject, principal.ActionCreate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if h.blobs == nil {
		httperr.Write(w, r, fmt.Errorf("this role stores no objects, so it cannot accept an import: %w", apperrors.ErrConflict))
		return
	}

	object, body, err := readImportUpload(w, r)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	profile, err := migration.ProfileCSV(bytes.NewReader(body), migration.ProfileRowLimit)
	if err != nil {
		httperr.Write(w, r, importProblem(err))
		return
	}
	targets, err := importTargets(object)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		httperr.Write(w, r, fmt.Errorf("no workspace is bound to this request: %w", apperrors.ErrPermissionDenied))
		return
	}
	key := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](ws), importBlobKind, ids.NewV7().String())
	if err := h.blobs.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/csv"); err != nil {
		httperr.Write(w, r, fmt.Errorf("storing the import source: %w", err))
		return
	}

	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ImportSourceProfile{
		SourceRef:        key,
		Object:           crmcontracts.ImportObject(object),
		Columns:          toContractColumns(profile),
		RowsProfiled:     profile.RowsProfiled,
		SuggestedMapping: migration.SuggestMapping(profile, targets),
		Targets:          targets,
	})
}

// CreateImportRun validates a mapped file against the estate and writes
// nothing (IEM-WIRE-3, AC-M5). The run arrives at awaiting_approval carrying
// the report a human reads.
func (h importHandlers) CreateImportRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := auth.RequireHuman(ctx); err != nil {
		httperr.Write(w, r, err)
		return
	}
	// The grant is taken BEFORE the body is read: an ungranted caller must not
	// be able to tell a rejected mapping from an accepted one, which is what a
	// 422 arriving ahead of the 403 would tell them.
	if err := auth.Require(ctx, migration.ImportRunObject, principal.ActionCreate); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if h.blobs == nil {
		httperr.Write(w, r, errNoObjectStore)
		return
	}
	var req crmcontracts.CreateImportRunRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	object := string(req.Object)
	if err := h.ownsSource(ctx, req.SourceRef); err != nil {
		httperr.Write(w, r, err)
		return
	}
	mapping, err := mappingFrom(object, req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	runs := migration.NewRunStore(h.db)
	run, err := runs.CreateStagedRun(ctx, migration.CreateStagedRunInput{
		Connector: string(req.Connector),
		SourceRef: req.SourceRef,
		Source:    importSourceProvenance,
		Mapping:   mapping,
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	source := migration.NewCSVSource(h.blobs, req.SourceRef, object, mapping.Fields, mapping.SourceKey)
	writers := newCSVWriters(h.db, run.ID, object)
	report, err := migration.NewEngine(runs, writers).DryRun(ctx, source)
	if err != nil {
		// The run row already exists. Left alone it would sit in `validating`
		// forever with nothing able to move it, so the failure is recorded on
		// the run the caller was just handed rather than only in the response.
		failValidation(ctx, runs, run.ID, err)
		httperr.Write(w, r, importProblem(err))
		return
	}
	report, err = refinePrediction(ctx, source, writers, object, report)
	if err != nil {
		failValidation(ctx, runs, run.ID, err)
		httperr.Write(w, r, importProblem(err))
		return
	}
	report = withSkippedLines(report, object, source.Skipped())
	if err := runs.AwaitApproval(ctx, run.ID, report); err != nil {
		httperr.Write(w, r, err)
		return
	}

	staged, err := runs.GetStaged(ctx, run.ID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, toContractImportRun(staged))
}

// GetImportRun reports the lifecycle (IEM-WIRE-6): a failed run carries its
// checkpoint, which is what makes it resumable rather than a dead end.
func (h importHandlers) GetImportRun(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	run, err := h.staged(r, id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractImportRun(run))
}

// GetImportRunReport reads what the run will do, or did (IEM-WIRE-4) — one
// shape for both, so a human comparing them compares like with like.
func (h importHandlers) GetImportRunReport(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	run, err := h.staged(r, id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if run.Report == nil {
		httperr.Write(w, r, fmt.Errorf("import run %s has not been validated yet: %w", run.ID, apperrors.ErrConflict))
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractImportReport(run))
}

// ApproveImportRun commits a validated run (IEM-WIRE-5).
func (h importHandlers) ApproveImportRun(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	ctx := r.Context()
	run, err := h.staged(r, id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if run.Mapping == nil {
		httperr.Write(w, r, fmt.Errorf("import run %s carries no mapping, so it is not an approvable import: %w", run.ID, apperrors.ErrConflict))
		return
	}
	if h.blobs == nil {
		httperr.Write(w, r, errNoObjectStore)
		return
	}

	runs := migration.NewRunStore(h.db)
	approved, err := h.startOrResume(ctx, runs, run)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	object := run.Mapping.Object
	source := migration.NewCSVSource(h.blobs, run.SourceRef, object, run.Mapping.Fields, run.Mapping.SourceKey)
	writers := newCSVWriters(h.db, approved.ID, object)
	// The commit outlives the request deliberately. Cancelling it when the
	// browser goes away would leave the run `running` with rows already
	// committed and nothing able to record the failure — a state neither
	// approve (not awaiting) nor resume (not failed) can move.
	commitCtx := context.WithoutCancel(ctx)
	if _, err := migration.NewEngine(runs, writers).Run(commitCtx, approved.ID, source); err != nil {
		// The engine has already recorded the failure and its checkpoint on the
		// run; the caller is told which run to resume rather than being handed
		// a bare 500.
		httperr.Write(w, r, importProblem(err))
		return
	}

	final, err := runs.GetStaged(commitCtx, approved.ID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, toContractImportRun(final))
}

// startOrResume begins an approved run, or continues one that failed part-way.
//
// A failed run is resumable by contract (IEM-WIRE-6), and approve is the only
// door onto the engine, so pressing it again on a failure continues from the
// checkpoint rather than refusing. Every other state falls through to Approve,
// which refuses anything but awaiting_approval.
func (h importHandlers) startOrResume(ctx context.Context, runs *migration.RunStore, run migration.Run) (migration.Run, error) {
	if run.Status == migration.StatusFailed {
		return runs.ResumeApproved(ctx, run.ID)
	}
	return runs.Approve(ctx, run.ID)
}

// staged is the read every id-bearing operation starts from: human-only, then
// the store's own gate, which answers not-found for a run outside the caller's
// scope rather than disclosing that it exists.
func (h importHandlers) staged(r *http.Request, id openapi_types.UUID) (migration.Run, error) {
	ctx := r.Context()
	if err := auth.RequireHuman(ctx); err != nil {
		return migration.Run{}, err
	}
	return migration.NewRunStore(h.db).GetStaged(ctx, migration.RunID(id))
}

// errNoObjectStore refuses an import on a process role that stores no objects.
// A conflict rather than a 500: the installation is configured this way, and a
// nil store reached later would be a panic inside a run that already exists.
var errNoObjectStore = fmt.Errorf("this role stores no objects, so it cannot run an import: %w", apperrors.ErrConflict)

// ownsSource refuses a source reference minted for another installation.
//
// The reference is a blobstore key and the blobstore treats keys as opaque
// bytes — it enforces no tenant boundary of its own, by design (the key IS the
// boundary, see blobstore.WorkspaceKey). So the only thing standing between a
// caller and another workspace's uploaded file is this check: without it, a
// reference obtained anywhere could be dry-run and approved here, importing
// somebody else's estate into this one.
func (h importHandlers) ownsSource(ctx context.Context, sourceRef string) error {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return fmt.Errorf("no workspace is bound to this request: %w", apperrors.ErrPermissionDenied)
	}
	if sourceRef != blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](ws), importBlobKind, path.Base(sourceRef)) {
		// Not-found, not forbidden: a caller may not learn whether a reference
		// they were never given exists.
		return fmt.Errorf("import source %q: %w", sourceRef, apperrors.ErrNotFound)
	}
	return nil
}

// failValidation records a dry run that could not finish, so the run it was
// opened for does not sit in `validating` with nothing able to move it.
func failValidation(ctx context.Context, runs *migration.RunStore, id migration.RunID, cause error) {
	if err := runs.FailValidation(ctx, id, cause); err != nil {
		slog.ErrorContext(ctx, "recording a failed import validation", "run", id, "err", err)
	}
}

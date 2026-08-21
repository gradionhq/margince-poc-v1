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
	"errors"
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
	// importSpillBytes is how much of the upload is held in memory before the
	// rest goes to a temp file. The request cap itself is CAP-BODY, owned by the
	// rate-limit chapter and set by the deployment (OPS-CFG-12); enforced with a
	// MaxBytesReader so an oversized file is a distinct refusal, never a
	// truncated read that imports half a customer's estate and reports success.
	importSpillBytes = 1 << 20
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
	// uploadLimit is the deployment's ceiling for this route (OPS-CFG-12).
	// Zero refuses every upload, which is the honest reading of "nobody has
	// said" for a bound.
	uploadLimit int64
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

	if h.uploadLimit <= 0 {
		// Our fault, not the caller's — the same guard the other two upload
		// routes carry, and for the same reason: a zero bound refuses a
		// perfectly good file and tells its sender it "exceeds the 0 MB limit",
		// which sends them off to shrink something that was never too large.
		httperr.Write(w, r, errUploadLimitUnset)
		return
	}

	object, body, err := readImportUpload(w, r, h.uploadLimit)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	out, err := h.profileAndStore(ctx, object, body)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, out)
}

// profileAndStore is everything the upload does once it HAS the bytes: read the
// header, propose a mapping, put the file where a run can find it.
//
// It is separate from the transport because two doors arrive at this point with
// bytes in hand. The REST one reads a multipart file. The tool surface takes
// CSV as text, since an assistant holding a spreadsheet's contents cannot
// perform a file upload — and asking it to invent a multipart body would be a
// worse door than none.
func (h importHandlers) profileAndStore(
	ctx context.Context, object string, body []byte,
) (crmcontracts.ImportSourceProfile, error) {
	profile, err := migration.ProfileCSV(bytes.NewReader(body), migration.ProfileRowLimit)
	if err != nil {
		return crmcontracts.ImportSourceProfile{}, importProblem(err)
	}
	targets, err := importTargets(object)
	if err != nil {
		return crmcontracts.ImportSourceProfile{}, err
	}
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return crmcontracts.ImportSourceProfile{}, fmt.Errorf(
			"no workspace is bound to this request: %w", apperrors.ErrPermissionDenied)
	}
	key := blobstore.WorkspaceKey(ids.From[ids.WorkspaceKind](ws), importBlobKind, ids.NewV7().String())
	if err := h.blobs.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/csv"); err != nil {
		return crmcontracts.ImportSourceProfile{}, fmt.Errorf("storing the import source: %w", err)
	}
	return crmcontracts.ImportSourceProfile{
		SourceRef:        key,
		Object:           crmcontracts.ImportObject(object),
		Columns:          toContractColumns(profile),
		RowsProfiled:     profile.RowsProfiled,
		SuggestedMapping: migration.SuggestMapping(profile, targets),
		Targets:          targets,
	}, nil
}

// CreateImportRun validates a mapped file against the estate and writes
// nothing (IEM-WIRE-3, AC-M5). The run arrives at awaiting_approval carrying
// the report a human reads.
//
// Open to an agent: this call writes NO domain rows, by construction (AC-M5),
// so the worst an ungranted-but-authenticated caller could do is produce a
// report. What commits is approveImportRun, which stays confirm-first on the
// tool surface — a person sees the report and says yes.
func (h importHandlers) CreateImportRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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
	staged, err := h.stageRun(ctx, req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, staged)
}

// stageRun is everything createImportRun does once it HAS the request: check
// the source belongs to this workspace, validate the mapping, dry-run it, and
// park the result for a human to approve.
//
// It is separate from the transport because the tool surface arrives here too,
// and the dry run is the part that must be identical on both doors — G stays
// product law, so nothing may commit an import that has not produced a report
// through exactly this path.
func (h importHandlers) stageRun(
	ctx context.Context, req crmcontracts.CreateImportRunRequest,
) (crmcontracts.ImportRun, error) {
	object := string(req.Object)
	if err := h.ownsSource(ctx, req.SourceRef); err != nil {
		return crmcontracts.ImportRun{}, err
	}
	mapping, err := mappingFrom(object, req)
	if err != nil {
		return crmcontracts.ImportRun{}, err
	}

	runs := migration.NewRunStore(h.db)
	run, err := runs.CreateStagedRun(ctx, migration.CreateStagedRunInput{
		Connector: string(req.Connector),
		SourceRef: req.SourceRef,
		Source:    importSourceProvenance,
		Mapping:   mapping,
	})
	if err != nil {
		return crmcontracts.ImportRun{}, err
	}

	source := migration.NewCSVSource(h.blobs, req.SourceRef, object, mapping.Fields, mapping.SourceKey)
	writers := newCSVWriters(h.db, run.ID, object)
	report, err := migration.NewEngine(runs, writers).DryRun(ctx, source)
	if err != nil {
		// The run row already exists. Left alone it would sit in `validating`
		// forever with nothing able to move it, so the failure is recorded on
		// the run the caller was just handed rather than only in the response.
		failValidation(ctx, runs, run.ID, err)
		return crmcontracts.ImportRun{}, importProblem(err)
	}
	report, err = refinePrediction(ctx, source, writers, object, report)
	if err != nil {
		failValidation(ctx, runs, run.ID, err)
		return crmcontracts.ImportRun{}, importProblem(err)
	}
	report = withSkippedLines(report, object, source.Skipped())
	if err := runs.AwaitApproval(ctx, run.ID, report); err != nil {
		// The same orphan the two branches above avoid: a run that cannot be
		// parked for a human is a run in `validating` that neither approve nor
		// resume will ever move.
		failValidation(ctx, runs, run.ID, err)
		return crmcontracts.ImportRun{}, err
	}

	staged, err := runs.GetStaged(ctx, run.ID)
	if err != nil {
		return crmcontracts.ImportRun{}, err
	}
	return toContractImportRun(staged), nil
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
	out, err := h.commitRun(r.Context(), id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, out)
}

// commitRun is the approval and the write, without the transport.
//
// Shared with the tool surface for the reason the whole import seam is shared:
// this is the call that writes a customer's estate, and two paths that both
// "commit an approved run" would be two chances to disagree about what an
// approved run is.
func (h importHandlers) commitRun(
	ctx context.Context, id openapi_types.UUID,
) (crmcontracts.ImportRun, error) {
	run, err := h.stagedFor(ctx, id)
	if err != nil {
		return crmcontracts.ImportRun{}, err
	}
	if run.Mapping == nil {
		return crmcontracts.ImportRun{}, fmt.Errorf(
			"import run %s carries no mapping, so it is not an approvable import: %w",
			run.ID, apperrors.ErrConflict)
	}
	if h.blobs == nil {
		return crmcontracts.ImportRun{}, errNoObjectStore
	}

	runs := migration.NewRunStore(h.db)
	approved, err := h.startOrResume(ctx, runs, run)
	if err != nil {
		return crmcontracts.ImportRun{}, err
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
		return crmcontracts.ImportRun{}, importProblem(err)
	}

	final, err := runs.GetStaged(commitCtx, approved.ID)
	if err != nil {
		return crmcontracts.ImportRun{}, err
	}
	return toContractImportRun(final), nil
}

// UndoImportRun reverses a completed csv run (IEM-WIRE-9). Mapping.Object is
// read from the run rather than the request: the run itself is the only
// authority for what its own rows are.
func (h importHandlers) UndoImportRun(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	ctx := r.Context()
	run, err := h.staged(r, id)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if run.Mapping == nil {
		httperr.Write(w, r, fmt.Errorf("import run %s carries no mapping, so it created nothing to undo: %w", run.ID, apperrors.ErrConflict))
		return
	}

	runs := migration.NewRunStore(h.db)
	writers := newCSVWriters(h.db, run.ID, run.Mapping.Object)
	// The reversal outlives the request deliberately, the same reason the
	// commit does (ApproveImportRun): cancelling it when the browser goes
	// away must not leave the run `undoing` with rows already reversed and
	// nothing able to record how far it got.
	undoCtx := context.WithoutCancel(ctx)
	if _, err := runs.Undo(undoCtx, run.ID, writers); err != nil {
		httperr.Write(w, r, err)
		return
	}

	final, err := runs.GetStaged(undoCtx, run.ID)
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

// staged is the read every id-bearing operation starts from: the store's own
// gate answers not-found for a run outside the caller's scope rather than
// disclosing that it exists.
func (h importHandlers) staged(r *http.Request, id openapi_types.UUID) (migration.Run, error) {
	return h.stagedFor(r.Context(), id)
}

// stagedFor is staged without a request, so the tool surface reaches the same
// read the REST one does.
//
// THE HUMAN-ONLY CHECK THAT SAT HERE IS GONE, not moved. Reading an import run
// and its report is open to an agent now — a migration an assistant is helping
// with is unreadable to it otherwise, which was the whole gap. What bounds it
// is the object grant, auth.Require(migration.ImportRunObject, …), taken by
// every caller of this and unchanged: an agent with no import grant still gets
// nothing, and the run store still answers not-found for another workspace's
// run.
//
// Two operations keep RequireHuman, each for its own reason. uploadImportSource
// is multipart and has no agent-shaped door at all. undoImportRun reverses a
// committed estate-wide write, and reversing is not something a caller should
// reach without a person present.
func (h importHandlers) stagedFor(ctx context.Context, id openapi_types.UUID) (migration.Run, error) {
	run, err := migration.NewRunStore(h.db).GetStaged(ctx, migration.RunID(id))
	if err != nil {
		return migration.Run{}, err
	}
	// The flip writes its own runs to the same table, and they carry no
	// mapping — no object, no columns, nothing this surface can describe. They
	// answer not-found here rather than being served as an import with an
	// empty `object` the contract's own enum forbids.
	if run.Connector != migration.ConnectorCSV {
		return migration.Run{}, fmt.Errorf("import run %s: %w", id, apperrors.ErrNotFound)
	}
	return run, nil
}

// errUploadLimitUnset reports that this composition never told the import
// handler its ceiling. A wiring fault, not a request fault, so it answers 500
// rather than refusing the caller's file for a size nobody set — the same guard
// the attachment and LinkedIn routes carry.
var errUploadLimitUnset = errors.New("compose: no upload ceiling configured for the import route")

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

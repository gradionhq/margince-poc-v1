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
	"io"
	"mime/multipart"
	"net/http"
	"strings"

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
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
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
)

type importHandlers struct {
	db      *database.DB
	blobs   blobstore.Store
	catalog fieldcatalog.Reader
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
	targets, err := importTargets(ctx, h.catalog, object)
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
	var req crmcontracts.CreateImportRunRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	object := string(req.Object)
	mapping, err := h.mappingFrom(ctx, object, req)
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
	report, err := migration.NewEngine(runs, newCSVWriters(h.db, run.ID, object)).DryRun(ctx, source)
	if err != nil {
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

	runs := migration.NewRunStore(h.db)
	approved, err := runs.Approve(ctx, migration.RunID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	object := run.Mapping.Object
	source := migration.NewCSVSource(h.blobs, run.SourceRef, object, run.Mapping.Fields, run.Mapping.SourceKey)
	writers := newCSVWriters(h.db, approved.ID, object)
	if _, err := migration.NewEngine(runs, writers).Run(ctx, approved.ID, source); err != nil {
		// The engine has already recorded the failure and its checkpoint on the
		// run; the caller is told which run to resume rather than being handed
		// a bare 500.
		httperr.Write(w, r, importProblem(err))
		return
	}

	final, err := runs.GetStaged(ctx, approved.ID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, toContractImportRun(final))
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

// mappingFrom validates the requested mapping against the object's live field
// catalog. A target the object does not have is refused here rather than at row
// 40,000 of a commit.
func (h importHandlers) mappingFrom(ctx context.Context, object string, req crmcontracts.CreateImportRunRequest) (migration.RunMapping, error) {
	targets, err := importTargets(ctx, h.catalog, object)
	if err != nil {
		return migration.RunMapping{}, err
	}
	allowed := make(map[string]bool, len(targets))
	for _, t := range targets {
		allowed[t] = true
	}
	fields := map[string]string{}
	for column, target := range req.Mapping {
		if !allowed[target] {
			return migration.RunMapping{}, httperr.Validation("mapping", "unknown_target",
				fmt.Sprintf("%q is not a field a %s can receive.", target, object))
		}
		fields[column] = target
	}
	if len(fields) == 0 {
		return migration.RunMapping{}, httperr.Validation("mapping", "empty",
			"A run with no mapped column would import nothing.")
	}

	sourceKey := csvSourceKeyDefault[object]
	if req.SourceKey != nil && strings.TrimSpace(*req.SourceKey) != "" {
		sourceKey = strings.TrimSpace(*req.SourceKey)
	} else {
		// The default names a TARGET field; the source must name the column
		// mapped onto it, or no row can be identified.
		sourceKey = columnFor(fields, sourceKey)
	}
	if sourceKey == "" {
		return migration.RunMapping{}, httperr.Validation("source_key", "unmappable",
			fmt.Sprintf("Map a column to %q, or name the column that identifies a row.", csvSourceKeyDefault[object]))
	}
	if _, mapped := fields[sourceKey]; !mapped {
		return migration.RunMapping{}, httperr.Validation("source_key", "unmapped_column",
			fmt.Sprintf("%q is not one of the mapped columns.", sourceKey))
	}
	return migration.RunMapping{Object: object, Fields: fields, SourceKey: sourceKey}, nil
}

// columnFor answers which source column was mapped onto a target field.
func columnFor(fields map[string]string, target string) string {
	for column, mapped := range fields {
		if mapped == target {
			return column
		}
	}
	return ""
}

// importProblem keeps a file's own failures on the caller's side of the line:
// an unreadable upload or an unusable header is the customer's file, not our
// server, and it is told which so they can fix it.
func importProblem(err error) error {
	switch {
	case errors.Is(err, migration.ErrHeaderInvalid):
		return httperr.Validation("file", "header_unusable", err.Error())
	case errors.Is(err, migration.ErrSourceUnreadable):
		return httperr.Validation("file", "unreadable", err.Error())
	case errors.Is(err, migration.ErrObjectNotInSource):
		return httperr.Validation("object", "mismatch", err.Error())
	default:
		return err
	}
}

// withSkippedLines folds the source's own disclosures into the object's report:
// a row the source could not deliver never reached the writer, so nothing else
// in the report would ever mention it.
func withSkippedLines(report migration.Report, object string, skipped []migration.SkippedLine) migration.Report {
	if len(skipped) == 0 {
		return report
	}
	for i := range report.Objects {
		if report.Objects[i].Object != object {
			continue
		}
		for _, s := range skipped {
			report.Objects[i].Skipped = append(report.Objects[i].Skipped, migration.SkippedRow{
				ExternalID: fmt.Sprintf("line %d", s.Line),
				Reason:     s.Reason,
			})
		}
	}
	return report
}

func toContractColumns(p migration.Profile) []crmcontracts.ImportColumn {
	out := make([]crmcontracts.ImportColumn, 0, len(p.Columns))
	for _, c := range p.Columns {
		samples := c.Samples
		if samples == nil {
			samples = []string{}
		}
		out = append(out, crmcontracts.ImportColumn{Header: c.Header, FillRate: float32(c.FillRate), Samples: samples})
	}
	return out
}

func toContractImportRun(run migration.Run) crmcontracts.ImportRun {
	out := crmcontracts.ImportRun{
		Id:         openapi_types.UUID(run.ID),
		Connector:  crmcontracts.ImportRunConnector(run.Connector),
		Status:     crmcontracts.ImportRunStatus(run.Status),
		Checkpoint: run.Checkpoint,
		Source:     importSourceProvenance,
		CreatedAt:  run.CreatedAt,
		UpdatedAt:  run.UpdatedAt,
	}
	if run.Mapping != nil {
		out.Object = crmcontracts.ImportObject(run.Mapping.Object)
	}
	if run.Error != "" {
		message := run.Error
		out.Error = &message
	}
	return out
}

func toContractImportReport(run migration.Run) crmcontracts.ImportRunReport {
	out := crmcontracts.ImportRunReport{
		RunId:  openapi_types.UUID(run.ID),
		Status: crmcontracts.ImportRunStatus(run.Status),
		Issues: []crmcontracts.ImportRowIssue{},
	}
	if run.Mapping != nil {
		out.SourceKeyUsed = run.Mapping.SourceKey
	}
	for _, o := range run.Report.Objects {
		out.RowsRead += o.MirrorCount
		out.Disposition.Created += o.Created + o.WillCreate
		out.Disposition.Updated += o.Updated + o.WillUpdate
		out.Disposition.Unchanged += o.Unchanged
		out.Disposition.Skipped += len(o.Skipped)
		for _, s := range o.Skipped {
			out.Issues = append(out.Issues, crmcontracts.ImportRowIssue{
				Line:   lineOf(s.ExternalID),
				Reason: s.Reason,
			})
		}
	}
	return out
}

// lineOf recovers the file line a skip named. The source records skips as
// "line N" because a row it could not identify has no external id to carry.
func lineOf(externalID string) int {
	var line int
	if _, err := fmt.Sscanf(externalID, "line %d", &line); err != nil {
		return 0
	}
	return line
}

// readImportUpload takes the multipart body apart under the 10 MB cap and
// returns the object the rows are and the file's bytes.
//
// The bytes are read whole rather than streamed to the blobstore, and that is
// deliberate: the same upload must be BOTH profiled and stored, and a stream
// can only be consumed once. The cap is what makes reading it whole safe, and
// MaxBytesReader (not a bare LimitReader) is what turns an over-cap upload into
// a refusal rather than a file silently cut in half.
func readImportUpload(w http.ResponseWriter, r *http.Request) (string, []byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
	//nolint:gosec // r.Body is bounded by http.MaxBytesReader above, so the total parse size is capped; this argument only sets the in-memory/spill threshold.
	if err := r.ParseMultipartForm(maxImportUploadBytes); err != nil {
		return "", nil, httperr.Validation("file", "unreadable",
			"The upload could not be read, or it exceeds the 10 MB import cap.")
	}

	object := strings.TrimSpace(r.FormValue("object"))
	if _, ok := csvTargets[object]; !ok {
		return "", nil, httperr.Validation("object", "unsupported",
			"An import lands leads or organizations; name one of them.")
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		return "", nil, httperr.Validation("file", "missing", "Attach the file to import as `file`.")
	}
	defer func() { _ = file.Close() }()

	body, err := readAllUpload(file)
	if err != nil {
		return "", nil, httperr.Validation("file", "unreadable", "The upload could not be read.")
	}
	if len(body) == 0 {
		return "", nil, httperr.Validation("file", "empty", "The uploaded file has no content.")
	}
	return object, body, nil
}

func readAllUpload(file multipart.File) ([]byte, error) {
	body, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("reading the uploaded file: %w", err)
	}
	return body, nil
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The import surface's wire half: what a request must satisfy before a run
// exists, what one uploaded part yields, and how a run and its report reach
// the contract's shapes. Kept beside the handlers rather than inside them so
// each door stays readable as a sequence of decisions.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// mappingFrom validates the requested mapping against the object's live field
// catalog. A target the object does not have is refused here rather than at row
// 40,000 of a commit.
func mappingFrom(object string, req crmcontracts.CreateImportRunRequest) (migration.RunMapping, error) {
	targets, err := importTargets(object)
	if err != nil {
		return migration.RunMapping{}, err
	}
	allowed := make(map[string]bool, len(targets))
	for _, t := range targets {
		allowed[t] = true
	}
	fields := map[string]string{}
	claimed := map[string]string{}
	for column, target := range req.Mapping {
		if !allowed[target] {
			return migration.RunMapping{}, httperr.Validation("mapping", "unknown_target",
				fmt.Sprintf("%q is not a field a %s can receive.", target, object))
		}
		if other, taken := claimed[target]; taken {
			// Two columns onto one field: whichever the row builder happened to
			// write last would win, so identical requests could import
			// different data. Refused rather than resolved by luck.
			return migration.RunMapping{}, httperr.Validation("mapping", "duplicate_target",
				fmt.Sprintf("%q and %q both map to %q; one column per field.", other, column, target))
		}
		claimed[target] = column
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
	case errors.Is(err, blobstore.ErrNotFound):
		// The reference was this workspace's (ownsSource proved that) but the
		// bytes are gone. Not-found names what is actually missing; a 500 would
		// blame the server for a source the caller can simply re-upload.
		return fmt.Errorf("the uploaded file behind this run is no longer stored: %w", apperrors.ErrNotFound)
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
		CapturedBy: &run.CapturedBy,
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

	// Predicted counts and actual ones are never summed: a finished run reports
	// what it did, an awaiting one what it will do. Adding them would report
	// twice the rows the file holds — and the stored report DOES carry both,
	// because a run's own report is merged into the dry run's so a resumed
	// attempt keeps what the earlier one already achieved.
	committed := run.Status == migration.StatusComplete || run.Status == migration.StatusFailed ||
		run.Status == migration.StatusUndoing || run.Status == migration.StatusUndone
	seen := map[int]bool{}
	for _, o := range run.Report.Objects {
		out.RowsRead += o.MirrorCount
		if committed {
			out.Disposition.Created += o.Created
			out.Disposition.Updated += o.Updated
		} else {
			out.Disposition.Created += o.WillCreate
			out.Disposition.Updated += o.WillUpdate
		}
		for _, s := range o.Skipped {
			// The same row skipped by the dry run and again by the commit is
			// ONE row the human must go fix, named by its line.
			line := lineOf(s.ExternalID)
			if seen[line] {
				continue
			}
			seen[line] = true
			out.Disposition.Skipped++
			out.Issues = append(out.Issues, crmcontracts.ImportRowIssue{
				Line:   line,
				Reason: s.Reason,
			})
		}
	}

	// Unchanged is DERIVED, never carried: it is the rows that were read and
	// then neither created, updated nor skipped. Carrying the stored figure
	// would add the dry run's count to the commit's and report more rows than
	// the file holds — which is the contract's own invariant ("the four sum to
	// the rows read") failing in the one place a human reads it.
	out.Disposition.Unchanged = max(
		out.RowsRead-out.Disposition.Created-out.Disposition.Updated-out.Disposition.Skipped,
		0,
	)
	// Present once the run has been undone, not while undoing — a
	// still-in-progress or interrupted reversal's partial counts are the
	// run's own internal resume state, not a finished outcome to report.
	if run.UndoReport != nil && run.Status == migration.StatusUndone {
		out.Undo = toContractUndoReport(run.ID, run.Status, *run.UndoReport)
	}
	return out
}

// toContractUndoReport renders the reversal outcome (IEM-WIRE-9).
func toContractUndoReport(id migration.RunID, status string, rep migration.UndoReport) *crmcontracts.ImportUndoReport {
	kept := make([]struct {
		Id     openapi_types.UUID        `json:"id"` //nolint:staticcheck // matches the generated ImportUndoReport.Kept item shape
		Object crmcontracts.ImportObject `json:"object"`
	}, 0, len(rep.Kept))
	for _, k := range rep.Kept {
		kept = append(kept, struct {
			Id     openapi_types.UUID        `json:"id"` //nolint:staticcheck // matches the generated ImportUndoReport.Kept item shape
			Object crmcontracts.ImportObject `json:"object"`
		}{Id: openapi_types.UUID(k.ID), Object: crmcontracts.ImportObject(k.Object)})
	}
	errored := make([]struct {
		Id     openapi_types.UUID        `json:"id"` //nolint:staticcheck // matches the generated ImportUndoReport.Errored item shape
		Object crmcontracts.ImportObject `json:"object"`
		Reason string                    `json:"reason"`
	}, 0, len(rep.Errored))
	for _, e := range rep.Errored {
		errored = append(errored, struct {
			Id     openapi_types.UUID        `json:"id"` //nolint:staticcheck // matches the generated ImportUndoReport.Errored item shape
			Object crmcontracts.ImportObject `json:"object"`
			Reason string                    `json:"reason"`
		}{Id: openapi_types.UUID(e.ID), Object: crmcontracts.ImportObject(e.Object), Reason: e.Reason})
	}
	return &crmcontracts.ImportUndoReport{
		RunId:         openapi_types.UUID(id),
		Status:        crmcontracts.ImportRunStatus(status),
		ReversedCount: rep.ReversedCount,
		Kept:          kept,
		Errored:       errored,
	}
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
	defer func(ctx context.Context) {
		if cerr := file.Close(); cerr != nil {
			slog.WarnContext(ctx, "closing the uploaded import part", "err", cerr)
		}
	}(r.Context())

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

// refinePrediction replaces the engine's create/update split with one the
// report can honestly show a human.
//
// The engine classifies from Writers.Exists alone, so every row that already
// landed counts as an update — including the ones whose values are identical,
// which the commit will not rewrite. A dry run whose whole job is to say what
// WILL happen may not overstate it by the size of the customer's re-upload, so
// each row is compared here exactly as the commit will compare it.
func refinePrediction(ctx context.Context, source *migration.CSVSource, writers *csvWriters, object string, report migration.Report) (migration.Report, error) {
	created, updated, unchanged, err := predictPages(ctx, source, writers, object)
	if err != nil {
		return migration.Report{}, err
	}
	for i := range report.Objects {
		if report.Objects[i].Object != object {
			continue
		}
		report.Objects[i].WillCreate = created
		report.Objects[i].WillUpdate = updated
		report.Objects[i].Unchanged = unchanged
	}
	return report, nil
}

// predictPages walks the source and tallies what the commit would do with each
// row, page by page, the same way the engine will walk it.
func predictPages(ctx context.Context, source *migration.CSVSource, writers *csvWriters, object string) (created, updated, unchanged int, err error) {
	for offset := 0; ; offset += importPredictPage {
		rows, err := source.Rows(ctx, object, offset, importPredictPage)
		if err != nil {
			return 0, 0, 0, err
		}
		for _, row := range rows {
			outcome, err := writers.Predict(ctx, row)
			if err != nil {
				return 0, 0, 0, err
			}
			switch outcome {
			case predictCreate:
				created++
			case predictUpdate:
				updated++
			case predictUnchanged:
				unchanged++
			}
		}
		if len(rows) < importPredictPage {
			return created, updated, unchanged, nil
		}
	}
}

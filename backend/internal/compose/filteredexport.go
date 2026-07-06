// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// First-class filtered export (B-E15.13, features/10 §3): export one
// object type — or a saved view / dynamic list — filtered by the canonical
// predicate, to an open format (CSV or JSON), audited. It reuses the three
// pieces that already ship rather than growing a fourth filter/scope/format
// path:
//
//   - the ONE predicate engine (storekit.Query.SelectIDs via
//     collections.SegmentEngine) forces auth.ScopeClauseFor, so the slice
//     is exactly the rows the caller could already see AND that match the
//     filter — a filtered export can never widen visibility;
//   - the bundle writer's schema-derived columns + open-format rendering
//     (export.go: exportableColumns / rowsAsMaps / csvCell), so custom
//     fields ride along automatically and the CSV/JSON shape is one
//     spelling;
//   - saved views / dynamic lists as the filter source
//     (collections.SavedViewFilterSource / ListFilterSource), behind their
//     own owner-only / row-scope gates.
//
// The export operation is a bulk read that could exfiltrate at scale, so
// the endpoint is human-only (no agent principal) — and every export
// writes one audit_log 'export' row through the storekit shape.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// exportFileFormat is the requested open format; anything else is a client
// error, never a silent default.
type exportFileFormat string

const (
	exportFmtCSV  exportFileFormat = "csv"
	exportFmtJSON exportFileFormat = "json"
)

// FilteredExportResult is a rendered slice ready to stream: the wire
// content type, a download filename, the bytes, and the row count the
// audit row recorded.
type FilteredExportResult struct {
	ContentType string
	Filename    string
	Body        []byte
	RowCount    int
}

// FilteredExportWriter renders a resource's filtered slice in an open
// format over the shared pool.
type FilteredExportWriter struct {
	pool *pgxpool.Pool
}

// NewFilteredExportWriter builds the writer over the shared pool.
func NewFilteredExportWriter(pool *pgxpool.Pool) *FilteredExportWriter {
	return &FilteredExportWriter{pool: pool}
}

// exportBadRequest is a client fault the transport maps to 422 (an
// unsupported resource or format), distinct from an infrastructure error.
type exportBadRequest struct {
	field  string
	reason string
}

func (e *exportBadRequest) Error() string { return e.field + ": " + e.reason }

// WriteFiltered runs the resource's predicate through the ONE scope-forcing
// engine, reads the exact visible+matching rows with the bundle writer's
// schema-derived columns, renders them to the requested format, and writes
// one audit_log 'export' row — all inside a single workspace-bound
// transaction so the audit is atomic with the read it records.
func (w *FilteredExportWriter) WriteFiltered(ctx context.Context, resource string, pred storekit.Predicate, format exportFileFormat) (FilteredExportResult, error) {
	if format != exportFmtCSV && format != exportFmtJSON {
		return FilteredExportResult{}, &exportBadRequest{field: "format", reason: "must be csv or json"}
	}
	engine, ok := collections.SegmentEngine(resource)
	if !ok {
		return FilteredExportResult{}, &exportBadRequest{
			field:  "object",
			reason: fmt.Sprintf("%q is not a filter-exportable object", resource),
		}
	}

	var result FilteredExportResult
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		// The scope-forcing executor: the object RBAC gate + row-scope
		// clause + the predicate, composed in one place, hard-capped.
		matched, err := engine.SelectIDs(ctx, tx, pred, storekit.PredicateRowLimit)
		if err != nil {
			return err
		}
		columns, err := exportableColumns(ctx, tx, engine.Table)
		if err != nil {
			return err
		}
		rows, err := readRowsByID(ctx, tx, engine.Table, columns, matched)
		if err != nil {
			return err
		}
		data := memberData{table: engine.Table, columns: columns, rows: rows}

		body, contentType, err := renderExport(data, format)
		if err != nil {
			return err
		}
		result = FilteredExportResult{
			ContentType: contentType,
			Filename:    fmt.Sprintf("%s-export.%s", engine.Table, format),
			Body:        body,
			RowCount:    len(rows),
		}

		// The export operation itself is audited (P7/P12): who exported
		// which slice, when. It targets no single record (entity_id NULL),
		// so the filter and the row count stand in for "what slice".
		_, err = storekit.Audit(ctx, tx, "export", engine.Table, ids.Nil, nil, map[string]any{
			"kind":      "filtered",
			"format":    string(format),
			"row_count": len(rows),
			"filter":    pred,
		})
		return err
	})
	if err != nil {
		return FilteredExportResult{}, err
	}
	return result, nil
}

// readRowsByID reads the given rows' exportable columns, ordered by id for
// a deterministic slice. The ids come from the scope-forcing executor, so
// they are already the caller's visible+matching set; the table and column
// identifiers come from the trusted vocabulary and the live catalog, never
// from the request. An empty id set is an honest empty slice, not an error.
func readRowsByID(ctx context.Context, tx pgx.Tx, table string, columns []string, idList []ids.UUID) ([][]any, error) {
	if len(idList) == 0 {
		return nil, nil
	}
	selects := make([]string, len(columns))
	for i, col := range columns {
		selects[i] = "t." + col
	}
	sql := fmt.Sprintf("SELECT %s FROM %s t WHERE t.id = ANY($1) ORDER BY t.id",
		strings.Join(selects, ", "), table)
	pgRows, err := tx.Query(ctx, sql, idList)
	if err != nil {
		return nil, fmt.Errorf("filtered export read %s: %w", table, err)
	}
	defer pgRows.Close()

	var out [][]any
	for pgRows.Next() {
		values, err := pgRows.Values()
		if err != nil {
			return nil, err
		}
		out = append(out, values)
	}
	if err := pgRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// renderExport renders one member as the requested open format, reusing the
// bundle writer's cell/value normalizers so a filtered slice and a full
// bundle spell the same CSV and JSON.
func renderExport(data memberData, format exportFileFormat) (body []byte, contentType string, err error) {
	var buf bytes.Buffer
	switch format {
	case exportFmtCSV:
		cw := csv.NewWriter(&buf)
		if err := cw.Write(data.columns); err != nil {
			return nil, "", err
		}
		record := make([]string, len(data.columns))
		for _, row := range data.rows {
			for i := range data.columns {
				record[i] = csvCell(row[i])
			}
			if err := cw.Write(record); err != nil {
				return nil, "", err
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "text/csv; charset=utf-8", nil
	case exportFmtJSON:
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(filteredExportDoc{
			Format:   exportFormat,
			Object:   data.table,
			Rows:     rowsAsMaps(data),
			RowCount: len(data.rows),
		}); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "application/json; charset=utf-8", nil
	default:
		return nil, "", &exportBadRequest{field: "format", reason: "must be csv or json"}
	}
}

// filteredExportDoc is the JSON export envelope: the self-describing format
// tag, the exported object, and its rows as column→value maps.
//
//craft:ignore naked-any Rows carries schema-derived column→value maps, whose value types span every SQL type the object holds
type filteredExportDoc struct {
	Format   string           `json:"format"`
	Object   string           `json:"object"`
	RowCount int              `json:"row_count"`
	Rows     []map[string]any `json:"rows"`
}

// filteredExportHandlers shadows the generated CreateFilteredExport stub.
type filteredExportHandlers struct {
	writer      *FilteredExportWriter
	collections *collections.Store
}

// filteredExportRequest is the export body: exactly one source — an inline
// object (with a required filter), a saved view, or a dynamic list — plus
// the open format. The filter is the canonical predicate tree.
type filteredExportRequest struct {
	Object string              `json:"object"`
	Filter *storekit.Predicate `json:"filter"`
	ViewID string              `json:"view_id"`
	ListID string              `json:"list_id"`
	Format string              `json:"format"`
}

func (h filteredExportHandlers) CreateFilteredExport(w http.ResponseWriter, r *http.Request) {
	var req filteredExportRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	resource, pred, err := h.resolveSource(r.Context(), req)
	if err != nil {
		writeFilteredExportError(w, r, err)
		return
	}

	result, err := h.writer.WriteFiltered(r.Context(), resource, pred, exportFileFormat(req.Format))
	if err != nil {
		writeFilteredExportError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", result.ContentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", result.Filename))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(result.Body); err != nil {
		// The body is fully rendered before the header is written, so a
		// write failure here is a broken client connection, not a
		// server fault we can still answer — log-and-return is all that
		// remains once the 200 line is on the wire.
		return
	}
}

// resolveSource turns the request's chosen source into the (resource,
// predicate) pair the engine runs. Exactly one of object / view_id /
// list_id must be set; an inline object additionally requires a filter
// (this is filtered export — an unfiltered whole-object dump is the export
// bundle's job, not this path).
func (h filteredExportHandlers) resolveSource(ctx context.Context, req filteredExportRequest) (string, storekit.Predicate, error) {
	sources := 0
	if req.Object != "" {
		sources++
	}
	if req.ViewID != "" {
		sources++
	}
	if req.ListID != "" {
		sources++
	}
	if sources != 1 {
		return "", storekit.Predicate{}, &exportBadRequest{
			field:  "object",
			reason: "supply exactly one of object, view_id, or list_id",
		}
	}

	switch {
	case req.Object != "":
		if req.Filter == nil {
			return "", storekit.Predicate{}, &exportBadRequest{
				field:  "filter",
				reason: "a filtered export of an object needs a filter",
			}
		}
		return req.Object, *req.Filter, nil
	case req.ViewID != "":
		id, err := ids.Parse(req.ViewID)
		if err != nil {
			return "", storekit.Predicate{}, &exportBadRequest{field: "view_id", reason: "must be a UUID"}
		}
		src, err := h.collections.SavedViewFilterSource(ctx, id)
		if err != nil {
			return "", storekit.Predicate{}, err
		}
		return src.Resource, src.Predicate, nil
	default:
		id, err := ids.Parse(req.ListID)
		if err != nil {
			return "", storekit.Predicate{}, &exportBadRequest{field: "list_id", reason: "must be a UUID"}
		}
		src, err := h.collections.ListFilterSource(ctx, id)
		if err != nil {
			return "", storekit.Predicate{}, err
		}
		return src.Resource, src.Predicate, nil
	}
}

// writeFilteredExportError maps the export faults to the wire: a bad
// request, a rejected predicate, and a rejected source (collections'
// BadInputError) all become a 422 naming the offending field; everything
// else rides the sentinels (403 object denial, 404 existence-hiding, 5xx).
func writeFilteredExportError(w http.ResponseWriter, r *http.Request, err error) {
	var bad *exportBadRequest
	if errors.As(err, &bad) {
		httperr.Write(w, r, httperr.Validation(bad.field, "invalid", bad.reason))
		return
	}
	var pred *storekit.PredicateError
	if errors.As(err, &pred) {
		httperr.Write(w, r, httperr.Validation(pred.Field, pred.Code, pred.Message))
		return
	}
	var source *collections.BadInputError
	if errors.As(err, &source) {
		httperr.Write(w, r, httperr.Validation(source.Field, "invalid", source.Reason))
		return
	}
	httperr.Write(w, r, err)
}

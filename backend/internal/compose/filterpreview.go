// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Previewing a filter nobody has saved yet (LVS-EXT-9).
//
// A filter builder shows a count that recomputes as clauses change, and until
// now nothing in the contract could evaluate the tree it recomputes for: the
// object list operations take flat scalar parameters and cannot express a tree,
// membership needs a stored list, and the filtered export is audit-logged — so
// driving a live recount through it would write an audit row per keystroke.
//
// It lives in compose rather than in collections for the same reason filtered
// export does: it needs the export's schema-derived projection as well as the
// collections store's engine, and a module may not import a sibling. Sharing
// that projection is also what makes the preview honest — the columns and values
// are the ones the JSON export writes for the same filter, so somebody deciding
// from a preview is deciding about the thing they would get.
//
// What this deliberately does NOT do is write: no row, no audit entry, no outbox
// event. That is the property separating it from the export, and it is why the
// operation is human-only in the contract — un-audited is right for somebody
// typing and wrong for an agent reading records in bulk.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// filterPreviewDefaultRows is the page a caller who names no limit gets, and
// filterPreviewMaxRows the ceiling. Both are about what a builder renders while
// somebody types — a preview is a glance, not a report. The contract publishes
// the same two numbers.
const (
	filterPreviewDefaultRows = 25
	filterPreviewMaxRows     = 100
)

// filterPreviewHandlers shadows the generated PreviewFilter stub.
type filterPreviewHandlers struct {
	pool        *pgxpool.Pool
	collections *collections.Store
}

// filterPreviewRequest is the preview body, decoded here rather than through the
// generated type for one reason: the generated Filter is a map, and the engine
// takes a storekit.Predicate. Decoding straight into the predicate — exactly as
// filteredExportRequest does — keeps the tree typed from the wire inward instead
// of re-marshalling a map to get there.
type filterPreviewRequest struct {
	Resource string              `json:"resource"`
	Filter   *storekit.Predicate `json:"filter"`
	Limit    *int                `json:"limit"`
}

func (h filterPreviewHandlers) PreviewFilter(w http.ResponseWriter, r *http.Request) {
	var req filterPreviewRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Filter == nil {
		httperr.Write(w, r, httperr.Validation("filter", codeInvalid,
			"a filter tree is required — preview evaluates a candidate filter, not the whole object"))
		return
	}
	engine, ok, err := h.collections.SegmentEngine(r.Context(), req.Resource)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if !ok {
		httperr.Write(w, r, httperr.Validation("resource", codeInvalid,
			fmt.Sprintf("%q is not a filterable resource", req.Resource)))
		return
	}

	preview, err := h.preview(r.Context(), engine, *req.Filter, rowLimit(req.Limit))
	if err != nil {
		writeFilterPreviewError(w, r, err)
		return
	}
	preview.Resource = crmcontracts.FilterPreviewResource(req.Resource)
	httperr.WriteJSON(w, http.StatusOK, preview)
}

// preview runs the count and the page in ONE transaction.
//
// Two transactions against a moving table would let a concurrent write land
// between them, and the caller would read "812 matches" above a page whose rows
// do not add up — a discrepancy they cannot explain and would reasonably read as
// a bug in the filter they are building.
func (h filterPreviewHandlers) preview(
	ctx context.Context, engine storekit.Query, pred storekit.Predicate, limit int,
) (crmcontracts.FilterPreview, error) {
	var out crmcontracts.FilterPreview
	err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		count, err := engine.CountMatching(ctx, tx, pred)
		if err != nil {
			return err
		}
		matched, err := engine.SelectIDs(ctx, tx, pred, limit)
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
		out = crmcontracts.FilterPreview{
			MatchCount: count,
			Columns:    columns,
			Rows:       rowsAsMaps(memberData{table: engine.Table, columns: columns, rows: rows}),
			Truncated:  count > len(rows),
		}
		return nil
	})
	return out, err
}

// rowLimit clamps the requested page. An absent limit is the default rather than
// "as many as allowed": a caller who did not ask for a size is a builder
// rendering a glance, and the ceiling is there so one cannot ask for a report.
func rowLimit(requested *int) int {
	if requested == nil || *requested <= 0 {
		return filterPreviewDefaultRows
	}
	if *requested > filterPreviewMaxRows {
		return filterPreviewMaxRows
	}
	return *requested
}

// writeFilterPreviewError maps a refused predicate onto the 422 the contract
// promises, naming the offending field. Everything else — a permission denial
// from the engine's own read gate, a database fault — travels as itself.
func writeFilterPreviewError(w http.ResponseWriter, r *http.Request, err error) {
	var pred *storekit.PredicateError
	if errors.As(err, &pred) {
		httperr.Write(w, r, httperr.Validation(pred.Field, pred.Code, pred.Message))
		return
	}
	httperr.Write(w, r, err)
}

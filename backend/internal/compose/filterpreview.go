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
// operation is human-only — un-logged is right for somebody typing and wrong for
// an agent reading records in bulk.
//
// Why un-logged is defensible at all, stated as the fact rather than the
// intuition: GET /v1/people already returns the same Person projection, 200 rows
// a page WITH a cursor, writing no ledger row either. Preview is strictly less
// capable — 100 rows, no cursor — so it opens no channel a human with read access
// did not have. The export's system_log row is about an extraction ARTIFACT
// leaving the system, which a preview does not produce; it is not a general
// obligation on reading records, or the paged list read would owe one too.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
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
	// previewStatementTimeoutMS bounds each statement in a preview. Generous for
	// an indexable filter and short enough that an expensive one cannot hold a
	// pool connection: a glance that takes five seconds has already failed at
	// being a glance. A literal rather than a bind, because SET LOCAL takes no
	// parameters — which is safe only because it is a constant here and never a
	// caller's value.
	previewStatementTimeoutMS = "5000"
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
	// The human-only class is enforced here as well as at the transport, the same
	// pairing the bundle export and overlay's user-map reads use: this returns
	// records in bulk and writes no ledger row, so it should not rest on
	// route-pattern resolution alone.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var req filterPreviewRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Filter == nil {
		httperr.Write(w, r, httperr.Validation("filter", codeInvalid,
			"a filter tree is required — preview evaluates a candidate filter, not the whole object"))
		return
	}
	limit, err := previewRowLimit(req.Limit)
	if err != nil {
		httperr.Write(w, r, err)
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

	preview, err := h.preview(r.Context(), engine, *req.Filter, limit)
	if err != nil {
		writeFilterPreviewError(w, r, err)
		return
	}
	preview.Resource = crmcontracts.FilterPreviewResource(req.Resource)
	httperr.WriteJSON(w, http.StatusOK, preview)
}

// preview runs the count and the page in ONE transaction, which buys less than
// it looks like and is still worth having.
//
// What it does NOT buy is a shared snapshot. These run at READ COMMITTED — the
// pool sets no isolation level — so each statement takes its own snapshot and a
// commit landing between them is visible to the second. Claiming otherwise would
// invite the next reader to rely on stability that is not there.
//
// What it does buy: one connection, one workspace binding, and two statements
// back to back, which is the smallest window available without paying for
// REPEATABLE READ. A preview is a glance at a moving table, and a count one write
// stale is the honest cost of that — the alternative is a serialization failure
// shown to somebody who is only typing.
func (h filterPreviewHandlers) preview(
	ctx context.Context, engine storekit.Query, pred storekit.Predicate, limit int,
) (crmcontracts.FilterPreview, error) {
	var out crmcontracts.FilterPreview
	err := database.WithWorkspaceTx(ctx, h.pool, func(tx pgx.Tx) error {
		// The count is unbounded by design and its predicate is written by the
		// caller — up to PredicateMaxLeaves substring matches, each of which can
		// mean a sequential scan, over the largest tables. That combination is new
		// here: the automation preview also counts without a cap, but from a fixed
		// catalog of predicates rather than a tree somebody typed.
		//
		// So the statement, not the tree, carries the ceiling. A filter too
		// expensive to count is refused in bounded time instead of holding a pool
		// connection for as long as it takes, which is the difference between one
		// slow request and a server that stops answering.
		if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = "+previewStatementTimeoutMS); err != nil {
			return fmt.Errorf("bound the preview statement: %w", err)
		}
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

// previewRowLimit resolves the requested page size, refusing what the contract
// calls invalid rather than quietly substituting something else.
//
// An ABSENT limit is the documented default: a caller who did not ask for a size
// is a builder rendering a glance. A limit outside the published 1..100 is a 422,
// which is the sibling preview's convention (the automation preview refuses
// window_days = 0 for the same reason) and the only answer consistent with
// declaring bounds at all — silently returning 100 rows to somebody who asked for
// 5000 tells them their request was honoured when it was rewritten.
func previewRowLimit(requested *int) (int, error) {
	if requested == nil {
		return filterPreviewDefaultRows, nil
	}
	if *requested < 1 || *requested > filterPreviewMaxRows {
		return 0, httperr.Validation("limit", codeInvalid,
			fmt.Sprintf("limit must be between 1 and %d — a preview is a glance, not a report", filterPreviewMaxRows))
	}
	return *requested, nil
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

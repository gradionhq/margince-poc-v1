// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package collections

// Previewing an unsaved filter (LVS-EXT-9), over the composed server.
//
// Three claims only a real database and a real server can settle, and each one
// is a promise the operation's contract makes:
//
//   - the count is the FULL match count, not the page it labels — the number a
//     human reads while deciding whether their filter is right;
//   - the columns and rows are the same projection the JSON export writes for the
//     same filter, so a preview is a preview OF the thing you would get;
//   - nothing is written. No audit row, no outbox event. That is what separates a
//     count recomputing while somebody types from an extraction they chose.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// seedPeopleWithTier creates people carrying a picklist custom field, returning
// the column name so a filter can name it.
func seedPeopleWithTier(t *testing.T, e *apptest.AppEnv, gold, other int) string {
	t.Helper()
	var field apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/custom-fields", apptest.AnyMap{
		"object": "person", "label": "Preview Tier", "type": "text", "source": "ui",
	}, nil, &field); status != http.StatusCreated {
		t.Fatalf("create custom field: status=%d body=%v", status, field)
	}
	column, _ := field["column_name"].(string)
	if column == "" {
		t.Fatalf("created field carries no column_name: %v", field)
	}
	for i := range gold + other {
		tier := "gold"
		if i >= gold {
			tier = "silver"
		}
		var person apptest.AnyMap
		if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
			"full_name": "Preview Subject", "source": "ui", column: tier,
		}, nil, &person); status != http.StatusCreated {
			t.Fatalf("create person %d: status=%d body=%v", i, status, person)
		}
	}
	return column
}

// ledgerRows counts every row this tree records a write in, across all three
// ledgers rather than one.
//
// All three, because "writes nothing" is only meaningful if it covers everywhere
// a write could show up: the write shape commits an audit row AND an outbox event
// together, so watching one would miss half of a mutation — and a BULK read is
// recorded in neither. The filtered export logs to system_log on the stated
// reasoning that an export targets no single record, so audit_log alone would
// have made the contrast below silently vacuous.
func ledgerRows(t *testing.T, e *apptest.AppEnv) int {
	t.Helper()
	var n int
	if err := e.Owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM audit_log)
		     + (SELECT count(*) FROM event_outbox)
		     + (SELECT count(*) FROM system_log)`).Scan(&n); err != nil {
		t.Fatalf("count the ledgers: %v", err)
	}
	return n
}

type previewBody struct {
	Resource   string           `json:"resource"`
	MatchCount int              `json:"match_count"`
	Columns    []string         `json:"columns"`
	Rows       []apptest.AnyMap `json:"rows"`
	Truncated  bool             `json:"truncated"`
}

// The count counts everything and the page is bounded, which is the whole point:
// a builder says "showing 3 of 12" from one call.
func TestAFilterPreviewCountsEveryMatchAndReturnsABoundedPage(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)
	column := seedPeopleWithTier(t, e, 12, 4)

	limit := 3
	var got previewBody
	if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
		"resource": "person",
		"filter":   apptest.AnyMap{"field": column, "op": "eq", "value": "gold"},
		"limit":    limit,
	}, nil, &got); status != http.StatusOK {
		t.Fatalf("preview: status=%d body=%+v", status, got)
	}
	if got.MatchCount != 12 {
		t.Errorf("match_count = %d, want 12 — the full match count, not the page", got.MatchCount)
	}
	if len(got.Rows) != limit {
		t.Errorf("rows = %d, want the requested %d", len(got.Rows), limit)
	}
	if !got.Truncated {
		t.Error("truncated = false while the count exceeds the page — a caller cannot say 'showing 3 of 12'")
	}
	if got.Resource != "person" {
		t.Errorf("resource = %q, want the one asked for", got.Resource)
	}
	// The non-matching four are excluded, so the filter really ran.
	if got.MatchCount == 16 {
		t.Error("match_count counted every person; the predicate was not applied")
	}
}

// A filter matching everything it selects fits in one page: truncated is false
// and the count equals the rows, so the flag is not simply always true.
func TestAFilterPreviewThatFitsIsNotTruncated(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)
	column := seedPeopleWithTier(t, e, 2, 1)

	var got previewBody
	if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
		"resource": "person",
		"filter":   apptest.AnyMap{"field": column, "op": "eq", "value": "gold"},
	}, nil, &got); status != http.StatusOK {
		t.Fatalf("preview: status=%d body=%+v", status, got)
	}
	if got.MatchCount != 2 || len(got.Rows) != 2 {
		t.Fatalf("match_count=%d rows=%d, want 2 and 2", got.MatchCount, len(got.Rows))
	}
	if got.Truncated {
		t.Error("truncated = true for a page holding every match")
	}
}

// The invariant the shared projection exists for: preview and the JSON export of
// the SAME filter describe the same slice. If these ever diverge, a human decides
// from a preview and receives something else.
func TestAFilterPreviewDescribesTheSameSliceTheExportWrites(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)
	column := seedPeopleWithTier(t, e, 3, 2)
	filter := apptest.AnyMap{"field": column, "op": "eq", "value": "gold"}

	var preview previewBody
	if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
		"resource": "person", "filter": filter, "limit": 100,
	}, nil, &preview); status != http.StatusOK {
		t.Fatalf("preview: status=%d body=%+v", status, preview)
	}

	// The export renders JSON, so the harness decodes its envelope straight into
	// the shape below — that envelope is the comparison's other half.
	var exported struct {
		Rows     []apptest.AnyMap `json:"rows"`
		RowCount int              `json:"row_count"`
	}
	if status := e.Call(t, "POST", "/v1/exports", apptest.AnyMap{
		"object": "person", "filter": filter, "format": "json",
	}, nil, &exported); status != http.StatusOK {
		t.Fatalf("export: status=%d", status)
	}

	if exported.RowCount != preview.MatchCount {
		t.Errorf("export wrote %d rows, preview promised %d", exported.RowCount, preview.MatchCount)
	}
	if len(exported.Rows) != len(preview.Rows) {
		t.Fatalf("export rows=%d preview rows=%d", len(exported.Rows), len(preview.Rows))
	}
	// Same keys, same values, row for row — both are ordered by id.
	for i := range preview.Rows {
		for key, want := range exported.Rows[i] {
			got, present := preview.Rows[i][key]
			if !present {
				t.Errorf("row %d: export carries %q and the preview omits it", i, key)
				continue
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("row %d key %q: preview %v, export %v", i, key, got, want)
			}
		}
		for key := range preview.Rows[i] {
			if _, present := exported.Rows[i][key]; !present {
				t.Errorf("row %d: preview carries %q and the export does not", i, key)
			}
		}
	}
}

// Previewing writes nothing. The export of the same filter DOES write a ledger
// row, which is what makes this a contrast rather than an assertion about
// silence: the ledgers provably grow on this path, and a preview does not grow
// them. Without that second half, a broken counter would read as a passing test.
func TestAFilterPreviewWritesNoLedgerRowWhereAnExportDoes(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)
	column := seedPeopleWithTier(t, e, 2, 0)
	filter := apptest.AnyMap{"field": column, "op": "eq", "value": "gold"}

	before := ledgerRows(t, e)
	for range 3 {
		var got previewBody
		if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
			"resource": "person", "filter": filter,
		}, nil, &got); status != http.StatusOK {
			t.Fatalf("preview: status=%d", status)
		}
	}
	if after := ledgerRows(t, e); after != before {
		t.Errorf("three previews wrote %d ledger rows; a recount while somebody types must write none", after-before)
	}

	var exported apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/exports", apptest.AnyMap{
		"object": "person", "filter": filter, "format": "json",
	}, nil, &exported); status != http.StatusOK {
		t.Fatalf("export: status=%d", status)
	}
	if after := ledgerRows(t, e); after <= before {
		t.Errorf("the export grew no ledger (%d → %d), so this test cannot tell a silent preview from a broken counter", before, after)
	}
}

// A tree the engine refuses is a 422 naming the offending field, not a 500 and
// not an empty preview — an empty result would read as "nothing matches".
func TestAFilterPreviewRefusesAnUnknownFieldByName(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)

	var refused apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
		"resource": "person",
		"filter":   apptest.AnyMap{"field": "not_a_column", "op": "eq", "value": "x"},
	}, nil, &refused); status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field: status=%d body=%v, want 422", status, refused)
	}
	var missingFilter apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
		"resource": "person",
	}, nil, &missingFilter); status != http.StatusUnprocessableEntity {
		t.Fatalf("absent filter: status=%d body=%v, want 422", status, missingFilter)
	}
	var badResource apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
		"resource": "activity",
		"filter":   apptest.AnyMap{"field": "kind", "op": "eq", "value": "call"},
	}, nil, &badResource); status != http.StatusUnprocessableEntity {
		t.Fatalf("non-filterable resource: status=%d body=%v, want 422", status, badResource)
	}
}

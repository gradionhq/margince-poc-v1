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
	"net/http"
	"reflect"
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

// ledgers counts the three places this tree records a write, SEPARATELY.
//
// Separately rather than as a sum, because a sum can only say "something moved":
// it cannot tell an export logging where it should from an export logging into
// the record-mutation spine, and the assertions below want both. All three,
// because "writes nothing" is only meaningful if it covers everywhere a write
// shows up — the write shape commits an audit row AND an outbox event together,
// and a bulk read is recorded in neither.
//
// Read through e.Owner precisely BECAUSE it bypasses RLS: "nothing was written
// anywhere" must not be satisfiable by a row hidden in another workspace.
type ledgerCounts struct{ audit, outbox, system int }

func ledgers(t *testing.T, e *apptest.AppEnv) ledgerCounts {
	t.Helper()
	var c ledgerCounts
	if err := e.Owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM audit_log),
		       (SELECT count(*) FROM event_outbox),
		       (SELECT count(*) FROM system_log)`).Scan(&c.audit, &c.outbox, &c.system); err != nil {
		t.Fatalf("count the ledgers: %v", err)
	}
	return c
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
	// columns is a required field the row comparison never touches, so an empty or
	// mis-sized one would pass every other assertion here.
	if len(got.Columns) == 0 {
		t.Error("columns is empty; a caller cannot render a table from row keys alone")
	}
	if len(got.Rows) > 0 && len(got.Columns) != len(got.Rows[0]) {
		t.Errorf("columns lists %d names for rows carrying %d keys", len(got.Columns), len(got.Rows[0]))
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
	// DeepEqual, not stringified. Both sides return through the same
	// json.Unmarshal so their types already agree — and type divergence with
	// identical TEXT is exactly what matters here: swap rowsAsMaps for a CSV-style
	// renderer and every value becomes a string, which a text comparison would wave
	// through while the preview stopped being a preview of the JSON export. The
	// per-key loop only reports which key diverged.
	for i := range preview.Rows {
		if reflect.DeepEqual(preview.Rows[i], exported.Rows[i]) {
			continue
		}
		for key, want := range exported.Rows[i] {
			if got := preview.Rows[i][key]; !reflect.DeepEqual(got, want) {
				t.Errorf("row %d key %q: preview %#v, export %#v", i, key, got, want)
			}
		}
		for key := range preview.Rows[i] {
			if _, present := exported.Rows[i][key]; !present {
				t.Errorf("row %d: preview carries %q and the export does not", i, key)
			}
		}
	}
}

// Previewing writes nothing, and the export of the same filter writes exactly one
// system_log row and nothing else.
//
// The second half is what makes the first half mean something: without it, a
// counter that had stopped working would read as a passing test. Naming WHICH
// ledger the export grows also turns the reasoning behind that choice — a bulk
// export targets no single record, so it does not belong in the audit spine —
// from a comment into an assertion.
func TestAFilterPreviewWritesNoLedgerRowWhereAnExportDoes(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)
	column := seedPeopleWithTier(t, e, 2, 0)
	filter := apptest.AnyMap{"field": column, "op": "eq", "value": "gold"}

	before := ledgers(t, e)
	for range 3 {
		var got previewBody
		if status := e.Call(t, "POST", "/v1/filters/preview", apptest.AnyMap{
			"resource": "person", "filter": filter,
		}, nil, &got); status != http.StatusOK {
			t.Fatalf("preview: status=%d", status)
		}
	}
	if after := ledgers(t, e); after != before {
		t.Errorf("three previews moved the ledgers %+v → %+v; a recount while somebody types must write nothing", before, after)
	}

	var exported apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/exports", apptest.AnyMap{
		"object": "person", "filter": filter, "format": "json",
	}, nil, &exported); status != http.StatusOK {
		t.Fatalf("export: status=%d", status)
	}
	after := ledgers(t, e)
	if after.system != before.system+1 {
		t.Errorf("system_log grew by %d, want exactly 1 — otherwise this test cannot tell a silent preview from a broken counter", after.system-before.system)
	}
	if after.audit != before.audit {
		t.Errorf("the export wrote %d audit_log row(s); a bulk export targets no single record and does not belong in the record-mutation spine", after.audit-before.audit)
	}
	if after.outbox != before.outbox {
		t.Errorf("the export wrote %d outbox event(s); exporting is not a domain mutation", after.outbox-before.outbox)
	}
}

// A refusal is a 422 that NAMES the offending input, which is the difference
// between a builder highlighting the broken clause and a builder showing a
// generic red banner. Asserting only the status code would leave that promise
// untested — an empty field, or the wrong one, would pass.
func TestAFilterPreviewRefusalNamesTheOffendingInput(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithSchemaPool(integration.SchemaPool(t)))
	e.BootstrapWorkspace(t)
	limitTooLarge := 5000

	for _, c := range []struct {
		name  string
		body  apptest.AnyMap
		field string
	}{
		{"a field the vocabulary does not admit", apptest.AnyMap{
			"resource": "person",
			"filter":   apptest.AnyMap{"field": "not_a_column", "op": "eq", "value": "x"},
		}, "not_a_column"},
		{"no filter at all", apptest.AnyMap{"resource": "person"}, "filter"},
		{"a resource with no engine", apptest.AnyMap{
			"resource": "activity",
			"filter":   apptest.AnyMap{"field": "kind", "op": "eq", "value": "call"},
		}, "resource"},
		// The contract publishes 1..100; a value outside it is refused rather than
		// quietly rewritten, so a caller learns their request was not honoured.
		{"a limit past the published ceiling", apptest.AnyMap{
			"resource": "person",
			"filter":   apptest.AnyMap{"field": "full_name", "op": "contains", "value": "a"},
			"limit":    limitTooLarge,
		}, "limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The per-field breakdown rides details.errors (httperr's fieldDetails),
			// not the problem's top level.
			var problem struct {
				Details struct {
					Errors []struct {
						Field string `json:"field"`
					} `json:"errors"`
				} `json:"details"`
			}
			if status := e.Call(t, "POST", "/v1/filters/preview", c.body, nil, &problem); status != http.StatusUnprocessableEntity {
				t.Fatalf("→ %d, want 422", status)
			}
			if got := problem.Details.Errors; len(got) != 1 || got[0].Field != c.field {
				t.Errorf("field errors = %v, want exactly one naming %q", got, c.field)
			}
		})
	}
}

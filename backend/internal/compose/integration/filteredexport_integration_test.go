// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// First-class filtered export (B-E15.13, features/10 §3): the headline
// security property is that a filtered export is BOTH row-scoped AND
// predicate-filtered through the one engine — the exported slice is exactly
// (caller-visible ∧ predicate-matching). This suite pins that intersection,
// the open-format validity of both CSV and JSON, the export operation's
// audit row, an honest empty result, and the 422 on an out-of-vocabulary
// predicate.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// filteredDealFixture seeds three deals that separate the two exclusion
// axes: one owned-and-matching (kept), one owned-but-not-matching (dropped
// by the predicate), and one matching-but-owned-by-the-other-team (dropped
// by row scope). forecast_category is the predicate field — independent of
// owner_id, so the predicate filter and the scope filter cannot be
// conflated.
type filteredDealFixture struct {
	matchOwn   ids.UUID // rep1, forecast 'commit' — visible AND matches
	missOwn    ids.UUID // rep1, forecast 'omitted' — visible but does not match
	matchOther ids.UUID // rep3, forecast 'commit' — matches but invisible to rep1
}

func (e *searchEnv) seedFilteredDeals(t *testing.T) filteredDealFixture {
	t.Helper()
	pipelineID := e.seed(t, `INSERT INTO pipeline (id, workspace_id, name, is_default, position) VALUES ($1, $2, 'Sales', true, 0)`)
	stageID := e.seed(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, $3, 'Qualify', 0, 'open', 10)`, pipelineID)

	deal := func(owner ids.UUID, name, forecast string) ids.UUID {
		return e.seed(t, `INSERT INTO deal (id, workspace_id, owner_id, name, pipeline_id, stage_id, forecast_category, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'manual', 'human:x')`, owner, name, pipelineID, stageID, forecast)
	}
	return filteredDealFixture{
		matchOwn:   deal(e.Rep1, "Match Own", "commit"),
		missOwn:    deal(e.Rep1, "Miss Own", "omitted"),
		matchOther: deal(e.Rep3, "Match Other", "commit"),
	}
}

// commitDeals is the predicate the suite exports: forecast_category = commit.
func commitDeals() storekit.Predicate {
	return storekit.Predicate{Field: "forecast_category", Op: "eq", Value: "commit"}
}

// TestFilteredExportIsScopedAndFiltered is the pinned intersection: a
// team-scoped caller's filtered export contains exactly the rows that are
// both visible to them and match the predicate — excluding invisible rows
// AND non-matching rows.
func TestFilteredExportIsScopedAndFiltered(t *testing.T) {
	e := setupSearch(t)
	f := e.seedFilteredDeals(t)

	result, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		e.exportRep(e.Rep1, e.Team1), "deal", commitDeals(), "csv")
	if err != nil {
		t.Fatalf("filtered export: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("row count = %d, want 1 (only the visible matching deal)", result.RowCount)
	}

	gotIDs := csvColumn(t, result.Body, "id")
	set := map[string]bool{}
	for _, id := range gotIDs {
		set[id] = true
	}
	if !set[f.matchOwn.String()] {
		t.Fatalf("export dropped the caller's own matching deal %s: got %v", f.matchOwn, gotIDs)
	}
	if set[f.missOwn.String()] {
		t.Fatalf("export LEAKED a non-matching deal %s (predicate not applied): got %v", f.missOwn, gotIDs)
	}
	if set[f.matchOther.String()] {
		t.Fatalf("export LEAKED an invisible deal %s (row scope not applied): got %v", f.matchOther, gotIDs)
	}
}

// TestFilteredExportOpenFormatsAndAudit proves both open formats are valid
// and carry the same slice, and that the export operation writes one
// audit_log row describing what slice was exported.
func TestFilteredExportOpenFormatsAndAudit(t *testing.T) {
	e := setupSearch(t)
	e.seedFilteredDeals(t)
	writer := compose.NewFilteredExportWriter(e.Pool)

	// CSV parses and holds the one matching deal for the admin (row_scope=all
	// still narrowed to the predicate: only 'commit' deals, both teams).
	csvResult, err := writer.WriteFiltered(e.exportAdmin(), "deal", commitDeals(), "csv")
	if err != nil {
		t.Fatalf("csv export: %v", err)
	}
	if got := len(csvColumn(t, csvResult.Body, "id")); got != 2 {
		t.Fatalf("csv rows = %d, want 2 (both teams' commit deals)", got)
	}

	// JSON validates, self-describes its format, and carries the same rows.
	jsonResult, err := writer.WriteFiltered(e.exportAdmin(), "deal", commitDeals(), "json")
	if err != nil {
		t.Fatalf("json export: %v", err)
	}
	var doc struct {
		Format   string           `json:"format"`
		Object   string           `json:"object"`
		RowCount int              `json:"row_count"`
		Rows     []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(jsonResult.Body, &doc); err != nil {
		t.Fatalf("json export is not valid JSON: %v", err)
	}
	if doc.Format == "" || doc.Object != "deal" || doc.RowCount != 2 || len(doc.Rows) != 2 {
		t.Fatalf("json export shape wrong: %+v", doc)
	}
	// A custom/real column rides along: forecast_category is present and is
	// exactly the value the predicate selected on.
	for _, row := range doc.Rows {
		if row["forecast_category"] != "commit" {
			t.Fatalf("exported a row outside the predicate: %v", row["forecast_category"])
		}
	}

	// The export operation itself is logged: one 'export' row in system_log
	// (a bulk export mutates no record — it is a non-entity operational
	// event) recording the exported table, format, and row count of the slice.
	action, detail := lastSystemLog(t, e, "export")
	if action != "export" || detail["table"] != "deal" {
		t.Fatalf("system_log row = (%s, table=%v), want (export, deal)", action, detail["table"])
	}
	if detail["format"] != "csv" && detail["format"] != "json" {
		t.Fatalf("system_log row omits the export format: %v", detail)
	}
	if detail["row_count"] == nil {
		t.Fatalf("system_log row omits the exported slice size: %v", detail)
	}
}

// TestFilteredExportEmptyResultIsHonest: a predicate that matches nothing
// yields a valid CSV with only the header row, not an error.
func TestFilteredExportEmptyResultIsHonest(t *testing.T) {
	e := setupSearch(t)
	e.seedFilteredDeals(t)

	result, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		e.exportAdmin(), "deal",
		storekit.Predicate{Field: "forecast_category", Op: "eq", Value: "best_case"}, "csv")
	if err != nil {
		t.Fatalf("empty-result export errored: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("row count = %d, want 0", result.RowCount)
	}
	records, err := csv.NewReader(bytes.NewReader(result.Body)).ReadAll()
	if err != nil {
		t.Fatalf("empty export is not valid CSV: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("empty export should be a header row only, got %d rows", len(records))
	}
}

// TestFilteredExportRejectsOutOfVocabularyPredicate: a field outside the
// resource's §13.5 allow-list is a PredicateError the transport maps to
// 422 — the filter can never reach an arbitrary column.
func TestFilteredExportRejectsOutOfVocabularyPredicate(t *testing.T) {
	e := setupSearch(t)
	e.seedFilteredDeals(t)

	_, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		e.exportAdmin(), "deal",
		storekit.Predicate{Field: "amount_minor", Op: "eq", Value: float64(1)}, "csv")
	var pred *storekit.PredicateError
	if !errors.As(err, &pred) {
		t.Fatalf("out-of-vocabulary field → %v, want a PredicateError", err)
	}
	if pred.Code != storekit.CodeFilterFieldNotAllowed {
		t.Fatalf("code = %q, want %q", pred.Code, storekit.CodeFilterFieldNotAllowed)
	}
}

// lastSystemLog reads the most recent system_log row for an action inside
// the workspace-bound GUC (FORCE RLS applies even to the table owner), so the
// suite can assert the export was recorded.
func lastSystemLog(t *testing.T, e *searchEnv, action string) (gotAction string, detail map[string]any) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors read-only probe; the rollback is the designed close of a SELECT-only tx
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.WS.String()); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	var detailRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT action, detail FROM system_log WHERE action = $1 ORDER BY occurred_at DESC LIMIT 1`,
		action).Scan(&gotAction, &detailRaw)
	if err != nil {
		t.Fatalf("reading system_log row: %v", err)
	}
	if err := json.Unmarshal(detailRaw, &detail); err != nil {
		t.Fatalf("system_log detail is not JSON: %v", err)
	}
	return gotAction, detail
}

// TestFilteredExportHTTPEndToEnd drives the endpoint over the wire: a valid
// filtered export returns a CSV download, and an out-of-vocabulary filter
// answers 422 — proving the transport wiring and error mapping.
func TestFilteredExportHTTPEndToEnd(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email": "ada@example.com", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}

	// A valid filter with no matching rows still returns a CSV (header row):
	// the endpoint is wired and the open format is honest on an empty slice.
	body, err := json.Marshal(anyMap{
		"object": "deal",
		"filter": anyMap{"field": "forecast_category", "op": "eq", "value": "commit"},
		"format": "csv",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", e.ts.URL+"/v1/exports", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Slug", e.slug)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("export request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export → %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/csv", ct)
	}
	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("response is not valid CSV: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("empty slice should be a header row only, got %d rows", len(records))
	}

	// An out-of-vocabulary filter field is a 422, not a 500 or a silent dump.
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "POST", "/v1/exports", anyMap{
		"object": "deal",
		"filter": anyMap{"field": "amount_minor", "op": "eq", "value": 1},
		"format": "csv",
	}, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Fatalf("out-of-vocabulary filter → %d, want 422", status)
	}
}

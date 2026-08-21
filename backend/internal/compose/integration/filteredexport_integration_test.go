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
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dealEngine resolves the deal vocabulary the same way the export handler
// does — WriteFiltered now takes the resolved engine rather than looking a
// resource string up itself, so a caller that drives the writer directly
// (this suite, bypassing the HTTP handler) resolves it through the same
// production constructor (compose.NewCollectionsStore) wireExportSurface
// wires the handler with, rather than a hand-built, catalogue-less store
// that would leave this lane never exercising the real wiring at all.
func dealEngine(ctx context.Context, t *testing.T, e *SearchEnv) storekit.Query {
	t.Helper()
	engine, ok, err := compose.NewCollectionsStore(e.Pool).SegmentEngine(ctx, "deal")
	if err != nil {
		t.Fatalf("resolve deal engine: %v", err)
	}
	if !ok {
		t.Fatalf("deal has no segment engine")
	}
	return engine
}

// filteredDealFixture seeds three deals: two of rep1's, of which one matches
// the predicate, and one matching deal of the other team's. A deal is
// readable by every seat, so every caller with deal.read sees two matches.
type filteredDealFixture struct {
	matchOwn   ids.UUID // rep1, forecast 'commit' — visible AND matches
	missOwn    ids.UUID // rep1, forecast 'omitted' — visible but does not match
	matchOther ids.UUID // rep3, forecast 'commit' — the other team's matching deal
}

func (e *SearchEnv) seedFilteredDeals(t *testing.T) filteredDealFixture {
	t.Helper()
	pipelineID := e.SeedID(t, `INSERT INTO pipeline (id, name, is_default, position) VALUES ($1, 'Sales', true, 0)`)
	stageID := e.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, 'Qualify', 0, 'open', 10)`, pipelineID)

	deal := func(owner ids.UUID, name, forecast string) ids.UUID {
		return e.SeedID(t, `INSERT INTO deal (id, owner_id, name, pipeline_id, stage_id, forecast_category, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, 'manual', 'human:x')`, owner, name, pipelineID, stageID, forecast)
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
// caller's filtered export contains exactly the rows that are both visible to
// them and match the predicate — excluding invisible rows AND non-matching
// rows. The specimen is an organization: every shareable record type is read
// by every seat (platform/auth tableclass.go), so capture privacy is the one
// narrowing left that can show the visibility half of the intersection, and
// an unpromoted capture belongs to its own owner alone.
func TestFilteredExportIsScopedAndFiltered(t *testing.T) {
	e := SetupSearch(t)
	company := func(owner ids.UUID, name, industry, visibility string) ids.UUID {
		return e.SeedID(t, `INSERT INTO organization (id, display_name, owner_id, industry, visibility, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 'manual', 'human:x')`, name, owner, industry, visibility)
	}
	matchOwn := company(e.Rep1, "Match Own", "pharma", "workspace")  // visible AND matches
	missOwn := company(e.Rep1, "Miss Own", "logistics", "workspace") // visible but does not match
	// Matches, but it is an unpromoted capture of Rep3's: only Rep3 reads it.
	matchOther := company(e.Rep3, "Match Other", "pharma", "owner")

	ctx := e.orgReader(&e.Rep1, &e.Team1, principal.RowScopeTeam)
	engine, ok, err := compose.NewCollectionsStore(e.Pool).SegmentEngine(ctx, "organization")
	if err != nil || !ok {
		t.Fatalf("resolve organization engine: ok=%v err=%v", ok, err)
	}
	result, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		ctx, engine, storekit.Predicate{Field: "industry", Op: "eq", Value: "pharma"}, "csv",
	)
	if err != nil {
		t.Fatalf("filtered export: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("row count = %d, want 1 (only the visible matching company)", result.RowCount)
	}

	gotIDs := CSVColumn(t, result.Body, "id")
	set := map[string]bool{}
	for _, id := range gotIDs {
		set[id] = true
	}
	if !set[matchOwn.String()] {
		t.Fatalf("export dropped the caller's own matching company %s: got %v", matchOwn, gotIDs)
	}
	if set[missOwn.String()] {
		t.Fatalf("export LEAKED a non-matching company %s (predicate not applied): got %v", missOwn, gotIDs)
	}
	if set[matchOther.String()] {
		t.Fatalf("export LEAKED a capture-private company %s (visibility not applied): got %v", matchOther, gotIDs)
	}
}

// TestFilteredExportOpenFormatsAndAudit proves both open formats are valid
// and carry the same slice, and that the export operation writes one
// audit_log row describing what slice was exported.
func TestFilteredExportOpenFormatsAndAudit(t *testing.T) {
	e := SetupSearch(t)
	e.seedFilteredDeals(t)
	writer := compose.NewFilteredExportWriter(e.Pool)
	ctx := e.exportAdmin()
	engine := dealEngine(ctx, t, e)

	// CSV parses and holds the one matching deal for the admin (row_scope=all
	// still narrowed to the predicate: only 'commit' deals, both teams).
	csvResult, err := writer.WriteFiltered(ctx, engine, commitDeals(), "csv")
	if err != nil {
		t.Fatalf("csv export: %v", err)
	}
	if got := len(CSVColumn(t, csvResult.Body, "id")); got != 2 {
		t.Fatalf("csv rows = %d, want 2 (both teams' commit deals)", got)
	}

	// JSON validates, self-describes its format, and carries the same rows.
	jsonResult, err := writer.WriteFiltered(ctx, engine, commitDeals(), "json")
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
	e := SetupSearch(t)
	e.seedFilteredDeals(t)

	ctx := e.exportAdmin()
	result, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		ctx, dealEngine(ctx, t, e),
		storekit.Predicate{Field: "forecast_category", Op: "eq", Value: "best_case"}, "csv",
	)
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
	e := SetupSearch(t)
	e.seedFilteredDeals(t)

	ctx := e.exportAdmin()
	_, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		ctx, dealEngine(ctx, t, e),
		storekit.Predicate{Field: "amount_minor", Op: "eq", Value: float64(1)}, "csv",
	)
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
func lastSystemLog(t *testing.T, e *SearchEnv, action string) (gotAction string, detail map[string]any) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.Owner.Begin(ctx)
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
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	if status := e.Call(t, "POST", "/v1/auth/login", apptest.AnyMap{
		"email": "ada@example.com", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}

	// A valid filter with no matching rows still returns a CSV (header row):
	// the endpoint is wired and the open format is honest on an empty slice.
	body, err := json.Marshal(apptest.AnyMap{
		"object": "deal",
		"filter": apptest.AnyMap{"field": "forecast_category", "op": "eq", "value": "commit"},
		"format": "csv",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", e.TS.URL+"/v1/exports", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Slug", e.Slug)
	resp, err := e.Client.Do(req)
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
	if status := e.Call(t, "POST", "/v1/exports", apptest.AnyMap{
		"object": "deal",
		"filter": apptest.AnyMap{"field": "amount_minor", "op": "eq", "value": 1},
		"format": "csv",
	}, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Fatalf("out-of-vocabulary filter → %d, want 422", status)
	}
}

// Export by SAVED VIEW and by LIST — the two sources that resolve their
// predicate from stored state rather than from the request body.
//
// Both were unexercised: nothing reached SavedViewFilterSource or
// ListFilterSource at all, so the guarantee #693 is about — one vocabulary
// across what a filter may say, what it selects, and what an export contains —
// held for the object path and was untested for the other two ways in.
//
// Each source's refusals are here too, because they are the interesting half:
// a static list has explicit members rather than a filter, and a view may carry
// no filter state at all. Both name their own wire field and answer 422.
func TestFilteredExportFromASavedViewAndFromAList(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	if status := e.Call(t, "POST", "/v1/auth/login", apptest.AnyMap{
		"email": "ada@example.com", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}

	commitFilter := apptest.AnyMap{"field": "forecast_category", "op": "eq", "value": "commit"}

	var view struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/views", apptest.AnyMap{
		"resource": "deals", "name": "Commit deals",
		"query": apptest.AnyMap{"filter": commitFilter},
	}, nil, &view); status != http.StatusCreated {
		t.Fatalf("create saved view → %d, want 201", status)
	}

	var dynamic struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/lists", apptest.AnyMap{
		"name": "Commit segment", "entity_type": "deal", "list_type": "dynamic",
		"definition": commitFilter,
	}, nil, &dynamic); status != http.StatusCreated {
		t.Fatalf("create dynamic list → %d, want 201", status)
	}

	var static struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/lists", apptest.AnyMap{
		"name": "Hand-picked deals", "entity_type": "deal", "list_type": "static",
	}, nil, &static); status != http.StatusCreated {
		t.Fatalf("create static list → %d, want 201", status)
	}

	var viewless struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/views", apptest.AnyMap{
		"resource": "deals", "name": "Columns only",
		"query": apptest.AnyMap{"columns": []string{"name"}},
	}, nil, &viewless); status != http.StatusCreated {
		t.Fatalf("create filterless view → %d, want 201", status)
	}

	// A filter of explicit null is how a client spells "cleared". The write
	// surface accepts it as such — and the two surfaces have to AGREE about it,
	// or a view the create path called filterless is called malformed at export.
	var cleared struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/views", apptest.AnyMap{
		"resource": "deals", "name": "Cleared filter",
		"query": apptest.AnyMap{"columns": []string{"name"}, "filter": nil},
	}, nil, &cleared); status != http.StatusCreated {
		t.Fatalf("create view with a null filter → %d, want 201 — the write surface reads null as cleared", status)
	}

	// Both stored sources export. No deal matches, so the honest answer is a
	// header row — the same shape the object path gives for an empty slice,
	// which is what "one engine" has to mean here.
	for _, c := range []struct {
		name string
		body apptest.AnyMap
	}{
		{"by view_id", apptest.AnyMap{"view_id": view.ID, "format": "csv"}},
		{"by list_id", apptest.AnyMap{"list_id": dynamic.ID, "format": "csv"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rows := exportCSV(t, e, c.body)
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want a header row only", len(rows))
			}
		})
	}

	// The refusals, each naming the field the caller actually sent.
	for _, c := range []struct {
		name  string
		body  apptest.AnyMap
		field string
	}{
		{"a static list has members, not a filter", apptest.AnyMap{"list_id": static.ID, "format": "csv"}, "list_id"},
		{"a view carrying no filter state", apptest.AnyMap{"view_id": viewless.ID, "format": "csv"}, "view_id"},
		{"a view whose filter is explicitly null", apptest.AnyMap{"view_id": cleared.ID, "format": "csv"}, "view_id"},
		{"a view_id that is not a uuid", apptest.AnyMap{"view_id": "not-a-uuid", "format": "csv"}, "view_id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The per-field breakdown rides `details.errors` (httperr's
			// fieldDetails), not the problem's top level.
			var problem struct {
				Details struct {
					Errors []struct {
						Field string `json:"field"`
					} `json:"errors"`
				} `json:"details"`
			}
			if status := e.Call(t, "POST", "/v1/exports", c.body, nil, &problem); status != http.StatusUnprocessableEntity {
				t.Fatalf("→ %d, want 422", status)
			}
			if got := problem.Details.Errors; len(got) != 1 || got[0].Field != c.field {
				t.Errorf("field errors = %v, want exactly one naming %q", got, c.field)
			}
		})
	}
}

// exportCSV posts an export and parses the CSV body, failing the test on any
// non-200 or unparseable response.
func exportCSV(t *testing.T, e *apptest.AppEnv, body apptest.AnyMap) [][]string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", e.TS.URL+"/v1/exports", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Slug", e.Slug)
	resp, err := e.Client.Do(req)
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
	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("response is not valid CSV: %v", err)
	}
	return records
}

// A field mask follows a deal into its export: the CSV row of another team's
// deal carries an EMPTY amount for a rep whose role masks it, while their own
// row keeps the figure — the same per-row decision the deal list makes,
// proven here against the statement the export actually runs.
func TestFilteredExportWithholdsAMaskedAmount(t *testing.T) {
	e := SetupSearch(t)
	f := e.seedFilteredDeals(t)
	for id, amount := range map[ids.UUID]int64{f.matchOwn: 100000, f.matchOther: 250000} {
		if _, err := e.Owner.Exec(context.Background(), `UPDATE deal SET amount_minor = $2, currency = 'EUR' WHERE id = $1`, id, amount); err != nil {
			t.Fatal(err)
		}
	}

	base := e.AsTeamRep(e.Rep1, e.Team1)
	actor, _ := principal.Actor(base)
	actor.Permissions.Objects["deal"] = principal.ObjectGrant{Read: true, Update: true}
	actor.Permissions.FieldMasks = []principal.FieldMask{
		{Object: "deal", Field: "amount_minor", Condition: principal.MaskOutsideWriteAuthority},
	}
	ctx := principal.WithActor(base, actor)

	result, err := compose.NewFilteredExportWriter(e.Pool).WriteFiltered(
		ctx, dealEngine(ctx, t, e), commitDeals(), "csv",
	)
	if err != nil {
		t.Fatalf("filtered export: %v", err)
	}
	rowIDs := CSVColumn(t, result.Body, "id")
	amounts := CSVColumn(t, result.Body, "amount_minor")
	if len(rowIDs) != 2 {
		t.Fatalf("exported rows = %v, want both matching deals (a deal is readable by every seat)", rowIDs)
	}
	for i, id := range rowIDs {
		switch id {
		case f.matchOwn.String():
			if amounts[i] != "100000" {
				t.Errorf("the rep's own amount = %q, want 100000", amounts[i])
			}
		case f.matchOther.String():
			if amounts[i] != "" {
				t.Errorf("the other team's amount = %q, want empty — the export printed a masked value", amounts[i])
			}
		default:
			t.Errorf("unexpected row %s", id)
		}
	}
}

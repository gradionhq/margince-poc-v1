// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The report engine + the context graph (B-EP05.19/.20, interfaces.md
// §3): prebuilt reports over HTTP with vocabulary enforcement, the
// seam-level ad-hoc plan with row scope, the run_report tool through
// the governed registry, and AssembleContext's fixed-depth walk with
// per-hop visibility.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// seedDealFixtures plants a pipeline, one open stage, an organization
// and n open deals owned by the given user (nil = ownerless).
func (e *searchEnv) seedDealFixtures(t *testing.T, n int, owner *ids.UUID) (orgID ids.UUID) {
	t.Helper()
	pipelineID := e.seed(t, `INSERT INTO pipeline (id, workspace_id, name, is_default, position) VALUES ($1, $2, 'Sales', true, 0)`)
	stageID := e.seed(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, $3, 'Qualify', 0, 'open', 10)`, pipelineID)
	orgID = e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Report Org', 'manual', 'human:x')`)
	for i := 0; i < n; i++ {
		e.seed(t, fmt.Sprintf(`INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, organization_id, owner_id, amount_minor, currency, source, captured_by)
			VALUES ($1, $2, 'Deal %d', $3, $4, $5, $6, 100000, 'EUR', 'manual', 'human:x')`, i),
			pipelineID, stageID, orgID, owner)
	}
	return orgID
}

func TestAdHocReportPlanCountsUnderRowScope(t *testing.T) {
	e := setupSearch(t)
	e.seedDealFixtures(t, 3, &e.Rep3) // owned by team2's rep
	provider := compose.NewProvider(e.Pool)

	// row_scope=all counts all three.
	res, err := provider.RunReport(e.Admin(), datasource.ReportPlan{
		Entity: datasource.EntityDeal, GroupBy: []string{"status"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][1]) != "3" {
		t.Fatalf("ad-hoc plan rows = %+v, want one open row counting 3", res.Rows)
	}

	// A team1 rep sees none of team2's deals — aggregates cannot leak
	// what the lists hide.
	res, err = provider.RunReport(e.asTeamRep(e.Rep1, e.Team1), datasource.ReportPlan{
		Entity: datasource.EntityDeal, GroupBy: []string{"status"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("row scope leaked into the aggregate: %+v", res.Rows)
	}
}

func TestSchemaIntrospectionServesDescriptors(t *testing.T) {
	e := setupSearch(t)
	provider := compose.NewProvider(e.Pool)
	objects, err := provider.ListObjects(context.Background())
	if err != nil || len(objects) != 5 {
		t.Fatalf("ListObjects → %d objects, err %v", len(objects), err)
	}
	fields, err := provider.ListFields(context.Background(), datasource.EntityDeal)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range fields {
		names[f.Name] = true
	}
	if !names["amount_minor"] || !names["status"] {
		t.Fatalf("deal descriptor incomplete: %+v", fields)
	}
	if _, err := provider.ListFields(context.Background(), datasource.EntityType("nonsense")); err == nil {
		t.Fatal("unknown entity must refuse, not answer empty")
	}
}

func TestPrebuiltReportOverHTTPAndVocabulary(t *testing.T) {
	e := setup(t)
	e.slug = "reports-e2e"
	bootstrapWorkspaceSession(t, e, "Reports E2E", "rep@fable.test")

	var org struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/organizations", anyMap{"display_name": "Acme"}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create org → %d", status)
	}
	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID       string `json:"id"`
				Semantic string `json:"semantic"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK || len(pipelines.Data) == 0 {
		t.Fatalf("list pipelines → %d %+v", status, pipelines)
	}
	stageID := ""
	for _, s := range pipelines.Data[0].Stages {
		if s.Semantic == "open" {
			stageID = s.ID
			break
		}
	}
	if stageID == "" {
		t.Fatalf("no open stage in the seeded pipeline: %+v", pipelines)
	}
	for i := 0; i < 2; i++ {
		if status := e.call(t, "POST", "/v1/deals", anyMap{
			"name": fmt.Sprintf("Acme Deal %d", i), "pipeline_id": pipelines.Data[0].ID,
			"stage_id": stageID, "organization_id": org.ID,
		}, nil, nil); status != http.StatusCreated {
			t.Fatalf("create deal → %d", status)
		}
	}

	var result struct {
		Report  string           `json:"report"`
		Columns []string         `json:"columns"`
		Rows    []map[string]any `json:"rows"`
	}
	if status := e.call(t, "POST", "/v1/reports/open-deals-per-company", nil, nil, &result); status != http.StatusOK {
		t.Fatalf("runReport → %d", status)
	}
	if result.Report != "open-deals-per-company" || len(result.Rows) != 1 {
		t.Fatalf("report result: %+v", result)
	}
	if fmt.Sprint(result.Rows[0]["open_deals"]) != "2" || result.Rows[0]["organization_id"] != org.ID {
		t.Fatalf("aggregate row wrong: %+v", result.Rows[0])
	}

	// Out-of-vocabulary group_by → 422 report_field_not_allowed.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/reports/open-deals-per-company",
		anyMap{"group_by": []string{"captured_by"}}, nil, &problem)
	if status != 422 || problem.Code != "report_field_not_allowed" {
		t.Fatalf("OOV field → %d %q, want 422 report_field_not_allowed", status, problem.Code)
	}
	// Unknown report keys are absent.
	if status := e.call(t, "POST", "/v1/reports/definitely-not-a-report", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown report → %d, want 404", status)
	}
}

func TestRunReportToolThroughGovernedRegistry(t *testing.T) {
	e := setupSearch(t)
	e.seedDealFixtures(t, 2, nil)
	registry := compose.NewRegistry(e.Pool)
	out, err := registry.Invoke(e.Admin(), "run_report", []byte(`{"report":"deals-by-stage"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"deals":2`) && !strings.Contains(string(out), `"deals": 2`) {
		t.Fatalf("tool result lacks the aggregate: %s", out)
	}
}

func TestAssembleContextFixedDepthWalk(t *testing.T) {
	e := setupSearch(t)
	orgID := e.seedDealFixtures(t, 1, nil)
	var dealID ids.UUID
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM deal LIMIT 1`).Scan(&dealID); err != nil {
		t.Fatal(err)
	}
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Graph Contact', 'manual', 'human:x')`)
	noteID := e.seed(t, `INSERT INTO activity (id, workspace_id, kind, subject, source, captured_by) VALUES ($1, $2, 'note', 'Kickoff call', 'manual', 'human:x')`)
	taskID := e.seed(t, `INSERT INTO activity (id, workspace_id, kind, subject, is_done, source, captured_by) VALUES ($1, $2, 'task', 'Send offer', false, 'manual', 'human:x')`)
	for _, activityID := range []ids.UUID{noteID, taskID} {
		e.seed(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, deal_id) VALUES ($1, $2, $3, 'deal', $4)`, activityID, dealID)
		e.seed(t, `INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, person_id) VALUES ($1, $2, $3, 'person', $4)`, activityID, personID)
	}

	retriever := search.NewRetriever(e.store, ai.NewFakeClient())
	assembled, err := retriever.AssembleContext(e.Admin(),
		datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID}, retrieval.AssembleOptions{MaxItems: 5})
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string][]retrieval.Item{}
	for _, s := range assembled.Sections {
		sections[s.Name] = s.Items
	}
	if len(sections["profile"]) != 1 {
		t.Fatalf("no profile section: %+v", assembled.Sections)
	}
	if len(sections["recent_touches"]) != 1 || sections["recent_touches"][0].Summary != "Kickoff call" {
		t.Fatalf("recent touches wrong: %+v", sections["recent_touches"])
	}
	if len(sections["open_tasks"]) != 1 || sections["open_tasks"][0].Summary != "Send offer" {
		t.Fatalf("open tasks wrong: %+v", sections["open_tasks"])
	}
	if len(sections["related_people"]) != 1 || sections["related_people"][0].Ref.ID != personID {
		t.Fatalf("hop-2 people wrong: %+v", sections["related_people"])
	}
	if len(sections["related_organizations"]) != 0 {
		// The org is linked to the deal via FK, not via activity_link —
		// the fixed-depth walk only follows conversation links.
		t.Logf("note: org appears only when linked through an activity: %+v", sections["related_organizations"])
	}
	_ = orgID

	// An anchor outside the caller's row scope assembles nothing.
	foreignDeal := e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, owner_id, source, captured_by)
		SELECT $1, $2, 'Foreign Deal', pipeline_id, stage_id, $3, 'manual', 'human:x' FROM deal LIMIT 1`, e.Rep3)
	if _, err := retriever.AssembleContext(e.asTeamRep(e.Rep1, e.Team1),
		datasource.EntityRef{Type: datasource.EntityDeal, ID: foreignDeal}, retrieval.AssembleOptions{}); err == nil {
		t.Fatal("foreign anchor must be absent, not assembled")
	}
}

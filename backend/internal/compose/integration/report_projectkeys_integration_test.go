// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project keys of the prebuilt report catalog, and the project_id filter
// the deal and activity reports gained, over a real migrated Postgres and
// through the governed run_report tool. Every row is seeded through the real
// writers — CreateProject, CreateDeal with a ProjectID, LogActivity with a
// project link, AdvanceProjectPhase — so the numbers come from what the
// product writes, not from a fixture's idea of it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// reportRow is one aggregate row as the tool hands it over.
type reportRow map[string]any

// runPrebuiltReport runs one catalog key through the governed run_report tool
// and returns its rows.
func runPrebuiltReport(ctx context.Context, t *testing.T, e *Env, report, plan string) []reportRow {
	t.Helper()
	rows, err := tryPrebuiltReport(ctx, e, report, plan)
	if err != nil {
		t.Fatalf("run_report %s %s: %v", report, plan, err)
	}
	return rows
}

func tryPrebuiltReport(ctx context.Context, e *Env, report, plan string) ([]reportRow, error) {
	registry := compose.NewRegistry(e.Pool, compose.SendPath{})
	// The plan arguments ride beside `report` in the tool's one object.
	args := `{"report":"` + report + `"}`
	if plan != "" {
		args = `{"report":"` + report + `",` + plan[1:]
	}
	out, err := registry.Invoke(ctx, "run_report", []byte(args))
	if err != nil {
		return nil, err
	}
	// The tool answers inside the result envelope; the report rides as data.
	var envelope struct {
		Data struct {
			Rows []reportRow `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		return nil, fmt.Errorf("decoding the tool result %s: %w", out, err)
	}
	return envelope.Data.Rows, nil
}

// cell reads one column as the number it carries, rendered through fmt so an
// int64 and a float64 compare alike.
func cell(row reportRow, column string) string { return fmt.Sprint(row[column]) }

// fileActivity logs one activity of the given kind at `when`, linked to the
// project when one is named, through the real activities writer.
func fileActivity(ctx context.Context, t *testing.T, e *Env, kind string, when time.Time, project *ids.ProjectID) {
	t.Helper()
	subject := kind + " on the project"
	in := activities.LogActivityInput{Kind: kind, Subject: &subject, OccurredAt: &when, Source: "manual"}
	if project != nil {
		in.Links = []activities.ActivityLinkInput{{EntityType: "project", EntityID: project.UUID}}
	}
	if _, _, err := e.Activities.LogActivity(ctx, in); err != nil {
		t.Fatalf("logging a %s: %v", kind, err)
	}
}

// fileTask files one open task under the project, due at `due`.
func fileTask(ctx context.Context, t *testing.T, e *Env, project ids.ProjectID, due time.Time) {
	t.Helper()
	subject := "promised"
	if _, _, err := e.Activities.LogActivity(ctx, activities.LogActivityInput{
		Kind: "task", Subject: &subject, DueAt: &due, Source: "manual",
		Links: []activities.ActivityLinkInput{{EntityType: "project", EntityID: project.UUID}},
	}); err != nil {
		t.Fatalf("filing a task: %v", err)
	}
}

func advanceProject(ctx context.Context, t *testing.T, e *Env, project ids.ProjectID, phase string) {
	t.Helper()
	if _, err := e.Deals.AdvanceProjectPhase(ctx, project, deals.AdvanceProjectPhaseInput{ToPhase: phase}); err != nil {
		t.Fatalf("moving the project to %s: %v", phase, err)
	}
}

// The activities report answers "which projects consumed the effort": the
// project_id filter admits exactly the activities filed under that project —
// an unfiled activity and one under another project are both excluded — and
// grouping by project_id buckets the unfiled ones under no project.
func TestActivitiesByKindFiltersAndGroupsByProject(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	org := e.SeedOrg(t, "Effort Co", nil)
	erp := seedProject(admin, t, e, "ERP replacement", strPtr("ERP-1"), org, nil)
	crm := seedProject(admin, t, e, "CRM rollout", nil, org, nil)
	when := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	fileActivity(admin, t, e, "meeting", when, &erp.ID)
	fileActivity(admin, t, e, "meeting", when.Add(time.Hour), &erp.ID)
	fileActivity(admin, t, e, "call", when, &crm.ID)
	fileActivity(admin, t, e, "note", when, nil)

	rows := runPrebuiltReport(admin, t, e, "activities-by-kind",
		`{"filters":{"project_id":"`+erp.ID.String()+`"}}`)
	if len(rows) != 1 || cell(rows[0], "kind") != "meeting" || cell(rows[0], "activities") != "2" {
		t.Fatalf("filtered to ERP: rows = %v, want one meeting row counting 2 (the CRM call and the unfiled note excluded)", rows)
	}

	byProject := map[string]string{}
	for _, row := range runPrebuiltReport(admin, t, e, "activities-by-kind", `{"group_by":["project_id","project"]}`) {
		byProject[cell(row, "project_id")+"|"+cell(row, "project")] = cell(row, "activities")
	}
	want := map[string]string{
		erp.ID.String() + "|ERP-1":       "2",
		crm.ID.String() + "|CRM rollout": "1",
		"<nil>|<nil>":                    "1",
	}
	if fmt.Sprint(byProject) != fmt.Sprint(want) {
		t.Fatalf("grouped by project = %v, want %v (key as the label when the project has one, name otherwise, NULL for unfiled)", byProject, want)
	}
}

// Filtering a deal report by project reads the project: a caller holding no
// project grant is refused (403) before the filter binds, and an id that names
// no project the caller can see answers the existence-hiding 404 — whatever
// deals the filter would or would not have matched.
func TestProjectFilterRunsTheProjectReadGate(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	org := e.SeedOrg(t, "Gated Co", nil)
	p := seedProject(admin, t, e, "Hidden work", nil, org, nil)

	noProjectGrant := principal.Permissions{
		Objects: map[string]principal.ObjectGrant{
			"deal": {Read: true}, "activity": {Read: true}, "pipeline": {Read: true},
		},
		RowScope: principal.RowScopeAll,
	}
	for _, report := range []string{"deals-by-stage", "activities-by-kind"} {
		_, err := tryPrebuiltReport(e.As(e.Rep1, nil, noProjectGrant), e, report,
			`{"filters":{"project_id":"`+p.ID.String()+`"}}`)
		if !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("%s filtered by project without project.read → %v, want ErrPermissionDenied", report, err)
		}
		_, err = tryPrebuiltReport(admin, e, report, `{"filters":{"project_id":"`+ids.NewV7().String()+`"}}`)
		if !errors.Is(err, apperrors.ErrNotFound) {
			t.Errorf("%s filtered by a project that does not exist → %v, want ErrNotFound", report, err)
		}
	}

	// The gate passed, the filter binds: only the project's deal is counted.
	pipeline, open, _ := DealFixture(t, e)
	orgID := orgIDOf(org)
	for _, name := range []string{"on the project", "elsewhere"} {
		in := deals.CreateDealInput{Name: name, PipelineID: pipeline, StageID: open, OrganizationID: &orgID, Source: "manual"}
		if name == "on the project" {
			in.ProjectID = &p.ID
		}
		if _, err := e.Deals.CreateDeal(admin, in); err != nil {
			t.Fatalf("create deal %q: %v", name, err)
		}
	}
	rows := runPrebuiltReport(admin, t, e, "deals-by-stage",
		`{"filters":{"project_id":"`+p.ID.String()+`"},"group_by":["status"]}`)
	if len(rows) != 1 || cell(rows[0], "deals") != "1" {
		t.Fatalf("deals-by-stage filtered by project = %v, want the project's one deal", rows)
	}
}

// projects-by-phase counts the projects on each rung and folds what their
// deals are worth in the base currency: the open side prices an open deal in
// the base currency at its amount, the won side reads the frozen base amount.
func TestProjectsByPhaseCountsAndFoldsDealValue(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	pipeline, open, won := DealFixture(t, e)
	org := e.SeedOrg(t, "Phase Co", nil)
	orgID := orgIDOf(org)
	pursued := seedProject(admin, t, e, "Pursued", nil, org, nil)
	advanceProject(admin, t, e, pursued.ID, deals.PhasePursuing)
	seedProject(admin, t, e, "Idea", nil, org, nil)

	amount := int64(250000)
	eur := "EUR"
	openDeal, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Open", PipelineID: pipeline, StageID: open, OrganizationID: &orgID,
		ProjectID: &pursued.ID, AmountMinor: &amount, Currency: &eur, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create the open deal: %v", err)
	}
	wonAmount := int64(100000)
	wonDeal, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
		Name: "Won", PipelineID: pipeline, StageID: open, OrganizationID: &orgID,
		ProjectID: &pursued.ID, AmountMinor: &wonAmount, Currency: &eur, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create the won deal: %v", err)
	}
	if _, err := e.Deals.AdvanceDeal(admin, ids.From[ids.DealKind](ids.UUID(wonDeal.Id)), wonInput(won)); err != nil {
		t.Fatalf("win the deal: %v", err)
	}
	_ = openDeal

	byPhase := map[string]reportRow{}
	for _, row := range runPrebuiltReport(admin, t, e, "projects-by-phase", "") {
		byPhase[cell(row, "phase")] = row
	}
	// Winning the deal moved the project to delivering (the close-won bridge).
	delivering := byPhase[deals.PhaseDelivering]
	if cell(delivering, "projects") != "1" || cell(delivering, "open_deal_value_minor") != "250000" ||
		cell(delivering, "won_deal_value_minor") != "100000" {
		t.Fatalf("delivering row = %v, want 1 project, open 250000, won 100000", delivering)
	}
	initiative := byPhase[deals.PhaseInitiative]
	if cell(initiative, "projects") != "1" || cell(initiative, "open_deal_value_minor") != "0" {
		t.Fatalf("initiative row = %v, want 1 project worth nothing yet", initiative)
	}
}

// project-commitments lists each project's open and overdue tasks, the most
// overdue first; a task with no past due date is open but not overdue.
func TestProjectCommitmentsCountsOpenAndOverdueFirst(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	org := e.SeedOrg(t, "Promise Co", nil)
	calm := seedProject(admin, t, e, "Calm", nil, org, nil)
	late := seedProject(admin, t, e, "Late", nil, org, nil)
	past := time.Now().UTC().Add(-48 * time.Hour)
	future := time.Now().UTC().Add(72 * time.Hour)
	fileTask(admin, t, e, calm.ID, future)
	fileTask(admin, t, e, late.ID, past)
	fileTask(admin, t, e, late.ID, past)
	fileTask(admin, t, e, late.ID, future)

	rows := runPrebuiltReport(admin, t, e, "project-commitments", "")
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want one per project", rows)
	}
	if cell(rows[0], "name") != "Late" || cell(rows[0], "open_commitments") != "3" || cell(rows[0], "overdue_commitments") != "2" {
		t.Fatalf("first row = %v, want Late with 3 open and 2 overdue, listed first", rows[0])
	}
	if cell(rows[1], "name") != "Calm" || cell(rows[1], "open_commitments") != "1" || cell(rows[1], "overdue_commitments") != "0" {
		t.Fatalf("second row = %v, want Calm with 1 open and none overdue", rows[1])
	}
}

// projects-gone-quiet lists the projects in flight that nothing has been filed
// against for `days` days: the default window, a narrower one the caller
// sends, and a project not in flight excluded however long it has sat.
func TestProjectsGoneQuietHonoursTheDaysThreshold(t *testing.T) {
	e := Setup(t)
	admin := e.Admin()
	org := e.SeedOrg(t, "Quiet Co", nil)
	now := time.Now().UTC()
	stale := seedProject(admin, t, e, "Stale", nil, org, &e.Rep1)
	advanceProject(admin, t, e, stale.ID, deals.PhasePursuing)
	fileActivity(admin, t, e, "note", now.AddDate(0, 0, -40), &stale.ID)
	recent := seedProject(admin, t, e, "Recent", nil, org, nil)
	advanceProject(admin, t, e, recent.ID, deals.PhaseDelivering)
	fileActivity(admin, t, e, "note", now.AddDate(0, 0, -5), &recent.ID)
	idea := seedProject(admin, t, e, "Idea", nil, org, nil)
	fileActivity(admin, t, e, "note", now.AddDate(0, 0, -90), &idea.ID)

	rows := runPrebuiltReport(admin, t, e, "projects-gone-quiet", "")
	if len(rows) != 1 || cell(rows[0], "name") != "Stale" || cell(rows[0], "owner_id") != e.Rep1.String() {
		t.Fatalf("default window rows = %v, want Stale alone, with its owner", rows)
	}
	if rows[0]["last_activity_at"] == nil || rows[0]["quiet_since"] == nil {
		t.Fatalf("the quiet row carries no last_activity_at / quiet_since: %v", rows[0])
	}

	names := []string{}
	for _, row := range runPrebuiltReport(admin, t, e, "projects-gone-quiet", `{"filters":{"days":3}}`) {
		names = append(names, cell(row, "name"))
	}
	if fmt.Sprint(names) != "[Stale Recent]" {
		t.Fatalf("days=3 rows = %v, want Stale then Recent (quietest first; the initiative is not in flight)", names)
	}

	if _, err := tryPrebuiltReport(admin, e, "projects-gone-quiet", `{"filters":{"days":"soon"}}`); err == nil {
		t.Fatal("days=\"soon\" was accepted; a threshold takes a whole number")
	}
}

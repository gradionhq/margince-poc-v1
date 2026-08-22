// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// "Explain This Number" owes the same grants the number did: a drill-through
// handle that names a project dimension is refused to a seat without
// project.read, and a handle that names nothing of the sort serves that seat
// rows WITHOUT the project columns rather than disclosing them alongside.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func (e *forecastEnv) activityReader(objects ...string) context.Context {
	grants := map[string]principal.ObjectGrant{"activity": {Read: true}, "installation_settings": {Read: true}}
	for _, object := range objects {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + e.Rep1.String(), UserID: e.Rep1,
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeAll},
	})
}

func (e *forecastEnv) explainStatus(ctx context.Context, report, handleURL string) int {
	req := httptest.NewRequest(http.MethodGet, handleURL, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.handlers.ExplainReport(rec, req, report, crmcontracts.ExplainReportParams{})
	return rec.Code
}

func TestActivityDrillThroughTakesTheProjectGrant(t *testing.T) {
	e := setupForecast(t)
	org := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Drill Co', 'manual', 'human:x')`)
	project := e.seedID(t, `INSERT INTO project (id, name, organization_id, source, captured_by)
		VALUES ($1, 'Secret rollout', $2, 'manual', 'human:x')`, org)
	activity := e.seedID(t, `INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'kickoff', now() - interval '1 hour', 'manual', 'human:x')`)
	e.seedID(t, `INSERT INTO activity_link (id, activity_id, entity_type, project_id) VALUES ($1, $2, 'project', $3)`, activity, project)

	byProject := derivationURL("activities-by-kind", nil, []string{"project_id"}, nil,
		map[string]any{"project_id": project.String()})
	if status := e.explainStatus(e.activityReader(), "activities-by-kind", byProject); status != http.StatusForbidden {
		t.Fatalf("drill-through grouped by project without project.read → %d, want 403", status)
	}
	if status := e.explainStatus(e.activityReader("project"), "activities-by-kind", byProject); status != http.StatusOK {
		t.Fatalf("drill-through grouped by project with project.read → %d, want 200", status)
	}

	byKind := derivationURL("activities-by-kind", nil, []string{"kind"}, nil, map[string]any{"kind": "meeting"})
	rows := e.explainReport(e.activityReader(), t, "activities-by-kind", byKind)
	if rows.TotalRows != 1 {
		t.Fatalf("drill-through by kind = %d rows, want the one meeting", rows.TotalRows)
	}
	for _, row := range rows.Rows {
		for _, withheld := range []string{"project_id", "project"} {
			if _, present := row[withheld]; present {
				t.Errorf("a seat without project.read was served %s on the drill-through row: %v", withheld, row)
			}
		}
	}
}

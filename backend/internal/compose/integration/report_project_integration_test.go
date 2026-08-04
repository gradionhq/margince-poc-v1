// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// `project` is a full member of the record vocabulary, so both consumers of
// the schema descriptors must serve it: ListFields answers its field list,
// and an ad-hoc RunReport plan compiles over it and aggregates only the rows
// the caller may read.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// seedProjects plants an anchor company and n live projects in one phase,
// owned by the given user (nil = ownerless, i.e. workspace-shared).
func (e *searchEnv) seedProjects(t *testing.T, phase string, owner *ids.UUID, n int) (orgID ids.UUID) {
	t.Helper()
	orgID = e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by)
		VALUES ($1, $2, 'Project Org', 'manual', 'human:x')`)
	for i := 0; i < n; i++ {
		e.seed(t, `INSERT INTO project (id, workspace_id, name, organization_id, owner_id, phase, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, $6, 'manual', 'human:x')`,
			fmt.Sprintf("%s Rollout %d", phase, i), orgID, owner, phase)
	}
	return orgID
}

// projectReader mints a human holding project.read at the given row scope —
// the report path gates on the object grant before any row scope applies, and
// the shared searchReadGrants helper predates the project vocabulary.
func (e *searchEnv) projectReader(user *ids.UUID, team *ids.UUID, scope principal.RowScope) context.Context {
	actor := principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"project": {Read: true}},
			RowScope: scope,
		},
	}
	if user != nil {
		actor.ID = "human:" + user.String()
		actor.UserID = *user
	}
	if team != nil {
		actor.TeamIDs = []ids.UUID{*team}
	}
	return principal.WithActor(principal.WithWorkspaceID(context.Background(), e.WS), actor)
}

func TestListFieldsServesTheProjectDescriptor(t *testing.T) {
	e := setupSearch(t)
	provider := compose.NewProvider(e.Pool)

	fields, err := provider.ListFields(context.Background(), datasource.EntityProject)
	if err != nil {
		t.Fatalf("ListFields(project): %v", err)
	}
	names := map[string]bool{}
	for _, f := range fields {
		names[f.Name] = true
	}
	for _, want := range []string{"name", "key", "organization_id", "owner_id", "phase", "created_at"} {
		if !names[want] {
			t.Errorf("project descriptor omits %q: %+v", want, fields)
		}
	}
}

func TestAdHocProjectReportCountsUnderRowScope(t *testing.T) {
	e := setupSearch(t)
	orgID := e.seedProjects(t, "delivering", &e.Rep3, 2)
	provider := compose.NewProvider(e.Pool)

	// row_scope=all groups every project by phase.
	res, err := provider.RunReport(e.projectReader(nil, nil, principal.RowScopeAll), datasource.ReportPlan{
		Entity: datasource.EntityProject, GroupBy: []string{"phase"},
	})
	if err != nil {
		t.Fatalf("ad-hoc project plan: %v", err)
	}
	if len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][0]) != "delivering" || fmt.Sprint(res.Rows[0][1]) != "2" {
		t.Fatalf("ad-hoc project rows = %+v, want one 'delivering' row counting 2", res.Rows)
	}

	// A descriptor field may filter as well as group.
	res, err = provider.RunReport(e.projectReader(nil, nil, principal.RowScopeAll), datasource.ReportPlan{
		Entity: datasource.EntityProject, GroupBy: []string{"phase"},
		Filter: map[string]string{"organization_id": orgID.String()},
	})
	if err != nil {
		t.Fatalf("filtered project plan: %v", err)
	}
	if len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][1]) != "2" {
		t.Fatalf("filtered project rows = %+v, want the same 2 under the anchor company", res.Rows)
	}

	// A team1 rep owns none of team2's projects — the aggregate cannot count
	// what the lists hide.
	res, err = provider.RunReport(e.projectReader(&e.Rep1, &e.Team1, principal.RowScopeTeam), datasource.ReportPlan{
		Entity: datasource.EntityProject, GroupBy: []string{"phase"},
	})
	if err != nil {
		t.Fatalf("team-scoped project plan: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("row scope leaked into the project aggregate: %+v", res.Rows)
	}

	// The rep's own project is counted — the empty answer above is scope, not
	// a plan that never matches.
	e.seed(t, `INSERT INTO project (id, workspace_id, name, organization_id, owner_id, phase, source, captured_by)
		VALUES ($1, $2, 'Own Rollout', $3, $4, 'pursuing', 'manual', 'human:x')`, orgID, e.Rep1)
	res, err = provider.RunReport(e.projectReader(&e.Rep1, &e.Team1, principal.RowScopeTeam), datasource.ReportPlan{
		Entity: datasource.EntityProject, GroupBy: []string{"phase"},
	})
	if err != nil {
		t.Fatalf("team-scoped project plan after seeding an own row: %v", err)
	}
	if len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][0]) != "pursuing" || fmt.Sprint(res.Rows[0][1]) != "1" {
		t.Fatalf("own project rows = %+v, want one 'pursuing' row counting 1", res.Rows)
	}
}

func TestAdHocProjectReportRefusesWithoutTheObjectGrant(t *testing.T) {
	e := setupSearch(t)
	e.seedProjects(t, "delivering", nil, 1)
	provider := compose.NewProvider(e.Pool)

	// searchReadGrants carries no project grant: the descriptor widened the
	// vocabulary, not the admission.
	_, err := provider.RunReport(e.Admin(), datasource.ReportPlan{
		Entity: datasource.EntityProject, GroupBy: []string{"phase"},
	})
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("project report without project.read → %v, want ErrPermissionDenied", err)
	}
}

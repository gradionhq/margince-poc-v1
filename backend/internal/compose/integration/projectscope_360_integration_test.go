// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The project scope on the two record pages. Each page reads its timeline
// and its last-touch dates through its own SQL, so the timeline list being
// scoped proves nothing about either; and the last-touch date is the number a
// reader trusts most, so it is asserted on its own rather than inferred from
// the rows beneath it.
//
// The fixture makes the OTHER engagement's mail the newest exchange on the
// account. An unscoped read therefore reports that date as the last inbound
// touch, and a scoped read that still does has leaked the other project
// through the one section the rows do not show.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// timelineIDs collects which activities a page's timeline section shows.
func timelineIDs(rows []crmcontracts.Activity) map[string]bool {
	out := map[string]bool{}
	for _, a := range rows {
		out[a.Id.String()] = true
	}
	return out
}

// assertScopedTimeline is the one reading both pages must give: the scoped
// project's mail and the unfiled mail stay, the other engagement's goes, and
// the last inbound touch is no longer the other engagement's date.
func assertScopedTimeline(t *testing.T, f scopeFixture, seen map[string]bool, lastInbound *time.Time) {
	t.Helper()
	if seen[f.onOther] {
		t.Error("the other engagement's mail survived a scoped page — the scope filtered nothing")
	}
	if !seen[f.onERP] {
		t.Error("the scoped project's own mail is missing from the page")
	}
	if !seen[f.unfiled] {
		t.Error("mail filed under NO project was dropped; the rule keeps it")
	}
	if lastInbound == nil {
		t.Fatal("the scoped page reports no inbound touch at all, though two in-scope mails are inbound")
	}
	if lastInbound.Equal(f.otherAt) {
		t.Errorf("last_inbound_at = %s, the other engagement's mail — the last-touch read ignores the scope", f.otherAt)
	}
}

// assertScopedNeighbours covers the sections beside the timeline that read
// activities through their own SQL: the open tasks, the next meeting and the
// since-last-visit count. Each assertion is the one that fails when that
// section's copy of the scope is missing.
//
// inScope is how many of the fixture's rows the scope keeps: the ERP mail, the
// unfiled mail, the ERP task and the ERP meeting. The caller has no visit
// baseline, so the count runs over the whole history.
func assertScopedNeighbours(t *testing.T, f scopeFixture, tasks map[string]bool, nextMeeting *string, newActivities int) {
	t.Helper()
	if tasks[f.otherTask] {
		t.Error("the other engagement's open task survived a scoped page — next_steps ignores the scope")
	}
	if !tasks[f.erpTask] {
		t.Error("the scoped project's own open task is missing from next_steps")
	}
	switch {
	case nextMeeting == nil:
		t.Error("the scoped page names no next meeting, though the scoped project has one booked")
	case *nextMeeting == f.otherMeeting:
		t.Error("the scoped page's next meeting is the other engagement's — next_meeting ignores the scope")
	case *nextMeeting != f.erpMeeting:
		t.Errorf("next_meeting = %s, want the scoped project's %s", *nextMeeting, f.erpMeeting)
	}
	const inScope = 4
	if newActivities != inScope {
		t.Errorf("since_last_visit.new_activities = %d, want %d — the count reads rows the scope drops", newActivities, inScope)
	}
}

func activityIDOf(id openapi_types.UUID) *string { s := id.String(); return &s }

func TestPerson360ScopedToOneProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	svc := personRoomService(e)
	personID := PersonIDOf(f.person)

	scoped, err := svc.AssembleScoped(e.Admin(), personID, person360.AssembleOptions{ProjectID: &f.erp})
	if err != nil {
		t.Fatalf("assemble scoped: %v", err)
	}
	if scoped.Activities == nil {
		t.Fatal("the activities section was withheld, so the scope cannot be judged")
	}
	assertScopedTimeline(t, f, timelineIDs(scoped.Activities.Data), scoped.LastInboundAt)
	if scoped.NextSteps == nil || scoped.SinceLastVisit == nil {
		t.Fatal("next_steps or since_last_visit was withheld, so the scope cannot be judged")
	}
	var nextMeeting *string
	if scoped.NextMeeting != nil {
		nextMeeting = activityIDOf(scoped.NextMeeting.ActivityId)
	}
	assertScopedNeighbours(t, f, timelineIDs(scoped.NextSteps.Data), nextMeeting, scoped.SinceLastVisit.NewActivities)

	// Unscoped, the same page still shows the other engagement and dates its
	// last touch from it — so the narrowing above is the scope's doing.
	wide, err := svc.Assemble(e.Admin(), personID)
	if err != nil {
		t.Fatalf("assemble unscoped: %v", err)
	}
	if !timelineIDs(wide.Activities.Data)[f.onOther] {
		t.Error("an unscoped page lost the other engagement, so the scoped one proves nothing")
	}
	if wide.NextMeeting == nil || wide.NextMeeting.ActivityId.String() != f.otherMeeting {
		t.Error("an unscoped page does not name the other engagement's sooner meeting, so the scoped one proves nothing")
	}
	if wide.LastInboundAt == nil || !wide.LastInboundAt.Equal(f.otherAt) {
		t.Errorf("unscoped last_inbound_at = %v, want the other engagement's %s", wide.LastInboundAt, f.otherAt)
	}
}

func TestOrganization360ScopedToOneProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	svc := org360.NewService(e.Pool, e.People, e.Deals, approvals.NewService(e.DB()),
		func() time.Time { return roomFixedNow })
	orgID := orgIDOf(f.org)

	scoped, err := svc.AssembleScoped(e.Admin(), orgID, org360.AssembleOptions{ProjectID: &f.erp})
	if err != nil {
		t.Fatalf("assemble scoped: %v", err)
	}
	if scoped.Activities == nil {
		t.Fatal("the activities section was withheld, so the scope cannot be judged")
	}
	assertScopedTimeline(t, f, timelineIDs(scoped.Activities.Data), scoped.LastInboundAt)
	if scoped.NextSteps == nil || scoped.SinceLastVisit == nil {
		t.Fatal("next_steps or since_last_visit was withheld, so the scope cannot be judged")
	}
	tasks := map[string]bool{}
	for _, step := range scoped.NextSteps.Data {
		tasks[step.ActivityId.String()] = true
	}
	var nextMeeting *string
	if scoped.NextMeeting != nil {
		nextMeeting = activityIDOf(scoped.NextMeeting.ActivityId)
	}
	assertScopedNeighbours(t, f, tasks, nextMeeting, scoped.SinceLastVisit.NewActivities)

	wide, err := svc.Assemble(e.Admin(), orgID)
	if err != nil {
		t.Fatalf("assemble unscoped: %v", err)
	}
	if !timelineIDs(wide.Activities.Data)[f.onOther] {
		t.Error("an unscoped page lost the other engagement, so the scoped one proves nothing")
	}
	if wide.NextMeeting == nil || wide.NextMeeting.ActivityId.String() != f.otherMeeting {
		t.Error("an unscoped page does not name the other engagement's sooner meeting, so the scoped one proves nothing")
	}
	if wide.LastInboundAt == nil || !wide.LastInboundAt.Equal(f.otherAt) {
		t.Errorf("unscoped last_inbound_at = %v, want the other engagement's %s", wide.LastInboundAt, f.otherAt)
	}
}

// The scope is a read of the project. A caller with no project grant may not
// use it as an oracle for which activities are filed under one (403), and a
// project the caller cannot see — or that does not exist — answers the same
// existence-hiding 404 a direct read gives.
func TestAProjectScopeIsGatedLikeAReadOfTheProject(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(roomPerms, "project"))
	nobodyID := ids.From[ids.ProjectKind](ids.NewV7())
	person := string(datasource.RecordPerson)

	if _, _, err := e.Activities.ListActivities(rep, activities.ListActivitiesInput{
		EntityType: &person, EntityID: &f.person, WithinProjectID: &f.erp,
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("list scoped to a project without a project grant: err = %v, want permission denied", err)
	}
	if _, _, err := e.Activities.ListActivities(e.Admin(), activities.ListActivitiesInput{
		EntityType: &person, EntityID: &f.person, WithinProjectID: &nobodyID,
	}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("list scoped to a project that does not exist: err = %v, want not found", err)
	}

	personSvc := personRoomService(e)
	if _, err := personSvc.AssembleScoped(rep, PersonIDOf(f.person), person360.AssembleOptions{ProjectID: &f.erp}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("person page scoped without a project grant: err = %v, want permission denied", err)
	}
	if _, err := personSvc.AssembleScoped(e.Admin(), PersonIDOf(f.person), person360.AssembleOptions{ProjectID: &nobodyID}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("person page scoped to a project that does not exist: err = %v, want not found", err)
	}

	orgSvc := org360.NewService(e.Pool, e.People, e.Deals, approvals.NewService(e.DB()), func() time.Time { return roomFixedNow })
	if _, err := orgSvc.AssembleScoped(rep, orgIDOf(f.org), org360.AssembleOptions{ProjectID: &f.erp}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("company page scoped without a project grant: err = %v, want permission denied", err)
	}
	if _, err := orgSvc.AssembleScoped(e.Admin(), orgIDOf(f.org), org360.AssembleOptions{ProjectID: &nobodyID}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("company page scoped to a project that does not exist: err = %v, want not found", err)
	}
}

// The same gate on the wire: the handler maps the refusal to the status a
// client acts on rather than answering an unfiltered page.
func TestAProjectScopeOnTheActivityListAnswers403And404OnTheWire(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	h := activities.NewHandlers(e.DB())
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, withoutGrant(roomPerms, "project"))
	list := func(ctx context.Context, projectID ids.UUID) int {
		t.Helper()
		wire := openapi_types.UUID(projectID)
		status, _ := call(ctx, http.MethodGet, "/v1/activities?project_id="+projectID.String(),
			func(w http.ResponseWriter, r *http.Request) {
				h.ListActivities(w, r, crmcontracts.ListActivitiesParams{ProjectId: &wire})
			})
		return status
	}
	if got := list(rep, f.erp.UUID); got != http.StatusForbidden {
		t.Errorf("GET /activities?project_id= without a project grant = %d, want 403", got)
	}
	if got := list(e.Admin(), ids.NewV7()); got != http.StatusNotFound {
		t.Errorf("GET /activities?project_id=<nobody> = %d, want 404", got)
	}
	if got := list(e.Admin(), f.erp.UUID); got != http.StatusOK {
		t.Errorf("GET /activities?project_id= with the grant = %d, want 200 — the gate refuses everyone", got)
	}
}

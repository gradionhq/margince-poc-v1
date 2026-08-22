// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Every AI surface can be told which project it is about: the prepared
// questions, the account brief and the pre-meeting brief narrowed to one
// body of work, and the scope line each reports beside its words.
//
// The NEGATIVE half leads, as in activity_projectscope_integration_test.go:
// a surface scoped to one engagement must not carry the other engagement's
// rows, and a test asserting only that the wanted rows appear would pass
// against a scope that does nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// recordingLane keeps the account summary the writer was handed, which is the
// one place a scope leak shows regardless of what the model then says.
type recordingLane struct {
	prompt string
}

func (l *recordingLane) Complete(_ context.Context, req model.Request) (model.Response, error) {
	for _, message := range req.Messages {
		l.prompt += message.Content
	}
	return model.Response{Text: `{"sections":[]}`}, nil
}

// The fixture's open task on each engagement, plus one nobody filed: the
// "filed here or filed nowhere" rule needs an unfiled row to keep.
func unfiledTask(t *testing.T, e *Env, f scopeFixture) {
	t.Helper()
	subject := "Chase the signed NDA"
	_, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "task", Subject: &subject,
		Links: []activities.ActivityLinkInput{
			{EntityType: "person", EntityID: f.person},
			{EntityType: "organization", EntityID: f.org},
		},
	})
	if err != nil {
		t.Fatalf("log the unfiled task: %v", err)
	}
}

func TestAskScopedToOneProjectDropsTheOtherEngagementAndReportsTheScope(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	unfiledTask(t, e, f)
	svc := briefService(e, nil, "")

	answer, err := svc.AskScoped(e.Admin(), ids.From[ids.OrganizationKind](f.org),
		crmcontracts.OrganizationQuestionWhatsOpen, &f.erp)
	if err != nil {
		t.Fatalf("ask scoped: %v", err)
	}
	var text strings.Builder
	for _, sentence := range answer.Sentences {
		text.WriteString(sentence.Text + "\n")
	}
	if strings.Contains(text.String(), "rack haulier") {
		t.Errorf("answer = %q; names the other engagement's task", text.String())
	}
	if !strings.Contains(text.String(), "cutover checklist") || !strings.Contains(text.String(), "signed NDA") {
		t.Errorf("answer = %q; want this engagement's task and the unfiled one", text.String())
	}
	if answer.Scope == nil {
		t.Fatal("scope = nil, want the project the answer was narrowed to")
	}
	if answer.Scope.ProjectId != crmcontracts.Id(f.erp.UUID) || answer.Scope.Key == nil || *answer.Scope.Key != "ERP-27" {
		t.Errorf("scope = %+v, want ERP-27", *answer.Scope)
	}
	// Eight activities on the account; the scope drops the other
	// engagement's mail, task and meeting.
	if answer.Scope.InScope == nil || answer.Scope.Total == nil || *answer.Scope.InScope != 5 || *answer.Scope.Total != 8 {
		t.Errorf("scope counts = %v of %v, want 5 of 8", answer.Scope.InScope, answer.Scope.Total)
	}

	unscoped, err := svc.Ask(e.Admin(), ids.From[ids.OrganizationKind](f.org), crmcontracts.OrganizationQuestionWhatsOpen)
	if err != nil {
		t.Fatalf("ask unscoped: %v", err)
	}
	if unscoped.Scope != nil {
		t.Errorf("unscoped scope = %+v, want none", *unscoped.Scope)
	}
}

func TestAskScopedToAProjectTheCallerCannotSeeAnswersNotFound(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	ghost := ids.From[ids.ProjectKind](ids.NewV7())
	_, err := briefService(e, nil, "").AskScoped(e.Admin(), ids.From[ids.OrganizationKind](f.org),
		crmcontracts.OrganizationQuestionWhatsOpen, &ghost)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want not-found for a project that does not exist", err)
	}
}

// The brief is written from the scoped summary, and a scoped brief and an
// unscoped one never serve each other from the cache: the project rides the
// fingerprint, so the switch back is a rewrite rather than a stale read.
func TestOrganizationBriefScopedToOneProjectWritesFromTheScopedSummaryOnly(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	lane := &recordingLane{}
	svc := briefService(e, lane, "routing-1")
	org := ids.From[ids.OrganizationKind](f.org)

	scoped, err := svc.GetScoped(e.Admin(), org, false, &f.erp)
	if err != nil {
		t.Fatalf("brief scoped: %v", err)
	}
	if strings.Contains(lane.prompt, "Rack decommissioning") || strings.Contains(lane.prompt, "rack haulier") {
		t.Errorf("scoped summary carries the other engagement:\n%s", lane.prompt)
	}
	if !strings.Contains(lane.prompt, "Invoice question") || !strings.Contains(lane.prompt, "ERP-27") {
		t.Errorf("scoped summary = %q; want the unfiled mail kept and the project named", lane.prompt)
	}
	if scoped.Scope == nil || scoped.Scope.ProjectId != crmcontracts.Id(f.erp.UUID) {
		t.Fatalf("scope = %+v, want ERP-27", scoped.Scope)
	}
	if scoped.Scope.InScope == nil || scoped.Scope.Total == nil || *scoped.Scope.InScope != 4 || *scoped.Scope.Total != 7 {
		t.Errorf("scope counts = %v of %v, want 4 of 7", scoped.Scope.InScope, scoped.Scope.Total)
	}

	lane.prompt = ""
	unscoped, err := svc.Get(e.Admin(), org, false)
	if err != nil {
		t.Fatalf("brief unscoped: %v", err)
	}
	if lane.prompt == "" {
		t.Fatal("the unscoped brief was served from the scoped cache row")
	}
	if !strings.Contains(lane.prompt, "Rack decommissioning") {
		t.Errorf("unscoped summary = %q; want the whole account", lane.prompt)
	}
	if unscoped.Scope != nil {
		t.Errorf("unscoped scope = %+v, want none", *unscoped.Scope)
	}
}

// A meeting filed under no project takes the requested scope: the lead
// attendee's history is read narrowed by it, and the brief says so.
func TestMeetingBriefTakesARequestedProjectForAnUnattributedMeeting(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	subject := "Quarterly check-in"
	startsAt := roomFixedNow.AddDate(0, 0, 1)
	logged, _, err := e.Activities.LogActivity(e.Admin(), activities.LogActivityInput{
		Kind: "meeting", MeetingStatus: strPtr("booked"), Subject: &subject, OccurredAt: &startsAt,
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: f.person}},
	})
	if err != nil {
		t.Fatalf("log the unattributed meeting: %v", err)
	}
	meeting := ids.UUID(logged.Id)

	brief, err := meetingBriefService(e).GetScoped(e.Admin(), meeting, &f.erp)
	if err != nil {
		t.Fatalf("brief scoped: %v", err)
	}
	if brief.Scope == nil || brief.Scope.ProjectId != crmcontracts.Id(f.erp.UUID) {
		t.Fatalf("scope = %+v, want ERP-27", brief.Scope)
	}
	// The attendee's eight activities include the meeting itself; the scope
	// drops the other engagement's three.
	if brief.Scope.InScope == nil || brief.Scope.Total == nil || *brief.Scope.InScope != 5 || *brief.Scope.Total != 8 {
		t.Errorf("scope counts = %v of %v, want 5 of 8", brief.Scope.InScope, brief.Scope.Total)
	}
	var text strings.Builder
	for _, section := range brief.Sections {
		for _, sentence := range section.Sentences {
			text.WriteString(sentence.Text + "\n")
		}
	}
	if strings.Contains(text.String(), "Rack") {
		t.Errorf("brief = %q; describes the other engagement", text.String())
	}

	unscoped, err := meetingBriefService(e).Get(e.Admin(), meeting)
	if err != nil {
		t.Fatalf("brief unscoped: %v", err)
	}
	if unscoped.Scope != nil {
		t.Errorf("unscoped scope = %+v, want none", *unscoped.Scope)
	}
}

// A meeting filed under one project is not available as a brief about
// another, and the refusal hides which project it IS filed under.
func TestMeetingBriefRefusesAProjectTheMeetingIsNotFiledUnder(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	meeting := ids.MustParse(f.erpMeeting)

	_, err := meetingBriefService(e).GetScoped(e.Admin(), meeting, &f.other)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want not-found for a project the meeting is not filed under", err)
	}
	agreeing, err := meetingBriefService(e).GetScoped(e.Admin(), meeting, &f.erp)
	if err != nil {
		t.Fatalf("the meeting's own project refused: %v", err)
	}
	if agreeing.Scope == nil || agreeing.Scope.ProjectId != crmcontracts.Id(f.erp.UUID) {
		t.Errorf("scope = %+v, want the meeting's own project", agreeing.Scope)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// An activity's audience is a per-message, human-set limit on who reads its
// CONTENT. Customer identity is workspace-readable, so a colleague in another
// team discovers the row through the contact — and sees it withheld: date,
// direction and kind, not subject or body. The author keeps reading it, a
// named member is let in, and nobody but a writer of the row may set it.
func TestLimitingAnActivityWithholdsItsContentFromEveryoneButItsAudience(t *testing.T) {
	e := Setup(t)
	author := e.As(e.Rep1, []ids.UUID{e.Team1}, activityLifecyclePerms)
	colleague := e.As(e.Rep3, []ids.UUID{e.Team2}, activityLifecyclePerms)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)

	subject, body := "Q3 renewal terms", "confidential pricing"
	logged, _, err := e.Activities.LogActivity(author, activities.LogActivityInput{
		Kind: "email", Subject: &subject, Body: &body, Direction: strPtr("outbound"),
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: contact}},
	})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	id := ids.From[ids.ActivityKind](ids.UUID(logged.Id))

	// Before: the colleague reads it whole.
	before, err := e.Activities.GetActivity(colleague, id, storekit.LiveOnly)
	if err != nil || before.Subject == nil || *before.ContentState != crmcontracts.ActivityContentStateAvailable {
		t.Fatalf("before limiting: %+v %v, want the subject and content_state=available", before, err)
	}

	// Nobody but a writer of the row may limit it.
	if _, err := e.Activities.SetAudience(colleague, id, activities.SetAudienceInput{Audience: "participants"}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("a colleague limiting another team's mail → %v, want ErrPermissionDenied", err)
	}
	if _, err := e.Activities.SetAudience(e.AgentCtx(), id, activities.SetAudienceInput{Audience: "participants"}); err == nil {
		t.Error("an agent limited an activity; the audience is a human's call")
	}

	limited, err := e.Activities.SetAudience(author, id, activities.SetAudienceInput{Audience: "participants"})
	if err != nil {
		t.Fatalf("author limiting: %v", err)
	}
	if limited.Audience == nil || *limited.Audience != crmcontracts.ActivityAudienceParticipants || limited.Subject == nil {
		t.Errorf("the author's own read after limiting = %+v, want audience=participants with the content", limited)
	}

	// After: discoverable, content withheld — on the single read and on the list.
	after, err := e.Activities.GetActivity(colleague, id, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("colleague reading a limited mail: %v, want the withheld row", err)
	}
	if *after.ContentState != crmcontracts.ActivityContentStateWithheld || after.Subject != nil || after.Body != nil {
		t.Errorf("colleague's read = %+v, want content_state=withheld with no subject/body", after)
	}
	if after.Direction == nil || after.OccurredAt.IsZero() {
		t.Errorf("the withheld row lost its safe markers: %+v", after)
	}
	page, _, err := e.Activities.ListActivities(colleague, activities.ListActivitiesInput{EntityType: strPtr("person"), EntityID: &contact})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || *page[0].ContentState != crmcontracts.ActivityContentStateWithheld || page[0].Subject != nil {
		t.Errorf("colleague's list = %+v, want the one row withheld", page)
	}
	// The audit row carries no content either.
	if n := e.WsCount(t, `SELECT count(*) FROM audit_log WHERE action = 'update' AND entity_id = $1 AND after::text LIKE '%'||$2||'%'`, logged.Id, body); n != 0 {
		t.Error("the audience audit row carries the body")
	}

	// Naming the colleague lets them in; re-opening lets everyone in.
	if _, err := e.Activities.SetAudience(author, id, activities.SetAudienceInput{Audience: "selected",
		Members: []activities.AudienceMember{{SubjectType: "user", SubjectID: e.Rep3}}}); err != nil {
		t.Fatalf("selecting the colleague: %v", err)
	}
	if got, err := e.Activities.GetActivity(colleague, id, storekit.LiveOnly); err != nil || got.Subject == nil {
		t.Errorf("a selected member's read = %+v %v, want the content", got, err)
	}
	if _, err := e.Activities.SetAudience(author, id, activities.SetAudienceInput{Audience: "workspace"}); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity_audience_member WHERE activity_id = $1`, logged.Id); n != 0 {
		t.Errorf("%d member rows after re-opening, want 0", n)
	}
	if got, err := e.Activities.GetActivity(colleague, id, storekit.LiveOnly); err != nil || got.Subject == nil {
		t.Errorf("after re-opening, colleague's read = %+v %v, want the content", got, err)
	}
}

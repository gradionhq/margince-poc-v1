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
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/compose/person360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
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

	// Unscoped, the same page still shows the other engagement and dates its
	// last touch from it — so the narrowing above is the scope's doing.
	wide, err := svc.Assemble(e.Admin(), personID)
	if err != nil {
		t.Fatalf("assemble unscoped: %v", err)
	}
	if !timelineIDs(wide.Activities.Data)[f.onOther] {
		t.Error("an unscoped page lost the other engagement, so the scoped one proves nothing")
	}
	if wide.LastInboundAt == nil || !wide.LastInboundAt.Equal(f.otherAt) {
		t.Errorf("unscoped last_inbound_at = %v, want the other engagement's %s", wide.LastInboundAt, f.otherAt)
	}
}

func TestOrganization360ScopedToOneProjectDropsTheOtherEngagement(t *testing.T) {
	e := Setup(t)
	f := seedTwoEngagementAccount(t, e)
	svc := org360.NewService(e.Pool, e.People, approvals.NewService(e.DB()),
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

	wide, err := svc.Assemble(e.Admin(), orgID)
	if err != nil {
		t.Fatalf("assemble unscoped: %v", err)
	}
	if !timelineIDs(wide.Activities.Data)[f.onOther] {
		t.Error("an unscoped page lost the other engagement, so the scoped one proves nothing")
	}
	if wide.LastInboundAt == nil || !wide.LastInboundAt.Equal(f.otherAt) {
		t.Errorf("unscoped last_inbound_at = %v, want the other engagement's %s", wide.LastInboundAt, f.otherAt)
	}
}

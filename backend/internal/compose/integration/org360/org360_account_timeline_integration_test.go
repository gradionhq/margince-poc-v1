// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package org360

// What "this activity belongs to this account" means, proved over a real
// database and through every surface that asks the question.
//
// Mail is filed against the PERSON it was with, so an account whose timeline
// matched only its own activity_link rows showed a rep an empty page for a
// company they had been emailing all week. The account is reached through
// three links — its own, its deal's, and a LIVE employment — and the timeline
// list, the company view's section and the since-last-visit count all read the
// same walk, so they cannot answer differently about one activity.
//
// The walk widens WHICH activities belong to the account. It must not widen
// WHO may read one, which is the row-scope case below.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// accountMailAt logs one inbound email at a fixed instant and returns its id.
// Both timestamps are set: occurred_at orders the timeline, created_at is what
// the since-last-visit count compares against the baseline.
func accountMailAt(t *testing.T, owner *pgx.Conn, ws ids.UUID, subject string, at time.Time) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `INSERT INTO activity
		(id, workspace_id, kind, direction, subject, occurred_at, created_at, source, captured_by)
		VALUES ($1, $2, 'email', 'inbound', $3, $4, $4, 'manual', 'human:x')`,
		id, ws, subject, at); err != nil {
		t.Fatalf("seeding %q: %v", subject, err)
	}
	return id
}

// employ ties a person to an organization as a current employee, in the role
// the graph draws them in.
func employ(t *testing.T, e *integration.Env, person, org ids.UUID, title string) {
	t.Helper()
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, role, source, captured_by)
		VALUES ($1, 'employment', $2, $3, $4, 'manual', 'human:x')`, e.WS, person, org, title)
}

// employAt ties a person to an organization. endedOn nil is a live employment;
// a date ends it.
func employAt(t *testing.T, e *integration.Env, person, org ids.UUID, endedOn *time.Time) {
	t.Helper()
	e.WsExec(t, `INSERT INTO relationship
		(workspace_id, kind, person_id, organization_id, started_at, ended_at, source, captured_by)
		VALUES ($1, 'employment', $2, $3, DATE '2026-01-01', $4::date, 'manual', 'human:x')`,
		e.WS, person, org, endedOn)
}

// accountTimeline lists the account's timeline the way GET /activities does.
func accountTimeline(ctx context.Context, t *testing.T, e *integration.Env, org ids.UUID, limit int, cursor string) ([]ids.UUID, string) {
	t.Helper()
	entityType := "organization"
	in := activities.ListActivitiesInput{EntityType: &entityType, EntityID: &org, Limit: &limit}
	if cursor != "" {
		in.Cursor = &cursor
	}
	rows, page, err := e.Activities.ListActivities(ctx, in)
	if err != nil {
		t.Fatalf("listing the account timeline: %v", err)
	}
	got := make([]ids.UUID, 0, len(rows))
	for _, row := range rows {
		got = append(got, ids.UUID(row.Id))
	}
	return got, page.NextCursor
}

// containsActivity reports whether the listed timeline holds one activity.
func containsActivity(got []ids.UUID, want ids.UUID) bool {
	for _, id := range got {
		if id == want {
			return true
		}
	}
	return false
}

// The three arms and their two exclusions, on one account, read through the
// timeline list and the company view's own section — which must agree,
// because the section is that list.
func TestAccountTimelineReachesMailThroughItsContactsAndDeals(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)
	pipeline, stage, _ := integration.DealFixture(t, e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	other := e.SeedOrg(t, "Globex", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)

	employee := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employAt(t, e, employee, org, nil)
	leaver := e.SeedPerson(t, "Sam Leaver", &e.Rep1)
	ended := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	employAt(t, e, leaver, org, &ended)
	stranger := e.SeedPerson(t, "Kim Elsewhere", &e.Rep1)
	employAt(t, e, stranger, other, nil)

	deal := e.SeedDeal(t, "Acme renewal", pipeline, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)

	direct := accountMailAt(t, owner, e.WS, "direct", org360Clock.Add(-1*time.Hour))
	integration.LinkToOrg(t, e, direct, org)
	viaContact := accountMailAt(t, owner, e.WS, "via a current employee", org360Clock.Add(-2*time.Hour))
	integration.LinkActivity(t, owner, e.WS, viaContact, "person", employee)
	viaDeal := accountMailAt(t, owner, e.WS, "via the deal", org360Clock.Add(-3*time.Hour))
	integration.LinkActivity(t, owner, e.WS, viaDeal, "deal", deal)
	viaLeaver := accountMailAt(t, owner, e.WS, "via a former employee", org360Clock.Add(-4*time.Hour))
	integration.LinkActivity(t, owner, e.WS, viaLeaver, "person", leaver)
	viaStranger := accountMailAt(t, owner, e.WS, "via another account's contact", org360Clock.Add(-5*time.Hour))
	integration.LinkActivity(t, owner, e.WS, viaStranger, "person", stranger)

	listed, _ := accountTimeline(rep, t, e, org, 25, "")
	for _, want := range []struct {
		id   ids.UUID
		what string
	}{
		{direct, "an activity linked to the account itself"},
		{viaContact, "mail filed against a current employee"},
		{viaDeal, "mail filed against the account's deal"},
	} {
		if !containsActivity(listed, want.id) {
			t.Errorf("the account timeline omits %s (%v): %v", want.what, want.id, listed)
		}
	}
	for _, unwanted := range []struct {
		id   ids.UUID
		what string
	}{
		{viaLeaver, "mail filed against someone who has LEFT the company"},
		{viaStranger, "mail filed against a contact at another account"},
	} {
		if containsActivity(listed, unwanted.id) {
			t.Errorf("the account timeline includes %s (%v): %v", unwanted.what, unwanted.id, listed)
		}
	}

	// The company view's section is the same list, so it must hold the same set.
	view, err := svc.Assemble(rep, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Activities == nil {
		t.Fatal("the activities section is absent for a rep who may read the timeline")
	}
	section := make([]ids.UUID, 0, len(view.Activities.Data))
	for _, row := range view.Activities.Data {
		section = append(section, ids.UUID(row.Id))
	}
	if len(section) != len(listed) {
		t.Errorf("the 360 section holds %d activities, the timeline list %d — one read, two answers",
			len(section), len(listed))
	}
	if !containsActivity(section, viaContact) {
		t.Errorf("the 360 activities section omits mail filed against a current employee: %v", section)
	}
}

// The account walk decides WHICH activities belong to the account. WHO may
// read one is still the activity link-walk row scope, so reaching further must
// not hand a bounded rep an item they could not open.
func TestTheAccountWalkDoesNotWidenTheCallersRowScope(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)

	// Both contacts work at the account; only one is inside Rep1's team scope.
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	employAt(t, e, mine, org, nil)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	employAt(t, e, theirs, org, nil)

	visible := accountMailAt(t, owner, e.WS, "terms", org360Clock.Add(-1*time.Hour))
	integration.LinkActivity(t, owner, e.WS, visible, "person", mine)
	hidden := accountMailAt(t, owner, e.WS, "confidential terms", org360Clock.Add(-2*time.Hour))
	integration.LinkActivity(t, owner, e.WS, hidden, "person", theirs)

	listed, _ := accountTimeline(rep, t, e, org, 25, "")
	if containsActivity(listed, hidden) {
		t.Errorf("the account timeline hands a bounded rep an activity whose only link "+
			"is a contact outside their row scope (%v): %v", hidden, listed)
	}
	// The positive control: reaching through a contact still works, so the gate
	// narrows the page rather than emptying it.
	if !containsActivity(listed, visible) {
		t.Errorf("the account timeline omits mail filed against a contact the rep can read (%v): %v",
			visible, listed)
	}
}

// Paging the account timeline must lose nothing and repeat nothing, including
// an activity reachable through TWO arms at once — the case a join would
// duplicate and a duplicate is what shifts a keyset page boundary.
func TestAccountTimelinePagesWithoutDuplicatesOrOmissions(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employAt(t, e, contact, org, nil)

	// Five mails, one hour apart, alternating how they reach the account; the
	// third is linked BOTH ways.
	want := make([]ids.UUID, 0, 5)
	for i := range 5 {
		mail := accountMailAt(t, owner, e.WS, "thread", org360Clock.Add(-time.Duration(i+1)*time.Hour))
		if i%2 == 0 {
			integration.LinkToOrg(t, e, mail, org)
		}
		if i%2 == 1 || i == 2 {
			integration.LinkActivity(t, owner, e.WS, mail, "person", contact)
		}
		want = append(want, mail)
	}

	seen := map[ids.UUID]int{}
	var order []ids.UUID
	cursor := ""
	for range 5 {
		got, next := accountTimeline(rep, t, e, org, 2, cursor)
		for _, id := range got {
			seen[id]++
			order = append(order, id)
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	if cursor != "" {
		t.Fatalf("the timeline still reports more after five pages of two over five activities")
	}
	if len(order) != len(want) {
		t.Errorf("paging returned %d activities, want %d: %v", len(order), len(want), order)
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Errorf("activity %v was returned %d times across the pages, want exactly 1", id, seen[id])
		}
	}
	// Newest first, which is the order the cursor is built from.
	for i, id := range order {
		if i < len(want) && id != want[i] {
			t.Errorf("page position %d = %v, want %v (newest first, stable across the boundary)", i, id, want[i])
		}
	}
}

// since_last_visit counts what the page shows. Mail that reaches the account
// only through a contact is on the page, so it is new here too — a rep told
// "0 new" above three unread emails would stop trusting the marker.
func TestSinceLastVisitCountsMailRolledUpThroughAContact(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	orgID := ids.From[ids.OrganizationKind](org)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	employAt(t, e, contact, org, nil)

	// Mail that arrived BEFORE the visit is not new; the ack pins the baseline
	// at the read's own instant.
	before := accountMailAt(t, owner, e.WS, "old thread", org360Clock.Add(-time.Hour))
	integration.LinkActivity(t, owner, e.WS, before, "person", contact)
	if _, err := svc.Acknowledge(rep, orgID); err != nil {
		t.Fatalf("acknowledging the visit: %v", err)
	}
	after := accountMailAt(t, owner, e.WS, "new thread", org360Clock.Add(time.Hour))
	integration.LinkActivity(t, owner, e.WS, after, "person", contact)

	view, err := svc.Assemble(rep, orgID)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.SinceLastVisit == nil {
		t.Fatal("since_last_visit is absent for a rep who may read the timeline")
	}
	if view.SinceLastVisit.NewActivities != 1 {
		t.Errorf("new_activities = %d, want 1 — the one mail filed against a contact since the visit",
			view.SinceLastVisit.NewActivities)
	}
	if view.SinceLastVisit.BaselineAt == nil || !view.SinceLastVisit.BaselineAt.Equal(org360Clock) {
		t.Errorf("baseline_at = %v, want the acknowledged instant %v",
			view.SinceLastVisit.BaselineAt, org360Clock)
	}
}

// TestLastTouchSeparatesWhoWroteLastAndWalksTheSameThreeLinks pins the pair
// that replaced the header's 0-100 score (AC-company-2). One "last touch"
// would collapse the only distinction that matters — an account we mailed a
// fortnight ago with no reply reads identically to one that just wrote to us.
func TestLastTouchSeparatesWhoWroteLastAndWalksTheSameThreeLinks(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Scale Commerce", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)
	employee := e.SeedPerson(t, "Christian Contact", &e.Rep1)
	employAt(t, e, employee, org, nil)

	// The newest inbound reaches the account only through its contact, which
	// is how capture files real mail — a direct-link-only reading would miss it.
	oldInbound := integration.AccountMailDirectedAt(t, owner, e.WS, "old reply", "inbound", org360Clock.Add(-90*time.Hour))
	integration.LinkToOrg(t, e, oldInbound, org)
	newInbound := integration.AccountMailDirectedAt(t, owner, e.WS, "their reply", "inbound", org360Clock.Add(-30*time.Hour))
	integration.LinkActivity(t, owner, e.WS, newInbound, "person", employee)
	outbound := integration.AccountMailDirectedAt(t, owner, e.WS, "our nudge", "outbound", org360Clock.Add(-2*time.Hour))
	integration.LinkToOrg(t, e, outbound, org)

	view, err := svc.Assemble(rep, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.LastInboundAt == nil || !view.LastInboundAt.Equal(org360Clock.Add(-30*time.Hour)) {
		t.Errorf("last inbound = %v, want the contact-linked reply at -30h", view.LastInboundAt)
	}
	if view.LastOutboundAt == nil || !view.LastOutboundAt.Equal(org360Clock.Add(-2*time.Hour)) {
		t.Errorf("last outbound = %v, want our nudge at -2h", view.LastOutboundAt)
	}
}

// TestLastTouchIsNullRatherThanZeroWhenNothingWasCaptured keeps "we have never
// heard from them" distinct from "we never looked". Null is the account's
// answer; an absent section would be the caller's.
func TestLastTouchIsNullRatherThanZeroWhenNothingWasCaptured(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Silent Co", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)
	outbound := integration.AccountMailDirectedAt(t, owner, e.WS, "our first mail", "outbound", org360Clock.Add(-time.Hour))
	integration.LinkToOrg(t, e, outbound, org)

	view, err := svc.Assemble(rep, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.LastInboundAt != nil {
		t.Errorf("last inbound = %v, want null — they have never written", view.LastInboundAt)
	}
	if view.LastOutboundAt == nil {
		t.Error("last outbound is null, but we mailed them an hour ago")
	}
}

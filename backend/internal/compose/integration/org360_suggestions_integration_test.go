// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Suggestions end to end, over a real database.
//
// The rules themselves are proved without one (org360/suggestions_test.go).
// What needs the database is the part the rules cannot state: which advice a
// real account actually raises, in what order the card offers it, and how much
// of the account the rules still see once the read's own sections have applied
// their page caps.
//
// Every fixture sets its timestamps explicitly. The read's clock is pinned to
// org360Clock while the database's now() is not, so a fixture on now() would
// land on the wrong side of a stale-thread window by accident.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedUnansweredOutbound logs an outbound email old enough to be worth
// chasing, linked to the account.
func seedUnansweredOutbound(t *testing.T, e *Env, org ids.UUID) {
	t.Helper()
	owner := OwnerConn(t)
	sent := SeedRow(t, owner, `INSERT INTO activity
		(id, workspace_id, kind, direction, subject, occurred_at, created_at, source, captured_by)
		VALUES ($1, $2, 'email', 'outbound', 'Proposal — following up',
		        '2026-05-10T09:00:00Z', '2026-05-10T09:00:00Z', 'manual', 'human:x')`, e.WS)
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`, e.WS, sent, org)
}

// The rules must look PAST the section page cap.
//
// The 360's collections are truncated summaries (sectionLimit = 25). A rule
// derived from that page would miss an overdue unanswered email buried under 25
// newer notes, and a rep would read the silent card as "nothing to chase here" —
// which is the one thing this surface must never say wrongly.
func TestSuggestionsLookPastTheSectionPageCap(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	seedUnansweredOutbound(t, e, org.UUID)

	// Thirty notes packed into the hours before the read, every one newer than the
	// email seeded three weeks back — so the email falls past the section's cap.
	// Spacing them by DAYS would not: the email is only three weeks old, so the
	// older notes would sort behind it and it would sit inside the page after all.
	for i := range 30 {
		note := ids.NewV7()
		if _, err := owner.Exec(context.Background(), `INSERT INTO activity
			(id, workspace_id, kind, subject, occurred_at, created_at, source, captured_by)
			VALUES ($1, $2, 'note', $3, $4, $4, 'manual', 'human:x')`,
			note, e.WS, fmt.Sprintf("note %d", i),
			org360Clock.Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("seeding note %d: %v", i, err)
		}
		e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
			VALUES ($1, $2, 'organization', $3)`, e.WS, note, org.UUID)
	}

	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Suggestions == nil {
		t.Fatal("suggestions absent")
	}

	// The premise, asserted rather than assumed: the timeline section this caller
	// was served does NOT contain the email. Without this the fixture could drift
	// into putting it inside the page, and the test below would pass over a rule
	// that read the section — the defect it exists to catch.
	if view.Activities == nil || !view.Activities.Page.HasMore {
		t.Fatalf("the timeline section is not truncated, so nothing here is past its cap")
	}
	for _, activity := range view.Activities.Data {
		if activity.Kind == "email" {
			t.Fatal("the email is inside the section page, so a rule reading the section " +
				"would find it too and this test would prove nothing")
		}
	}

	found := false
	for _, suggestion := range *view.Suggestions {
		if suggestion.Kind == "no_reply" {
			found = true
		}
	}
	if !found {
		t.Errorf("no no_reply suggestion for an email past the section cap: %+v", *view.Suggestions)
	}
}

// The order the rules run in IS the priority the cap applies, and on a full card
// that order is the whole product decision: what the rep sees when they do not
// scroll. A person waiting on us leads; money that stopped moving follows.
//
// It needs an account that produces MORE than the card lists and at least one of
// each kind, or the ordering never binds and the test passes on an accident.
func TestTheMostUrgentAdviceLeadsAFullCard(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	seedUnansweredOutbound(t, e, org.UUID)
	const stalled = maxListedSuggestions + 2
	for i := range stalled {
		deal := e.SeedDeal(t, fmt.Sprintf("Deal %d", i), pipelineID, stage, &e.Rep1)
		e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
			WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200-i))
	}

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	found := *view.Suggestions
	if len(found) != maxListedSuggestions {
		t.Fatalf("listed %d suggestions, want the card's %d — the cap is not binding, "+
			"so this proves nothing about order", len(found), maxListedSuggestions)
	}
	if view.SuggestionsDropped == nil || *view.SuggestionsDropped == 0 {
		t.Fatalf("suggestions_dropped = %v, want the advice the cap cut to be reported",
			view.SuggestionsDropped)
	}
	if string(found[0].Kind) != "no_reply" {
		t.Errorf("the card leads with %q, want the unanswered message — a person is "+
			"waiting on us, and nothing else here is someone else's time", found[0].Kind)
	}
	for _, suggestion := range found[1:] {
		if string(suggestion.Kind) != "stalled_deal" {
			t.Errorf("suggestion %q is listed above a stalled deal", suggestion.Kind)
		}
	}
}

// maxListedSuggestions mirrors the card's own cap (org360.maxSuggestions). Spelled
// here because the integration package cannot see the unexported constant, and a
// test that derived it from the answer could not tell a cap from a coincidence.
const maxListedSuggestions = 3

// Every figure a suggestion states is the ACCOUNT's, never this read's.
//
// The card lists at most a handful of stalled deals. The count in the
// no-next-step reason, the dropped total, and the digest the dismissal is keyed
// on all have to cover the deals past that listing — a figure bounded by its own
// read is one a rep cannot tell from a real one, and a fingerprint built from
// the listed part would leave a dismissal in force after a deal it never saw
// changed.
func TestSuggestionCountsAreTheAccountsNotTheReads(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	// Eight open deals, every one idle long enough to be stalled — more than the
	// card lists, so the listing is bounded while the figures must not be.
	const openDeals = 8
	for i := range openDeals {
		deal := e.SeedDeal(t, fmt.Sprintf("Deal %d", i), pipelineID, stage, &e.Rep1)
		e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = NULL
			WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200))
	}

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Suggestions == nil {
		t.Fatal("suggestions absent")
	}
	listed := len(*view.Suggestions)
	if listed != maxListedSuggestions {
		t.Fatalf("listed %d suggestions for %d stalled deals, want the card's %d",
			listed, openDeals, maxListedSuggestions)
	}
	// The dropped total plus the listed rows must account for every one of them.
	// The no-next-step rule also fires here (nothing is scheduled), so the
	// suggestion count is the stalled deals plus that one.
	if view.SuggestionsDropped == nil {
		t.Fatal("suggestions_dropped absent on a section that was computed")
	}
	if listed+*view.SuggestionsDropped != openDeals+1 {
		t.Errorf("listed %d + dropped %d = %d, want the %d suggestions this account has",
			listed, *view.SuggestionsDropped, listed+*view.SuggestionsDropped, openDeals+1)
	}

	// Nothing here is waiting on a reply, so the priority order puts stalled
	// deals first and the no-next-step row is what the cap drops.
	for _, suggestion := range *view.Suggestions {
		if string(suggestion.Kind) != "stalled_deal" {
			t.Errorf("suggestion %q was listed ahead of a stalled deal", suggestion.Kind)
		}
	}
}

// A task the next-steps PAGE does not show still counts as something scheduled.
//
// hasOpenTask asks the database directly rather than reading that page, and this is
// the case that distinguishes the two: 30 tasks, so the section truncates at 25,
// and the account is left with an open deal. If the rule read the page it would
// still see tasks here — so the test also pins the reachability the direct query
// uses, by linking the only task through the DEAL rather than to the account.
func TestNoNextStepSeesATaskThePageDoesNot(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)
	deal := e.SeedDeal(t, "Renewal", pipelineID, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org.UUID)

	// One task, reachable only through the deal — never linked to the account.
	task := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `INSERT INTO activity
		(id, workspace_id, kind, subject, occurred_at, created_at, source, captured_by, is_done)
		VALUES ($1, $2, 'task', 'Call the CFO', $3, $3, 'manual', 'human:x', false)`,
		task, e.WS, org360Clock); err != nil {
		t.Fatalf("seeding the task: %v", err)
	}
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, deal_id)
		VALUES ($1, $2, 'deal', $3)`, e.WS, task, deal)

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Suggestions == nil {
		t.Fatalf("suggestions absent (sections_omitted=%v)", view.SectionsOmitted)
	}
	for _, suggestion := range *view.Suggestions {
		if suggestion.Kind == "no_next_step" {
			t.Errorf("no_next_step fired on an account with an open task reachable "+
				"through its deal: %+v", suggestion)
		}
	}
}

// A reader who can see the pipeline but not the timeline still gets the advice
// their grants support.
//
// The section holds no grant of its own, so this is the half of that design that
// a fixed activity gate would have broken: stalled-deal advice is something a
// deal reader can act on, and withholding it because they cannot read activities
// would cost them advice they are entitled to.
func TestSuggestionsSurviveWithoutTheActivityGrant(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	deal := e.SeedDeal(t, "Fleet retrofit", pipelineID, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
		WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200))

	// Deals and pipelines, no activity grant at all.
	dealReader := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Read: true}, "deal": {Read: true}, "pipeline": {Read: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	view, err := svc.Assemble(dealReader, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Suggestions == nil {
		t.Fatalf("suggestions omitted for a caller who can read the pipeline (sections_omitted=%v)",
			view.SectionsOmitted)
	}
	if got := stalledFingerprints(*view.Suggestions); len(got) != 1 {
		t.Errorf("got %d stalled-deal suggestions, want the one this reader can act on: %+v",
			len(got), *view.Suggestions)
	}
	// And nothing derived from the timeline they cannot read.
	for _, suggestion := range *view.Suggestions {
		if string(suggestion.Kind) == "no_reply" {
			t.Error("a no_reply suggestion reached a caller with no activity grant")
		}
	}
}

// A caller shown neither the timeline nor the pipeline has nothing to be advised
// from, so the section is omitted and named rather than answering empty.
func TestSuggestionsAreOmittedWhenNeitherInputReachesTheCaller(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	seedUnansweredOutbound(t, e, org.UUID)

	// Organization read only: neither the timeline nor the pipeline.
	reader := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  map[string]principal.ObjectGrant{"organization": {Read: true}},
		RowScope: principal.RowScopeTeam,
	})
	view, err := svc.Assemble(reader, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Suggestions != nil {
		t.Errorf("suggestions = %+v for a caller with no activity grant, want the section omitted",
			*view.Suggestions)
	}
	named := false
	for _, section := range view.SectionsOmitted {
		if section == "suggestions" {
			named = true
		}
	}
	if !named {
		t.Errorf("sections_omitted = %v, want it to name suggestions", view.SectionsOmitted)
	}
}

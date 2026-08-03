// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Suggestions end to end, over a real database.
//
// The rules themselves are proved without one (org360/suggestions_test.go).
// What needs the database is the part the rules cannot state: a dismissal is
// per user and keyed on evidence, so silencing advice must silence it for
// exactly one rep and stop silencing it when the situation changes.
//
// Every fixture sets its timestamps explicitly. The read's clock is pinned to
// org360Clock while the database's now() is not, so a fixture on now() would
// land on the wrong side of a stale-thread window by accident.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// wellFormedFingerprint has the shape the endpoint accepts, so a test about the
// RECORD gate is not answered by the body check instead.
const wellFormedFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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

// Dismissing a listed suggestion must reveal the next one, not shrink the card.
//
// The display cap is applied AFTER dismissals are filtered out. Capping first
// spends a slot on a row the rep has already dealt with, so a rep who judges
// five suggestions on a busy account ends up with an empty card and stalled
// deals they were never shown.
func TestDismissingASuggestionRevealsTheNextOne(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	// Seven stalled deals, more than the card lists.
	for i := range 7 {
		deal := e.SeedDeal(t, fmt.Sprintf("Deal %d", i), pipelineID, stage, &e.Rep1)
		e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
			WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200-i))
	}

	before, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	listed := stalledFingerprints(*before.Suggestions)
	if len(listed) < 2 {
		t.Fatalf("only %d stalled suggestions listed, want the card's full complement", len(listed))
	}
	if err := svc.DismissSuggestion(rep, org, listed[0]); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	after, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("re-assemble: %v", err)
	}
	remaining := stalledFingerprints(*after.Suggestions)
	if len(remaining) != len(listed) {
		t.Errorf("stalled suggestions went from %d to %d after one dismissal — "+
			"the cap was applied before the filter, so judging a row cost a slot",
			len(listed), len(remaining))
	}
	for _, fingerprint := range remaining {
		if fingerprint == listed[0] {
			t.Error("the dismissed suggestion is still listed")
		}
	}
}

// stalledFingerprints is the stalled-deal rows of one answer, in order.
func stalledFingerprints(found []crmcontracts.Organization360Suggestion) []string {
	out := make([]string, 0, len(found))
	for _, suggestion := range found {
		if string(suggestion.Kind) == "stalled_deal" {
			out = append(out, suggestion.Fingerprint)
		}
	}
	return out
}

// A judgment the rep made is never resurrected, however many they make.
//
// The suggestion set is bounded only by the account's own data, so any
// count-bounded retention on the dismissals would eventually delete the earliest
// ones and bring that advice back. A rep who works through a busy account has to
// be able to reach the end of it.
func TestNoDismissalIsEverResurrected(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	// More stalled deals than any retention bound would plausibly keep.
	const stalled = 60
	for i := range stalled {
		deal := e.SeedDeal(t, fmt.Sprintf("Deal %d", i), pipelineID, stage, &e.Rep1)
		e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
			WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200-i))
	}

	// Work the card the way a rep does: dismiss what it offers, re-read, repeat,
	// until it stops offering stalled deals.
	judged := map[string]bool{}
	for round := 0; round < stalled+2; round++ {
		view, err := svc.Assemble(rep, org)
		if err != nil {
			t.Fatalf("assemble in round %d: %v", round, err)
		}
		offered := stalledFingerprints(*view.Suggestions)
		if len(offered) == 0 {
			break
		}
		for _, fingerprint := range offered {
			if judged[fingerprint] {
				t.Fatalf("round %d re-offered a suggestion this rep already dismissed", round)
			}
			if err := svc.DismissSuggestion(rep, org, fingerprint); err != nil {
				t.Fatalf("dismiss in round %d: %v", round, err)
			}
			judged[fingerprint] = true
		}
	}
	if len(judged) != stalled {
		t.Errorf("worked through %d suggestions, want all %d stalled deals reachable", len(judged), stalled)
	}

	// And the card is genuinely empty of them now, not merely quiet this round.
	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("final assemble: %v", err)
	}
	if left := stalledFingerprints(*view.Suggestions); len(left) != 0 {
		t.Errorf("%d stalled suggestions left after dismissing every one", len(left))
	}
	if view.SuggestionsDropped == nil || *view.SuggestionsDropped != 0 {
		t.Errorf("suggestions_dropped = %v with nothing left to show", view.SuggestionsDropped)
	}
}

// A dismissed stall comes back when the deal stalls AGAIN, end to end.
//
// The unit test pins the fingerprint; this pins that the read produces the
// changed one, so the two halves cannot pass while disagreeing.
func TestADismissedStallReturnsWhenTheDealStallsAgain(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)
	deal := e.SeedDeal(t, "Renewal", pipelineID, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
		WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200))

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	first := stalledFingerprints(*view.Suggestions)
	if len(first) != 1 {
		t.Fatalf("got %d stalled suggestions, want the one seeded deal", len(first))
	}
	if err := svc.DismissSuggestion(rep, org, first[0]); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if after, err := svc.Assemble(rep, org); err != nil {
		t.Fatalf("re-assemble: %v", err)
	} else if left := stalledFingerprints(*after.Suggestions); len(left) != 0 {
		t.Fatalf("%d stalled suggestions right after dismissing the only one", len(left))
	}

	// The deal is worked, and then goes quiet again for longer than the window.
	e.WsExec(t, `UPDATE deal SET last_activity_at = $2 WHERE id = $1`,
		deal, org360Clock.AddDate(0, 0, -90))

	again, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble after the second stall: %v", err)
	}
	second := stalledFingerprints(*again.Suggestions)
	if len(second) != 1 {
		t.Fatalf("the second stall raised %d suggestions, want 1 — the first dismissal "+
			"silenced this deal for good", len(second))
	}
	if second[0] == first[0] {
		t.Error("the second stall reuses the first stall's fingerprint")
	}
}

// A deferral cannot resurrect a dismissal, however it is moved.
//
// "The customer asked us to wait" suppresses the stall while the wait runs, so no
// advice is due for a dismissal to affect. What must never happen is the wait being
// set, expiring and then CLEARED, walking the deal back to a shape the rep already
// dismissed — the earlier fingerprint would come back to life and silence advice
// they may have been shown again in between. The fingerprint carries only the idle
// instant, which activities advance with greatest() and nothing lowers.
func TestADeferralNeverResurrectsADismissal(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)
	deal := e.SeedDeal(t, "Renewal", pipelineID, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
		WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200))

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	dismissed := stalledFingerprints(*view.Suggestions)
	if len(dismissed) != 1 {
		t.Fatalf("got %d stalled suggestions, want the one seeded deal", len(dismissed))
	}
	if err := svc.DismissSuggestion(rep, org, dismissed[0]); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// A live deferral: the deal is not stalled, so nothing is advised either way.
	e.WsExec(t, `UPDATE deal SET wait_until = $2 WHERE id = $1`,
		deal, org360Clock.AddDate(0, 0, 30))
	held, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble while deferred: %v", err)
	}
	if left := stalledFingerprints(*held.Suggestions); len(left) != 0 {
		t.Errorf("%d stalled suggestions while the deal is deferred", len(left))
	}

	// The deferral expires, and is then cleared. Neither may hand back advice the
	// rep dismissed, and neither may lose a dismissal they still hold.
	for _, step := range []struct {
		name string
		wait any
	}{
		{"the deferral expires", org360Clock.AddDate(0, 0, -5)},
		{"the deferral is cleared", nil},
	} {
		e.WsExec(t, `UPDATE deal SET wait_until = $2 WHERE id = $1`, deal, step.wait)
		again, err := svc.Assemble(rep, org)
		if err != nil {
			t.Fatalf("assemble after %s: %v", step.name, err)
		}
		if left := stalledFingerprints(*again.Suggestions); len(left) != 0 {
			t.Errorf("after %s the card offers %d stalled suggestions, want the rep's "+
				"dismissal to still hold — nothing was worked on this deal", step.name, len(left))
		}
	}

	// Working the deal is what ends the silence.
	e.WsExec(t, `UPDATE deal SET last_activity_at = $2 WHERE id = $1`,
		deal, org360Clock.AddDate(0, 0, -90))
	worked, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble after the deal was worked: %v", err)
	}
	fresh := stalledFingerprints(*worked.Suggestions)
	if len(fresh) != 1 {
		t.Fatalf("the new stall raised %d suggestions, want 1", len(fresh))
	}
	if fresh[0] == dismissed[0] {
		t.Error("the new stall reuses the dismissed fingerprint")
	}
}

// Advancing the deal re-arms the advice; re-selecting its stage does not.
//
// A stage advance is the most deliberate kind of work there is on a deal, and it
// moves no timestamp the stall rule reads — so a fingerprint built from that rule's
// own inputs would keep the advice silenced through every stage the deal reached.
// It goes through deals.AdvanceDeal rather than an UPDATE, because what the episode
// counts is the history row that path writes.
func TestAdvancingADealReArmsDismissedStallAdvice(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	next := secondOpenStage(t, e, pipelineID, stage)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)
	deal := e.SeedDeal(t, "Renewal", pipelineID, stage, &e.Rep1)
	e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
		WHERE id = $1`, deal, org.UUID, org360Clock.AddDate(0, 0, -200))

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	dismissed := stalledFingerprints(*view.Suggestions)
	if len(dismissed) != 1 {
		t.Fatalf("got %d stalled suggestions, want the one seeded deal", len(dismissed))
	}
	if err := svc.DismissSuggestion(rep, org, dismissed[0]); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if quiet, err := svc.Assemble(rep, org); err != nil {
		t.Fatalf("re-assemble: %v", err)
	} else if left := stalledFingerprints(*quiet.Suggestions); len(left) != 0 {
		t.Fatalf("%d stalled suggestions right after dismissing the only one", len(left))
	}

	// The deal moves to another OPEN stage, so it stays a candidate. Nothing logs
	// an activity, so the stall rule's own inputs are unchanged and the deal is
	// still stalled — but it has plainly been worked.
	if _, err := e.Deals.AdvanceDeal(e.As(e.Rep1, nil, AdminPerms),
		ids.From[ids.DealKind](deal), deals.AdvanceDealInput{ToStageID: next}); err != nil {
		t.Fatalf("advancing the deal: %v", err)
	}

	after, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble after the advance: %v", err)
	}
	fresh := stalledFingerprints(*after.Suggestions)
	if len(fresh) != 1 {
		t.Fatalf("the advanced deal raised %d stalled suggestions, want 1 — the "+
			"dismissal outlasted a stage the rep never judged", len(fresh))
	}
	if fresh[0] == dismissed[0] {
		t.Error("the advanced deal reuses the dismissed fingerprint")
	}

	// Now the other half. The rep judges the new advice, and then someone opens the
	// stage picker and re-selects the stage the deal is already in. That writes a
	// history row and is not work, so the advice must stay dismissed.
	//
	// The dismissal has to happen HERE, after the advance: asserting against the
	// pre-advance fingerprint would hold whether or not the no-op row is counted,
	// which is a test that cannot fail for the behaviour it names.
	if err := svc.DismissSuggestion(rep, org, fresh[0]); err != nil {
		t.Fatalf("dismiss the post-advance advice: %v", err)
	}
	if _, err := e.Deals.AdvanceDeal(e.As(e.Rep1, nil, AdminPerms),
		ids.From[ids.DealKind](deal), deals.AdvanceDealInput{ToStageID: next}); err != nil {
		t.Fatalf("re-selecting the same stage: %v", err)
	}

	settled, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble after the no-op: %v", err)
	}
	if left := stalledFingerprints(*settled.Suggestions); len(left) != 0 {
		t.Errorf("re-selecting the same stage handed back %d dismissed suggestions — "+
			"opening the stage picker and changing nothing counted as work", len(left))
	}
}

// secondOpenStage finds another open stage in the pipeline, so a deal can be
// advanced without leaving the open set the suggestion rules read.
func secondOpenStage(t *testing.T, e *Env, pipelineID ids.PipelineID, notThisOne ids.StageID) ids.StageID {
	t.Helper()
	p, err := e.Deals.GetPipeline(e.Admin(), pipelineID)
	if err != nil {
		t.Fatalf("reading the pipeline: %v", err)
	}
	// Dereferencing a nil Stages would surface as a panic somewhere unrelated;
	// the fixture's own precondition should say what is missing.
	if p.Stages == nil {
		t.Fatal("the pipeline read carried no stages, so no advance target exists")
	}
	for _, st := range *p.Stages {
		id := ids.From[ids.StageKind](ids.UUID(st.Id))
		if st.Semantic == "open" && id != notThisOne {
			return id
		}
	}
	t.Fatal("the default pipeline has only one open stage, so a deal cannot advance within it")
	return ids.StageID{}
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

// The no-next-step fingerprint covers every open deal, not the listed ones.
//
// Closing a deal the card never showed still changes the situation the rep
// judged, so a dismissal has to re-arm. A fingerprint built from a fetched page
// would stay in force over a pipeline the account no longer has.
func TestNoNextStepFingerprintFollowsUnlistedDeals(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	pipelineID, stage, _ := DealFixture(t, e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	// Two stalled deals — under the card's cap, so the no-next-step row is
	// listed too — plus a healthy third the stalled listing never mentions.
	var healthy ids.UUID
	for i := range 3 {
		deal := e.SeedDeal(t, fmt.Sprintf("Deal %d", i), pipelineID, stage, &e.Rep1)
		idleSince := org360Clock.AddDate(0, 0, -200)
		if i == 2 {
			idleSince = org360Clock.AddDate(0, 0, -1)
			healthy = deal
		}
		e.WsExec(t, `UPDATE deal SET organization_id = $2, created_at = $3, last_activity_at = $3
			WHERE id = $1`, deal, org.UUID, idleSince)
	}

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	before := fingerprintOfKind(t, *view.Suggestions, "no_next_step")

	e.WsExec(t, `UPDATE deal SET status = 'lost', lost_reason = 'other', closed_at = $2 WHERE id = $1`,
		healthy, org360Clock)

	after, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("re-assemble: %v", err)
	}
	if got := fingerprintOfKind(t, *after.Suggestions, "no_next_step"); got == before {
		t.Error("closing a deal the stalled listing never named left the no-next-step " +
			"fingerprint unchanged — a dismissal would stay in force over a pipeline the account no longer has")
	}
}

// fingerprintOfKind finds the one suggestion of a kind, failing loudly when the
// scenario did not produce it — a test that silently found nothing would pass by
// comparing two empty strings.
func fingerprintOfKind(
	t *testing.T, found []crmcontracts.Organization360Suggestion, kind string,
) string {
	t.Helper()
	for _, suggestion := range found {
		if string(suggestion.Kind) == kind {
			return suggestion.Fingerprint
		}
	}
	t.Fatalf("no %q suggestion among %+v", kind, found)
	return ""
}

// A dismissal belongs to the rep who made it. One rep judging a suggestion
// must not decide it for their colleague, who has never seen it.
func TestSuggestionDismissalIsPerUser(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	seedUnansweredOutbound(t, e, org.UUID)

	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)
	rep2 := e.As(e.Rep2, []ids.UUID{e.Team1}, org360RepPerms)

	view, err := svc.Assemble(rep1, org)
	if err != nil {
		t.Fatalf("assemble as the first rep: %v", err)
	}
	if view.Suggestions == nil {
		t.Fatalf("suggestions absent (sections_omitted=%v)", view.SectionsOmitted)
	}
	// Named by kind rather than taken at [0]: a fixture that later grows a deal
	// would put a stalled-deal row first, and this would silently start testing
	// per-user-ness of a rule it was not written for.
	fingerprint := fingerprintOfKind(t, *view.Suggestions, "no_reply")

	if err := svc.DismissSuggestion(rep1, org, fingerprint); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	after, err := svc.Assemble(rep1, org)
	if err != nil {
		t.Fatalf("re-assemble as the first rep: %v", err)
	}
	for _, suggestion := range *after.Suggestions {
		if suggestion.Fingerprint == fingerprint {
			t.Error("the dismissed suggestion came back for the rep who dismissed it")
		}
	}

	colleague, err := svc.Assemble(rep2, org)
	if err != nil {
		t.Fatalf("assemble as the second rep: %v", err)
	}
	if colleague.Suggestions == nil {
		t.Fatal("suggestions absent for the second rep")
	}
	found := false
	for _, suggestion := range *colleague.Suggestions {
		if suggestion.Fingerprint == fingerprint {
			found = true
		}
	}
	if !found {
		t.Error("the second rep lost a suggestion they never saw — the dismissal is not per user")
	}
}

// The dismissal is keyed on the EVIDENCE, so it stays in force while the
// situation holds and stops applying when the situation is genuinely a new
// one. Without that, the surface would get quieter the longer it ran.
func TestSuggestionDismissalReArmsWhenTheEvidenceChanges(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	seedUnansweredOutbound(t, e, org.UUID)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	first, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	dismissedFingerprint := fingerprintOfKind(t, *first.Suggestions, "no_reply")
	if err := svc.DismissSuggestion(rep, org, dismissedFingerprint); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// A second unanswered outbound message: the same rule, a different message,
	// so this is a new fact about the account rather than the one already judged.
	seedUnansweredOutbound(t, e, org.UUID)

	again, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("re-assemble: %v", err)
	}
	if again.Suggestions == nil {
		t.Fatal("suggestions absent")
	}
	// It has to be a NO_REPLY with a different fingerprint. Asserting that
	// something fired, or that nothing carries the dismissed fingerprint, both
	// pass on a suggestion from another rule entirely — so fingerprintOfKind
	// fatals when the rule under test stayed silenced.
	reArmed := fingerprintOfKind(t, *again.Suggestions, "no_reply")
	if reArmed == dismissedFingerprint {
		t.Error("the re-armed suggestion carries the dismissed fingerprint")
	}
}

// A dismissal names a record, so it is a read: an account this caller cannot
// see must refuse rather than confirm it exists.
func TestSuggestionDismissalRefusesAnInvisibleAccount(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	// Owned by Rep3, who sits in Team2 — outside Rep1's team-scoped row scope.
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Someone else's account", &e.Rep3))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	// The record gate runs before anything else, so this is a 404 rather than the
	// silent success an unmatched fingerprint gets on a visible account.
	err := svc.DismissSuggestion(rep, org, wellFormedFingerprint)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("dismiss on an invisible account → %v, want ErrNotFound (existence-hiding)", err)
	}
	if count := e.WsCount(t, `SELECT count(*) FROM suggestion_dismissal`); count != 0 {
		t.Errorf("suggestion_dismissal rows = %d after a refused dismissal, want 0", count)
	}
}

// An agent has no opinion to record. Consuming a human's dismissal on their
// behalf would silence advice that human never saw.
func TestSuggestionDismissalRefusesAnAgent(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	err := svc.DismissSuggestion(agentWithOrgRead(e), org, wellFormedFingerprint)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent dismissal → %v, want ErrPermissionDenied", err)
	}
}

// A fingerprint the account does not currently raise stores nothing.
//
// That is what bounds the table: without it, every distinct well-formed value a
// caller sends is a row nothing will ever collect — an authenticated write sink
// in their own tenant. It answers success because there is genuinely nothing to
// dismiss, and saying which reason would answer a question the caller has no
// business asking.
func TestSuggestionDismissalStoresNothingForASuggestionTheAccountDoesNotRaise(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	// A quiet account raises nothing, so no fingerprint can match.
	for _, forged := range []string{wellFormedFingerprint, strings.Repeat("a", 64)} {
		if err := svc.DismissSuggestion(rep, org, forged); err != nil {
			t.Errorf("dismiss %q → %v, want success with nothing written", forged, err)
		}
	}
	if count := e.WsCount(t, `SELECT count(*) FROM suggestion_dismissal`); count != 0 {
		t.Errorf("suggestion_dismissal rows = %d, want 0 — the endpoint is a write sink", count)
	}

	// And the real one still stores, so the check is not simply refusing everything.
	seedUnansweredOutbound(t, e, org.UUID)
	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.Suggestions == nil {
		t.Fatal("suggestions absent")
	}
	served := fingerprintOfKind(t, *view.Suggestions, "no_reply")
	if err := svc.DismissSuggestion(rep, org, served); err != nil {
		t.Fatalf("dismiss a served suggestion: %v", err)
	}
	if count := e.WsCount(t, `SELECT count(*) FROM suggestion_dismissal`); count != 1 {
		t.Errorf("suggestion_dismissal rows = %d after dismissing a served suggestion, want 1", count)
	}
}

// A malformed fingerprint is a stated 422, not the silent no-op above — a client
// that mangled the value must be able to tell that from a hit. The shape is
// checked after the record gate, so the refusal cannot double as an existence
// probe.
func TestSuggestionDismissalRefusesAMalformedFingerprint(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	for _, refused := range []string{"", "   ", "not-a-digest", strings.ToUpper(wellFormedFingerprint)} {
		var detailed *httperr.DetailedError
		err := svc.DismissSuggestion(rep, org, refused)
		if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
			t.Errorf("fingerprint %q → %v, want a 422 validation error", refused, err)
		}
	}
	if count := e.WsCount(t, `SELECT count(*) FROM suggestion_dismissal`); count != 0 {
		t.Errorf("suggestion_dismissal rows = %d after refused dismissals, want 0", count)
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

// seedContractEnded writes the open signal the extraction task produces when a
// thread says the relationship is over.
func seedContractEnded(t *testing.T, e *Env, org ids.UUID) {
	t.Helper()
	SeedRow(t, OwnerConn(t), `INSERT INTO signal
		(id, workspace_id, kind, source_channel, entity_type, entity_id, resolved_org_id,
		 resolution_state, severity, summary, status, detected_at, source, captured_by)
		VALUES ($1, $2, 'contract_ended', 'derived', 'organization', '`+org.String()+`',
		        '`+org.String()+`', 'resolved', 'warn',
		        'They wrote that the contract ends on 31 July.', 'open',
		        '2026-05-20T09:00:00Z', 'signal-scan', 'agent:contract_ended')`, e.WS)
}

// The failure this whole surface was named for: an account holding a mail that
// ends the contract, filed under a stage that says the relationship is live.
//
// Both facts were already in the record and nothing put them next to each
// other. The page states the disagreement and leaves which side is wrong to
// the reader — but it must state it FIRST, because acting on a stage that is
// wrong is worse than not acting at all.
func TestTheRecordAndItsOwnMailAreShownToDisagree(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	e.WsExec(t, `UPDATE organization SET lifecycle = 'customer' WHERE id = $1`, org.UUID)
	seedContractEnded(t, e, org.UUID)
	// Advice that would otherwise lead, so leading is a choice this makes and
	// not the only thing left standing.
	seedUnansweredOutbound(t, e, org.UUID)

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	found := *view.Suggestions
	if len(found) == 0 || string(found[0].Kind) != "lifecycle_conflict" {
		t.Fatalf("the card leads with %v, want lifecycle_conflict — the record contradicts "+
			"its own correspondence, and no other advice on it can be trusted until that is settled",
			kindsOf(found))
	}
	if !strings.Contains(found[0].Reason, "customer") {
		t.Errorf("the reason is %q, want it to name the stage the mail contradicts — "+
			"a conflict a reader cannot see both sides of is not one they can settle",
			found[0].Reason)
	}
}

// A stage that already reads as over is not in conflict with the mail that
// says so; it is that mail's conclusion. Firing there would hand every closed
// account a permanent card nobody can clear.
func TestAnEndedContractDoesNotContradictAnEndedRelationship(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	e.WsExec(t, `UPDATE organization SET lifecycle = 'former_customer' WHERE id = $1`, org.UUID)
	seedContractEnded(t, e, org.UUID)

	view, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, suggestion := range *view.Suggestions {
		if string(suggestion.Kind) == "lifecycle_conflict" {
			t.Fatalf("the card claims a conflict on an account already filed as former_customer")
		}
	}
}

// kindsOf names what a card actually offered, so a failure says what was there
// instead of only what was missing.
func kindsOf(found []crmcontracts.Organization360Suggestion) []string {
	kinds := make([]string, 0, len(found))
	for _, suggestion := range found {
		kinds = append(kinds, string(suggestion.Kind))
	}
	return kinds
}

// A caller who may not read signals is told nothing about them, not told there
// is nothing. The rule reads a record the reader has no right to, so it stays
// silent rather than leaking its existence through advice.
func TestTheConflictStaysSilentWithoutTheSignalGrant(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	e.WsExec(t, `UPDATE organization SET lifecycle = 'customer' WHERE id = $1`, org.UUID)
	seedContractEnded(t, e, org.UUID)

	blind := org360RepPerms
	blind.Objects = make(map[string]principal.ObjectGrant, len(org360RepPerms.Objects))
	for object, grant := range org360RepPerms.Objects {
		if object == "signal" {
			continue
		}
		blind.Objects[object] = grant
	}
	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, blind), org)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, suggestion := range *view.Suggestions {
		if string(suggestion.Kind) == "lifecycle_conflict" {
			t.Fatalf("a caller without the signal grant was shown advice derived from one")
		}
	}
}

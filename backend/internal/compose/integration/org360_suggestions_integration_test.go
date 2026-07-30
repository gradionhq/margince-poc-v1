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

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

	// Thirty notes, every one NEWER than the unanswered email, so the newest 25
	// timeline entries the section carries are all notes.
	for i := range 30 {
		note := ids.NewV7()
		if _, err := owner.Exec(context.Background(), `INSERT INTO activity
			(id, workspace_id, kind, subject, occurred_at, created_at, source, captured_by)
			VALUES ($1, $2, 'note', $3, $4, $4, 'manual', 'human:x')`,
			note, e.WS, fmt.Sprintf("note %d", i), org360Clock.AddDate(0, 0, -i)); err != nil {
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
	found := false
	for _, suggestion := range *view.Suggestions {
		if suggestion.Kind == "no_reply" {
			found = true
		}
	}
	if !found {
		t.Errorf("no no_reply suggestion with 30 newer notes on the account: %+v", *view.Suggestions)
	}
}

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
	if listed == 0 || listed >= openDeals {
		t.Fatalf("listed %d suggestions for %d stalled deals, want a bounded handful", listed, openDeals)
	}
	// The dropped total plus the listed rows must account for every one of them.
	// The no-next-step rule also fires here (nothing is scheduled), so the
	// suggestion count is the stalled deals plus that one.
	if listed+view.SuggestionsDropped != openDeals+1 {
		t.Errorf("listed %d + dropped %d = %d, want the %d suggestions this account has",
			listed, view.SuggestionsDropped, listed+view.SuggestionsDropped, openDeals+1)
	}

	// Stalled deals lead, so the advice the cap cuts is the no-next-step row —
	// a rep with eight stuck deals needs them unstuck, not a note that nothing
	// is scheduled.
	for _, suggestion := range (*view.Suggestions)[:listed] {
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
	if view.Suggestions == nil || len(*view.Suggestions) == 0 {
		t.Fatalf("no suggestion for a 3-week-old unanswered outbound email (sections_omitted=%v)",
			view.SectionsOmitted)
	}
	fingerprint := (*view.Suggestions)[0].Fingerprint

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
	if err := svc.DismissSuggestion(rep, org, (*first.Suggestions)[0].Fingerprint); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// A second unanswered outbound message: the same rule, a different message,
	// so this is a new fact about the account rather than the one already judged.
	seedUnansweredOutbound(t, e, org.UUID)

	again, err := svc.Assemble(rep, org)
	if err != nil {
		t.Fatalf("re-assemble: %v", err)
	}
	if again.Suggestions == nil || len(*again.Suggestions) == 0 {
		t.Fatal("a newer unanswered message raised nothing — the dismissal buried the rule, not the situation")
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

// A fingerprint the read never served names no suggestion. Storing arbitrary
// text would make this endpoint a write-anything store, so its shape is checked
// — after the record gate, so the refusal cannot double as an existence probe.
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

// The rules read the sections the caller was actually shown, so a reader
// without the activity grant gets no advice derived from an absence.
func TestSuggestionsAreOmittedWithoutTheActivityGrant(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	seedUnansweredOutbound(t, e, org.UUID)

	// Organization read only: no activity grant at all.
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

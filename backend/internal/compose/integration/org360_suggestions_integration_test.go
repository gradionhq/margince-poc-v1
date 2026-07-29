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
	"errors"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

	err := svc.DismissSuggestion(rep, org, "any-fingerprint")
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

	err := svc.DismissSuggestion(agentWithOrgRead(e), org, "any-fingerprint")
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent dismissal → %v, want ErrPermissionDenied", err)
	}
}

// A blank fingerprint names no suggestion. Storing it would silence nothing
// while reporting success, so it is refused at the door.
func TestSuggestionDismissalRefusesABlankFingerprint(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	var detailed *httperr.DetailedError
	err := svc.DismissSuggestion(rep, org, "   ")
	if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
		t.Errorf("blank fingerprint → %v, want a 422 validation error", err)
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

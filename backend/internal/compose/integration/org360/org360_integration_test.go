// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package org360

// The company view is one read that hands back nine sections at once, so
// it is nine chances to out-see the dedicated endpoint each section
// summarizes. What this suite pins:
//
//   - the account itself is gated like any other record (cross-tenant and
//     out-of-row-scope both answer not-found, indistinguishably);
//   - a section the caller may not read is OMITTED and NAMED, never
//     returned as an empty list that reads like "there is none";
//   - the contact list, the deal figures and the timeline each carry the
//     caller's row scope, so the composite cannot become the side channel;
//   - the visit baseline moves only through the explicit acknowledgment,
//     monotonically, and per user.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	org360svc "github.com/gradionhq/margince/backend/internal/compose/org360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// org360Clock is the read's pinned instant. Strength half-lives and the
// stall window are both duration comparisons against "now", so a real
// clock would let a fixture drift across a boundary between seeding and
// reading.
var org360Clock = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// org360Service builds the composite read over the harness pool with the
// pinned clock.
func org360Service(e *integration.Env) *org360svc.Service {
	return org360svc.NewService(e.Pool, people.NewStore(e.Pool), approvals.NewService(e.Pool),
		func() time.Time { return org360Clock })
}

// org360SignalPerms is the same rep plus the signal read grant (the helper
// the graph suite already keeps, org360_graph_integration_test.go). Separate
// from integration.AccountRepPerms rather than folded into it because several
// tests read that fixture as "a rep who cannot see signals" to prove a section
// is withheld — granting it there made those pass without testing anything.
var org360SignalPerms = withSignalRead(integration.AccountRepPerms)

// org360NoDealPerms is the same rep with the deal grant taken away — the
// fixture that proves omission is distinguishable from emptiness.
var org360NoDealPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Read: true},
		"person":       {Read: true},
		"activity":     {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

func TestOrganization360OmitsSectionsTheCallerMayNotRead(t *testing.T) {
	e := integration.Setup(t)
	svc := org360Service(e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	orgID := ids.From[ids.OrganizationKind](org)

	full, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms), orgID)
	if err != nil {
		t.Fatalf("assemble as a fully-granted rep: %v", err)
	}
	if full.Deals == nil {
		t.Error("deals section absent for a rep who holds the deal grant")
	}
	if slices.Contains(full.SectionsOmitted, "deals") {
		t.Errorf("sections_omitted = %v, must not name deals for a rep who can read them", full.SectionsOmitted)
	}

	partial, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360NoDealPerms), orgID)
	if err != nil {
		t.Fatalf("assemble as a rep without the deal grant: %v", err)
	}
	if partial.Deals != nil {
		t.Error("deals section present for a rep who cannot read deals — an omitted section must be absent, not empty")
	}
	if !slices.Contains(partial.SectionsOmitted, "deals") {
		t.Errorf("sections_omitted = %v, want it to name deals — empty and forbidden must be distinguishable",
			partial.SectionsOmitted)
	}
	// The account itself is still served: losing one grant narrows the
	// page, it does not refuse it.
	if partial.Organization.DisplayName != "Acme" {
		t.Errorf("organization display_name = %q, want Acme", partial.Organization.DisplayName)
	}
	if partial.AsOf != org360Clock {
		t.Errorf("as_of = %v, want the read's pinned instant %v", partial.AsOf, org360Clock)
	}
}

func TestOrganization360HidesAnAccountOutsideTheCallersRowScope(t *testing.T) {
	e := integration.Setup(t)
	svc := org360Service(e)
	// Rep3 sits in the other team, so Rep1's team scope cannot reach it.
	theirs := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Other Team Account", &e.Rep3))

	_, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms), theirs)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("assemble out of row scope → %v, want ErrNotFound (existence-hiding)", err)
	}
	// The positive control: the same call on the caller's own account works,
	// so the gate narrows scope rather than breaking the read.
	mine := ids.From[ids.OrganizationKind](e.SeedOrg(t, "My Account", &e.Rep1))
	if _, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms), mine); err != nil {
		t.Errorf("assemble on the caller's own account: %v", err)
	}
}

func TestOrganization360ContactsCarryStrengthRolesAndConsent(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)
	admin := e.Admin()

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Dana Buyer", &e.Rep1)
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, contact, org)
	e.WsExec(t, `INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
		VALUES ($1, $2, 'dana@acme.test', true, 'manual', 'human:x')`, e.WS, contact)

	// Two qualifying interactions inside the §4 window, one each way, so
	// the score is non-zero and reciprocity is balanced.
	for _, direction := range []string{"inbound", "outbound"} {
		activity := integration.SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'terms', '2026-05-30T09:00:00Z', '`+direction+`', 'manual', 'human:x')`, e.WS)
		integration.LinkActivity(t, owner, e.WS, activity, "person", contact)
	}

	purpose := integration.SeedRow(t, owner, `INSERT INTO consent_purpose (id, workspace_id, key, label)
		VALUES ($1, $2, 'marketing_email', 'Marketing email')`, e.WS)
	e.WsExec(t, `INSERT INTO person_consent (workspace_id, person_id, purpose_id, state)
		VALUES ($1, $2, $3, 'granted')`, e.WS, contact, purpose)

	view, err := svc.Assemble(admin, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.People == nil || len(view.People.Data) != 1 {
		t.Fatalf("people section = %+v, want exactly one contact", view.People)
	}
	card := view.People.Data[0]
	if card.FullName != "Dana Buyer" {
		t.Errorf("contact full_name = %q, want Dana Buyer", card.FullName)
	}
	if card.PrimaryEmail == nil || *card.PrimaryEmail != "dana@acme.test" {
		t.Errorf("contact primary_email = %v, want dana@acme.test", card.PrimaryEmail)
	}
	if card.Strength.Score == 0 {
		t.Error("contact strength score = 0 after two qualifying interactions in the window")
	}
	if got := card.Consent["marketing_email"]; got != crmcontracts.Organization360ContactConsentGranted {
		t.Errorf("consent[marketing_email] = %q, want granted", got)
	}
	// The account roll-up is the strongest contact's score, and it names
	// who carries it — a number with nobody behind it is not actionable.
	if view.Strength == nil {
		t.Fatal("strength section absent for an admin")
	}
	if view.Strength.ContactCount != 1 {
		t.Errorf("strength contact_count = %d, want 1", view.Strength.ContactCount)
	}
	if view.Strength.ContributorPersonId == nil || ids.UUID(*view.Strength.ContributorPersonId) != contact {
		t.Errorf("strength contributor_person_id = %v, want the account's one contact %v",
			view.Strength.ContributorPersonId, contact)
	}
	if view.Strength.Score != card.Strength.Score {
		t.Errorf("account strength %d disagrees with its only contact's %d",
			view.Strength.Score, card.Strength.Score)
	}
}

// A purpose the person has no row for must still appear, as unknown:
// outbound is default-deny per purpose, and a missing key would let a
// caller read absence as permission.
func TestOrganization360ConsentReportsEveryPurposeEvenWithoutARow(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Silent Contact", &e.Rep1)
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, contact, org)
	integration.SeedRow(t, owner, `INSERT INTO consent_purpose (id, workspace_id, key, label)
		VALUES ($1, $2, 'product_updates', 'Product updates')`, e.WS)

	view, err := svc.Assemble(e.Admin(), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.People == nil || len(view.People.Data) != 1 {
		t.Fatalf("people section = %+v, want exactly one contact", view.People)
	}
	got, present := view.People.Data[0].Consent["product_updates"]
	if !present {
		t.Fatal("consent map omits a purpose the person has no row for — absence must not read as permission")
	}
	if got != crmcontracts.Organization360ContactConsentUnknown {
		t.Errorf("consent[product_updates] = %q, want unknown", got)
	}
}

// A contact the caller cannot read contributes nothing: not to the list,
// not to the count, and not to the account's warmth.
func TestOrganization360ContactsHonorTheCallersRowScope(t *testing.T) {
	e := integration.Setup(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	for _, person := range []ids.UUID{mine, theirs} {
		e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, person, org)
	}

	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.People == nil || len(view.People.Data) != 1 {
		t.Fatalf("people section = %+v, want only the contact inside the caller's row scope", view.People)
	}
	if ids.UUID(view.People.Data[0].PersonId) != mine {
		t.Errorf("contact = %v, want the caller's own %v", view.People.Data[0].PersonId, mine)
	}
	if view.Strength != nil && view.Strength.ContactCount != 1 {
		t.Errorf("strength contact_count = %d, want 1 — the roll-up must not out-see the contact list",
			view.Strength.ContactCount)
	}
}

// The transport is thin, but "thin" is a claim: it has to bind the path id,
// let the service's gates decide, and hand back the assembled body — and a
// native workspace must reach it, not be refused by the overlay guard that
// only exists for mirror-backed ones.
func TestOrganization360TransportServesANativeWorkspace(t *testing.T) {
	e := integration.Setup(t)
	handlers := org360svc.NewHandlers(org360Service(e),
		func(context.Context) (bool, error) { return false, nil })
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+org.String()+"/360", nil)
	handlers.GetOrganization360(rec, req.WithContext(rep), crmcontracts.Id(org.UUID))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var body crmcontracts.Organization360
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the 360 body: %v", err)
	}
	if body.Organization.DisplayName != "Acme" {
		t.Errorf("organization display_name = %q, want Acme", body.Organization.DisplayName)
	}
	if !body.AsOf.Equal(org360Clock) {
		t.Errorf("as_of = %v, want the read's pinned instant %v", body.AsOf, org360Clock)
	}

	rec = httptest.NewRecorder()
	ack := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+org.String()+"/view-ack", nil)
	handlers.AcknowledgeOrganizationView(rec, ack.WithContext(rep), crmcontracts.Id(org.UUID))
	if rec.Code != http.StatusOK {
		t.Fatalf("view-ack status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var stored crmcontracts.RecordViewAck
	if err := json.Unmarshal(rec.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decoding the ack body: %v", err)
	}
	if !stored.LastViewedAt.Equal(org360Clock) {
		t.Errorf("last_viewed_at = %v, want %v", stored.LastViewedAt, org360Clock)
	}
}

// A task reaches this account through a contact the caller can read, while
// also being linked to another team's deal. The task belongs on the page;
// that deal's id does not.
func TestOrganization360NextStepsHideALinkedDealOutOfRowScope(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)
	pipeline, stage, _ := integration.DealFixture(t, e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, mine, org)
	theirDeal := e.SeedDeal(t, "Other team deal", pipeline, stage, &e.Rep3)
	e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, theirDeal, org)

	task := integration.SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, is_done, source, captured_by)
		VALUES ($1, $2, 'task', 'Send the renewal paperwork', now(), false, 'manual', 'human:x')`, e.WS)
	integration.LinkActivity(t, owner, e.WS, task, "person", mine)
	integration.LinkActivity(t, owner, e.WS, task, "deal", theirDeal)

	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.NextSteps == nil || len(view.NextSteps.Data) != 1 {
		t.Fatalf("next_steps = %+v, want the one open task reachable through the visible contact", view.NextSteps)
	}
	step := view.NextSteps.Data[0]
	if step.LinkedDealId != nil {
		t.Errorf("linked_deal_id = %v for a deal outside the caller's row scope — the task is theirs to see, that deal is not",
			step.LinkedDealId)
	}
	if step.LinkedPersonId == nil || ids.UUID(*step.LinkedPersonId) != mine {
		t.Errorf("linked_person_id = %v, want the visible contact %v", step.LinkedPersonId, mine)
	}
}

// "No meeting is booked" and "you cannot see the calendar" are different
// sentences, and a page that renders them the same tells a rep to book a
// meeting that already exists. The section proves it holds the distinction.
func TestOrganization360NextMeetingSeparatesNoneFromWithheld(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	granted := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms)

	// Nothing booked, and the caller holds the activity grant: the answer is a
	// fact about the account, so the section is present and null.
	view, err := svc.Assemble(granted, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.NextMeeting != nil {
		t.Errorf("next_meeting = %+v, want null — nothing is scheduled", view.NextMeeting)
	}
	if slices.Contains(view.SectionsOmitted, "next_meeting") {
		t.Error("next_meeting was named as omitted while the caller holds the activity grant — that reads as 'hidden from you' for an account that simply has no meeting")
	}

	// A meeting in the past is not the next one. Seeded before the future meeting
	// so an ordering that ignored occurred_at would return this row.
	past := seedMeeting(t, owner, e.WS, "Kickoff, already held", org360Clock.Add(-48*time.Hour))
	integration.LinkActivity(t, owner, e.WS, past, "organization", org)
	future := seedMeeting(t, owner, e.WS, "Renewal review", org360Clock.Add(72*time.Hour))
	integration.LinkActivity(t, owner, e.WS, future, "organization", org)

	view, err = svc.Assemble(granted, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble after seeding: %v", err)
	}
	if view.NextMeeting == nil {
		t.Fatal("next_meeting = null with a meeting booked in three days")
	}
	if ids.UUID(view.NextMeeting.ActivityId) != future {
		t.Errorf("next_meeting = %q, want the future one — a meeting's place in time is when it happens, not when it was entered",
			view.NextMeeting.Subject)
	}

	// Without the activity grant the section is absent and NAMED, which is the
	// other half of the distinction.
	withheld := e.As(e.Rep2, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  map[string]principal.ObjectGrant{"organization": {Read: true}},
		RowScope: principal.RowScopeAll,
	})
	view, err = svc.Assemble(withheld, ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble without the activity grant: %v", err)
	}
	if view.NextMeeting != nil {
		t.Error("next_meeting was served to a caller with no activity grant")
	}
	if !slices.Contains(view.SectionsOmitted, "next_meeting") {
		t.Error("next_meeting was withheld without being named in sections_omitted, so the page cannot say why it is missing")
	}
}

// The meeting is reachable through a visible contact, so the caller may see
// that it exists. Who ELSE was in the room is a separate question, answered per
// person — otherwise the composite becomes the side channel that hands out a
// colleague's contacts.
func TestOrganization360NextMeetingParticipantsHonorRowScope(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	svc := org360Service(e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)

	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Another Team's Contact", &e.Rep3)
	for _, person := range []ids.UUID{mine, theirs} {
		e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, person, org)
	}

	meeting := seedMeeting(t, owner, e.WS, "Renewal review", org360Clock.Add(24*time.Hour))
	integration.LinkActivity(t, owner, e.WS, meeting, "person", mine)
	for _, person := range []ids.UUID{mine, theirs} {
		e.WsExec(t, `INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
			VALUES ($1, $2, $3, 'attendee')`, e.WS, meeting, person)
	}
	// The visible contact ALSO holds a second role. uq_activity_participant is
	// unique on (activity, role, person), so one person legitimately has several
	// rows on one meeting — a captured mail makes its sender both `from` and
	// `attendee`. Without this the fixture has one row per person and cannot
	// tell a correct answer from one that lists somebody once per role.
	e.WsExec(t, `INSERT INTO activity_participant (workspace_id, activity_id, person_id, role)
		VALUES ($1, $2, $3, 'from')`, e.WS, meeting, mine)

	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AccountRepPerms), ids.From[ids.OrganizationKind](org))
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if view.NextMeeting == nil {
		t.Fatal("next_meeting = null for a meeting reachable through a visible contact")
	}
	if len(view.NextMeeting.Participants) != 1 {
		t.Fatalf("participants = %+v, want the one contact this caller can read, named once however many roles they hold",
			view.NextMeeting.Participants)
	}
	if ids.UUID(view.NextMeeting.Participants[0].PersonId) != mine {
		t.Errorf("participants named %q — a meeting visible through one contact must not disclose a colleague's",
			view.NextMeeting.Participants[0].DisplayName)
	}
}

// seedMeeting books one meeting at a chosen instant. SeedRow binds only the id
// and the workspace, and a meeting's whole identity here is WHEN it is.
func seedMeeting(t *testing.T, owner *pgx.Conn, ws ids.UUID, subject string, at time.Time) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'meeting', $3, $4, 'manual', 'human:x')`, id, ws, subject, at); err != nil {
		t.Fatalf("seeding a meeting: %v", err)
	}
	return id
}

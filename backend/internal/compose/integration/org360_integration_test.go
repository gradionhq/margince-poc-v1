// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

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
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/org360"
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
func org360Service(e *Env) *org360.Service {
	return org360.NewService(e.Pool, people.NewStore(e.Pool), approvals.NewService(e.Pool),
		func() time.Time { return org360Clock })
}

// org360RepPerms is a team-scoped rep holding every grant the sections
// ask for. The interesting failures are row-scope ones, and an unbounded
// admin short-circuits every scope clause, so the fixture must be bounded.
var org360RepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"organization": {Read: true},
		"person":       {Create: true, Read: true, Update: true},
		"deal":         {Create: true, Read: true, Update: true},
		"activity":     {Create: true, Read: true, Update: true},
		"pipeline":     {Read: true},
		"tag":          {Read: true},
		"list":         {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

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
	e := Setup(t)
	svc := org360Service(e)
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	orgID := ids.From[ids.OrganizationKind](org)

	full, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), orgID)
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
	e := Setup(t)
	svc := org360Service(e)
	// Rep3 sits in the other team, so Rep1's team scope cannot reach it.
	theirs := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Other Team Account", &e.Rep3))

	_, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), theirs)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("assemble out of row scope → %v, want ErrNotFound (existence-hiding)", err)
	}
	// The positive control: the same call on the caller's own account works,
	// so the gate narrows scope rather than breaking the read.
	mine := ids.From[ids.OrganizationKind](e.SeedOrg(t, "My Account", &e.Rep1))
	if _, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), mine); err != nil {
		t.Errorf("assemble on the caller's own account: %v", err)
	}
}

func TestOrganization360ContactsCarryStrengthRolesAndConsent(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
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
		activity := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'terms', '2026-05-30T09:00:00Z', '`+direction+`', 'manual', 'human:x')`, e.WS)
		LinkActivity(t, owner, e.WS, activity, "person", contact)
	}

	purpose := SeedRow(t, owner, `INSERT INTO consent_purpose (id, workspace_id, key, label)
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
	e := Setup(t)
	owner := OwnerConn(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	contact := e.SeedPerson(t, "Silent Contact", &e.Rep1)
	e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
		VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, contact, org)
	SeedRow(t, owner, `INSERT INTO consent_purpose (id, workspace_id, key, label)
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
	e := Setup(t)
	svc := org360Service(e)

	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := e.SeedPerson(t, "My Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	for _, person := range []ids.UUID{mine, theirs} {
		e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, person, org)
	}

	view, err := svc.Assemble(e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms), ids.From[ids.OrganizationKind](org))
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

func TestOrganizationViewAckIsMonotonicAndPerUser(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	first, err := svc.Acknowledge(rep1, org)
	if err != nil {
		t.Fatalf("first acknowledgment: %v", err)
	}
	if !first.LastViewedAt.Equal(org360Clock) {
		t.Errorf("last_viewed_at = %v, want the pinned instant %v", first.LastViewedAt, org360Clock)
	}

	// A second tab whose clock lags must not rewind the mark: the upsert
	// keeps the later of the two.
	lagging := org360.NewService(e.Pool, people.NewStore(e.Pool), approvals.NewService(e.Pool),
		func() time.Time { return org360Clock.Add(-time.Hour) })
	second, err := lagging.Acknowledge(rep1, org)
	if err != nil {
		t.Fatalf("lagging acknowledgment: %v", err)
	}
	if !second.LastViewedAt.Equal(org360Clock) {
		t.Errorf("last_viewed_at = %v after a lagging ack, want the earlier %v to be kept",
			second.LastViewedAt, org360Clock)
	}

	// Rep2 shares Rep1's team and can read the same account, but has never
	// visited it: the baseline is per user, not per record.
	rep2 := e.As(e.Rep2, []ids.UUID{e.Team1}, org360RepPerms)
	view, err := svc.Assemble(rep2, org)
	if err != nil {
		t.Fatalf("assemble as the second rep: %v", err)
	}
	if view.SinceLastVisit == nil {
		t.Fatal("since_last_visit absent for a rep holding the activity grant")
	}
	if view.SinceLastVisit.BaselineAt != nil {
		t.Errorf("baseline_at = %v for a rep who never acknowledged this account, want null",
			view.SinceLastVisit.BaselineAt)
	}
}

// The 360 is a read: it must never advance the mark it reports against,
// or the "what changed" answer destroys itself on first sight.
func TestOrganization360DoesNotAdvanceTheVisitBaseline(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, org360RepPerms)

	if _, err := svc.Assemble(rep, org); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if marks := e.WsCount(t, `SELECT count(*) FROM user_record_view WHERE entity_id = $1`, org.UUID); marks != 0 {
		t.Errorf("user_record_view rows after a GET = %d, want 0 — only the ack writes the baseline", marks)
	}
	if _, err := svc.Acknowledge(rep, org); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if marks := e.WsCount(t, `SELECT count(*) FROM user_record_view WHERE entity_id = $1`, org.UUID); marks != 1 {
		t.Errorf("user_record_view rows after an ack = %d, want 1", marks)
	}
}

// An agent acting through a passport is not a visitor: it must not consume
// the human's unread marker, and it cannot triage approvals either.
func TestOrganization360RefusesTheVisitBaselineToAnAgent(t *testing.T) {
	e := Setup(t)
	svc := org360Service(e)
	org := ids.From[ids.OrganizationKind](e.SeedOrg(t, "Acme", &e.Rep1))

	if _, err := svc.Acknowledge(agentWithOrgRead(e), org); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("agent acknowledgment → %v, want ErrPermissionDenied", err)
	}
	view, err := svc.Assemble(agentWithOrgRead(e), org)
	if err != nil {
		t.Fatalf("assemble as an agent: %v", err)
	}
	if view.PendingApprovals != nil {
		t.Error("pending_approvals present for an agent — triage is human work, so the section must be omitted")
	}
	if !slices.Contains(view.SectionsOmitted, "pending_approvals") {
		t.Errorf("sections_omitted = %v, want it to name pending_approvals for an agent", view.SectionsOmitted)
	}
}

// agentWithOrgRead binds an agent principal holding the same object grants
// the rep does, unbounded so the only thing left under test is the
// human-only rule (an agent carries no user id, so a team row scope would
// refuse it for the wrong reason).
func agentWithOrgRead(e *Env) context.Context {
	perms := org360RepPerms
	perms.RowScope = principal.RowScopeAll
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test", SeatType: principal.SeatFull,
		Permissions: perms,
	})
}

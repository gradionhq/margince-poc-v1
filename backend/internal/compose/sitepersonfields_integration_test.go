// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Auto-filling a site person onto an employee the workspace already has
// (ADR-0072/A118 phase 4B): who counts as unmistakably the same person, what is
// written when they are, and every case that still stages a lead instead.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seatEmployee plants one person employed by org, optionally with an email.
func seatEmployee(t *testing.T, e *integration.Env, org ids.UUID, fullName, email string) ids.UUID {
	t.Helper()
	person := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `
			INSERT INTO person (id, full_name, source, captured_by)
			VALUES ($1, $2, 'gmail:seed', 'connector:gmail')`, person, fullName); err != nil {
			return err
		}
		if email != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO person_email (person_id, email, email_type, is_primary, source, captured_by)
				VALUES ($1, $2, 'work', true, 'gmail:seed', 'connector:gmail')`, person, email); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO relationship (kind, person_id, organization_id, is_current_primary, source, captured_by)
			VALUES ('employment', $1, $2, true, 'gmail:seed', 'connector:gmail')`, person, org)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return person
}

func seatedTitle(t *testing.T, e *integration.Env, person ids.UUID) *string {
	t.Helper()
	var title *string
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT title FROM person WHERE id = $1`, person).Scan(&title)
	})
	if err != nil {
		t.Fatal(err)
	}
	return title
}

func TestApplySitePersonFieldsMatchesAnEmployeeByName(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	org := e.SeedOrg(t, "Acme", nil)
	person := seatEmployee(t, e, org, "Bob Builder", "")

	matched, err := store.ApplySitePersonFields(e.Admin(), ids.From[ids.OrganizationKind](org),
		people.SitePersonFields{
			Name: "Bob Builder", Role: "Head of Delivery",
			EvidenceSnippet: "Bob Builder — Head of Delivery",
			SourceURL:       "https://acme.example/team",
		})
	if err != nil {
		t.Fatalf("ApplySitePersonFields: %v", err)
	}
	if !matched {
		t.Fatal("the site person names an employee of this company and was not matched")
	}
	if got := seatedTitle(t, e, person); got == nil || *got != "Head of Delivery" {
		t.Fatalf("title = %v, want the role the site published", got)
	}
	// The evidence is what makes the value auditable back to the page.
	if n := e.WsCount(t, `
		SELECT count(*) FROM person_profile_field
		 WHERE person_id = $1 AND field = 'role' AND source = 'site_read'`, person); n != 1 {
		t.Fatalf("%d role evidence rows, want 1", n)
	}

	t.Run("a re-read applies nothing twice", func(t *testing.T) {
		matched, err := store.ApplySitePersonFields(e.Admin(), ids.From[ids.OrganizationKind](org),
			people.SitePersonFields{
				Name: "Bob Builder", Role: "Chief of Everything",
				EvidenceSnippet: "Bob Builder — Chief of Everything",
				SourceURL:       "https://acme.example/team",
			})
		if err != nil || !matched {
			t.Fatalf("second read: matched=%v err=%v", matched, err)
		}
		if got := seatedTitle(t, e, person); got == nil || *got != "Head of Delivery" {
			t.Fatalf("title = %v — the first answer must stand", got)
		}
	})
}

func TestApplySitePersonFieldsRefusesToGuess(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	org := e.SeedOrg(t, "Acme", nil)
	seatEmployee(t, e, org, "Chris Taylor", "")
	seatEmployee(t, e, org, "Chris Taylor", "")

	t.Run("two employees of the same name are not identifiable", func(t *testing.T) {
		matched, err := store.ApplySitePersonFields(e.Admin(), ids.From[ids.OrganizationKind](org),
			people.SitePersonFields{
				Name: "Chris Taylor", Role: "Engineer",
				EvidenceSnippet: "Chris Taylor — Engineer", SourceURL: "https://acme.example/team",
			})
		if err != nil {
			t.Fatalf("ApplySitePersonFields: %v", err)
		}
		if matched {
			t.Fatal("an ambiguous name was matched — the lead must stage instead")
		}
	})

	t.Run("a stranger on the team page is not matched", func(t *testing.T) {
		matched, err := store.ApplySitePersonFields(e.Admin(), ids.From[ids.OrganizationKind](org),
			people.SitePersonFields{
				Name: "Someone Entirely Else", Role: "Engineer",
				EvidenceSnippet: "Someone Entirely Else — Engineer", SourceURL: "https://acme.example/team",
			})
		if err != nil {
			t.Fatalf("ApplySitePersonFields: %v", err)
		}
		if matched {
			t.Fatal("a stranger was matched — strangers stay staged (NEVER-8)")
		}
	})
}

func TestApplySitePersonFieldsStaysInsideTheCompany(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	acme := e.SeedOrg(t, "Acme", nil)
	other := e.SeedOrg(t, "Other", nil)
	person := seatEmployee(t, e, other, "Dana Reed", "dana@other.example")

	// Acme's site publishes Dana. The CRM records Dana at OTHER, so the two
	// claims disagree about where she works — a human's call, not a sweep's.
	matched, err := store.ApplySitePersonFields(e.Admin(), ids.From[ids.OrganizationKind](acme),
		people.SitePersonFields{
			Name: "Dana Reed", Role: "CTO", PublishedEmail: "dana@other.example",
			EvidenceSnippet: "Dana Reed — CTO", SourceURL: "https://acme.example/team",
		})
	if err != nil {
		t.Fatalf("ApplySitePersonFields: %v", err)
	}
	if matched {
		t.Fatal("a person employed elsewhere was matched from this company's site")
	}
	if got := seatedTitle(t, e, person); got != nil {
		t.Fatalf("title = %q — another company's site must not fill it", *got)
	}
}

func TestApplySitePersonFieldsNeverTouchesAHumansAnswer(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	org := e.SeedOrg(t, "Acme", nil)
	person := seatEmployee(t, e, org, "Erin Vance", "erin@acme.example")
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE person SET title = 'Handwritten Title' WHERE id = $1`, person)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	matched, err := store.ApplySitePersonFields(e.Admin(), ids.From[ids.OrganizationKind](org),
		people.SitePersonFields{
			Name: "Erin Vance", Role: "VP Sales", PublishedEmail: "erin@acme.example",
			EvidenceSnippet: "Erin Vance — VP Sales", SourceURL: "https://acme.example/team",
		})
	if err != nil {
		t.Fatalf("ApplySitePersonFields: %v", err)
	}
	if !matched {
		t.Fatal("an exact email match among the company's employees must match")
	}
	if got := seatedTitle(t, e, person); got == nil || *got != "Handwritten Title" {
		t.Fatalf("title = %v — the human's answer was touched", got)
	}
}

// seatEmployeeOwnedBy plants an employee whose row-scope OWNER is chosen, which
// is what the two tests below turn on. seatEmployee leaves owner_id NULL, and an
// unowned row is shared with everyone — so a probe against it passes for every
// seat and would prove nothing about scope.
func seatEmployeeOwnedBy(t *testing.T, e *integration.Env, org, owner ids.UUID, fullName string) ids.UUID {
	t.Helper()
	person := seatEmployee(t, e, org, fullName, "")
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE person SET owner_id = $2 WHERE id = $1`, person, owner)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return person
}

// sitePersonRepPerms is a team-scoped rep who may read the company and update a
// person. RepPerms itself is NOT usable here: it carries no `organization`
// grant, so the organization gate would refuse before the person probe is ever
// reached and the test would pass for the wrong reason.
var sitePersonRepPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":                {Create: true, Read: true, Update: true},
		"organization":          {Create: true, Read: true, Update: true},
		"relationship":          {Create: true, Read: true, Update: true},
		"installation_settings": {Read: true},
	},
	RowScope: principal.RowScopeTeam,
}

// The fill writes a person resolved from the ORGANIZATION's employment edges,
// so the organization gate says nothing about it. A caller who may read the
// company but may not write that employee must not change them.
//
// It has to be driven directly rather than through either production caller:
// both are PrincipalSystem, for whom every row-scope probe is inert by
// construction, so a test routed through them would be green whether or not the
// probe existed. The four tests above all use e.Admin() for the same reason and
// are equally unable to see this.
func TestApplySitePersonFieldsWillNotWriteAnEmployeeTheCallerCannotChange(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	// The company is the other team's, shared with nobody — but every seat
	// reads an organization, so the org gate passes and the PERSON probe is
	// what the assertion turns on.
	org := e.SeedOrg(t, "Acme", &e.Rep3)
	theirs := seatEmployeeOwnedBy(t, e, org, e.Rep3, "Bob Builder")
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, sitePersonRepPerms)

	matched, err := store.ApplySitePersonFields(rep, ids.From[ids.OrganizationKind](org),
		people.SitePersonFields{
			Name: "Bob Builder", Role: "Head of Delivery",
			EvidenceSnippet: "Bob Builder — Head of Delivery",
			SourceURL:       "https://acme.example/team",
		})
	if err != nil {
		t.Fatalf("ApplySitePersonFields: %v", err)
	}
	// Skip, not refuse: an employee this caller cannot write is the same case
	// as one the page did not identify, and the lead stages exactly as before.
	// Refusing would abort a whole company's confirmation over one row.
	if matched {
		t.Error("reported a match on an employee outside the caller's write authority — " +
			"the caller now learns the person exists by watching the write succeed")
	}
	if got := seatedTitle(t, e, theirs); got != nil {
		t.Errorf("title = %q on a person the caller may not change; want it untouched", *got)
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM person_profile_field
		 WHERE person_id = $1 AND source = 'site_read'`, theirs); n != 0 {
		t.Errorf("%d site_read evidence rows written for an unwritable person, want 0", n)
	}
	// The audit and the bus event hang off the same path, so a leak there is
	// the same disclosure by another door.
	if n := e.WsCount(t, `
		SELECT count(*) FROM audit_log WHERE entity_type = 'person' AND entity_id = $1`,
		theirs); n != 0 {
		t.Errorf("%d audit rows for a write that must not have happened, want 0", n)
	}
}

// The mirror, and the reason it is here: a probe that refused EVERYTHING would
// pass the test above while breaking the feature, and nothing else in this file
// runs under a bounded seat to catch it.
func TestApplySitePersonFieldsStillFillsAnEmployeeTheCallerMayChange(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	org := e.SeedOrg(t, "Acme", &e.Rep1)
	mine := seatEmployeeOwnedBy(t, e, org, e.Rep1, "Bob Builder")
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, sitePersonRepPerms)

	matched, err := store.ApplySitePersonFields(rep, ids.From[ids.OrganizationKind](org),
		people.SitePersonFields{
			Name: "Bob Builder", Role: "Head of Delivery",
			EvidenceSnippet: "Bob Builder — Head of Delivery",
			SourceURL:       "https://acme.example/team",
		})
	if err != nil {
		t.Fatalf("ApplySitePersonFields: %v", err)
	}
	if !matched {
		t.Fatal("the caller owns this employee and may change them; the fill was refused anyway")
	}
	if got := seatedTitle(t, e, mine); got == nil || *got != "Head of Delivery" {
		t.Fatalf("title = %v, want the role the site published", got)
	}
}

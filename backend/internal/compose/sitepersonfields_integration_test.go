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
)

// seatEmployee plants one person employed by org, optionally with an email.
func seatEmployee(t *testing.T, e *integration.Env, org ids.UUID, fullName, email string) ids.UUID {
	t.Helper()
	person := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `
			INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			VALUES ($1, $2, $3, 'gmail:seed', 'connector:gmail')`, person, e.WS, fullName); err != nil {
			return err
		}
		if email != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO person_email (workspace_id, person_id, email, email_type, is_primary, source, captured_by)
				VALUES ($1, $2, $3, 'work', true, 'gmail:seed', 'connector:gmail')`,
				e.WS, person, email); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO relationship (workspace_id, kind, person_id, organization_id, is_current_primary, source, captured_by)
			VALUES ($1, 'employment', $2, $3, true, 'gmail:seed', 'connector:gmail')`,
			e.WS, person, org)
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
	store := people.NewStore(e.Pool)
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
	store := people.NewStore(e.Pool)
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
	store := people.NewStore(e.Pool)
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
	store := people.NewStore(e.Pool)
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

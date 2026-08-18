// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Relationship strength (B-E13.16, formulas-and-rules §4) over real
// rows: fixed seed + fixed clock → the spec's worked example exactly;
// leads contribute nothing (ADR-0008); the org roll-up is the max over
// current employees.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestRelationshipStrengthOverSeededRows(t *testing.T) {
	e := Setup(t)
	owner := OwnerConn(t)
	store := people.NewStore(e.DB())
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	person := SeedIDRow(t, owner, `INSERT INTO person (id, full_name, source, captured_by) VALUES ($1, 'Warm Contact', 'manual', 'human:x')`)
	org := SeedIDRow(t, owner, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'Warm GmbH', 'manual', 'human:x')`)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO relationship (kind, person_id, organization_id, source, captured_by) VALUES ('employment', $1, $2, 'manual', 'human:x')`, person, org); err != nil {
		t.Fatal(err)
	}

	// The §4.1 worked example: 12 directed interactions inside 90 days
	// (7 inbound, 5 outbound), the most recent 5 days ago.
	for i := 0; i < 12; i++ {
		direction := "inbound"
		if i >= 7 {
			direction = "outbound"
		}
		occurred := now.AddDate(0, 0, -(5 + i*3))
		activity := SeedIDRow(t, owner, fmt.Sprintf(
			`INSERT INTO activity (id, kind, subject, occurred_at, direction, source, captured_by)
			 VALUES ($1, 'email', 'touch', '%s', '%s', 'manual', 'human:x')`,
			occurred.Format(time.RFC3339), direction,
		))
		if _, err := owner.Exec(context.Background(),
			`INSERT INTO activity_link (activity_id, entity_type, person_id) VALUES ($1, 'person', $2)`,
			activity, person); err != nil {
			t.Fatal(err)
		}
	}

	// A lead with its own linked activity: never an input (ADR-0008).
	lead := SeedIDRow(t, owner, `INSERT INTO lead (id, full_name, email, source, captured_by) VALUES ($1, 'Cold Lead', 'cold@lead.test', 'import', 'human:x')`)
	leadTouch := SeedIDRow(t, owner, `INSERT INTO activity (id, kind, subject, occurred_at, direction, source, captured_by) VALUES ($1, 'email', 'lead touch', now(), 'inbound', 'manual', 'human:x')`)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_link (activity_id, entity_type, lead_id) VALUES ($1, 'lead', $2)`,
		leadTouch, lead); err != nil {
		t.Fatal(err)
	}

	got, err := store.PersonStrength(ctx, PersonIDOf(person), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Strength != 47 || got.Bucket != "moderate" {
		t.Fatalf("worked example over real rows → %d (%s), want 47 (moderate)", got.Strength, got.Bucket)
	}
	if got.InteractionCount90d != 12 || got.Inbound90d != 7 || got.Outbound90d != 5 {
		t.Fatalf("inputs wrong: %+v", got)
	}
	if len(got.ContributingIDs) != 12 {
		t.Fatalf("contributing ids = %d, want the 12 qualifying touches", len(got.ContributingIDs))
	}
	for _, id := range got.ContributingIDs {
		if id.UUID == leadTouch {
			t.Fatal("a lead-linked activity leaked into the person computation (ADR-0008)")
		}
	}

	// Determinism: the same seed + clock reproduces the same value.
	again, err := store.PersonStrength(ctx, PersonIDOf(person), now)
	if err != nil {
		t.Fatal(err)
	}
	if again.Strength != got.Strength {
		t.Fatalf("same seed + clock → %d then %d", got.Strength, again.Strength)
	}

	// Org roll-up: max over current employees — here, the one person.
	orgStrength, err := store.OrganizationStrength(ctx, orgIDOf(org), now)
	if err != nil {
		t.Fatal(err)
	}
	if orgStrength.Strength != got.Strength {
		t.Fatalf("org roll-up → %d, want the max employee strength %d", orgStrength.Strength, got.Strength)
	}
}

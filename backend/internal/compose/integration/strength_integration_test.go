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
	store := people.NewStore(e.Pool)
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, AdminPerms)

	person := SeedRow(t, owner, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Warm Contact', 'manual', 'human:x')`, e.WS)
	org := SeedRow(t, owner, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Warm GmbH', 'manual', 'human:x')`, e.WS)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by) VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`,
		e.WS, person, org); err != nil {
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
		activity := SeedRow(t, owner, fmt.Sprintf(
			`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			 VALUES ($1, $2, 'email', 'touch', '%s', '%s', 'manual', 'human:x')`,
			occurred.Format(time.RFC3339), direction), e.WS)
		if _, err := owner.Exec(context.Background(),
			`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id) VALUES ($1, $2, 'person', $3)`,
			e.WS, activity, person); err != nil {
			t.Fatal(err)
		}
	}

	// A lead with its own linked activity: never an input (ADR-0008).
	lead := SeedRow(t, owner, `INSERT INTO lead (id, workspace_id, full_name, email, source, captured_by) VALUES ($1, $2, 'Cold Lead', 'cold@lead.test', 'import', 'human:x')`, e.WS)
	leadTouch := SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by) VALUES ($1, $2, 'email', 'lead touch', now(), 'inbound', 'manual', 'human:x')`, e.WS)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, lead_id) VALUES ($1, $2, 'lead', $3)`,
		e.WS, leadTouch, lead); err != nil {
		t.Fatal(err)
	}

	got, err := store.PersonStrength(ctx, personIDOf(person), now)
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
	again, err := store.PersonStrength(ctx, personIDOf(person), now)
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

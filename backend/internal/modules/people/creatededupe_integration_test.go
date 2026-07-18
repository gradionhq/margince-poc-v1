// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Manual creates meet the PO-F chokepoint: an exact key still refuses
// with the 409 contract (existing id disclosed under visibility), while
// a fuzzy near-match creates the record anyway and leaves the review
// trail — one dedupe_near_match system_log line committed in the same
// transaction as the create.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// nearMatchLines returns every dedupe_near_match ledger line for one
// created entity, so a test can assert both presence and count.
func nearMatchLines(ctx context.Context, t *testing.T, e *dedupeEnv, entityType string, createdID ids.UUID) []struct {
	MatchedID  string
	Confidence float64
} {
	t.Helper()
	var out []struct {
		MatchedID  string
		Confidence float64
	}
	err := e.store.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT detail->>'matched_id', (detail->>'confidence')::float8
			  FROM system_log
			 WHERE action = 'dedupe_near_match'
			   AND detail->>'entity_type' = $1
			   AND detail->>'created_id' = $2`,
			entityType, createdID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line struct {
				MatchedID  string
				Confidence float64
			}
			if err := rows.Scan(&line.MatchedID, &line.Confidence); err != nil {
				return err
			}
			out = append(out, line)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read dedupe_near_match ledger: %v", err)
	}
	return out
}

func TestCreatePersonFuzzyNearMatchCreatesAndRecords(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	johnID, _ := e.seedEmployedPerson(ctx, t, "John Doe", "john.doe@acme.test", "Acme GmbH", "acme.test")

	// Similar name + an address on the employer's domain: 0.55·0.9667 +
	// 0.45·0.8 = 0.8917 ≥ 0.72 → fuzzy review. The manual policy creates
	// anyway — a probability never blocks a human — and records the pair.
	created, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: "Jon Doe", Source: "manual",
		Emails: []PersonEmailInput{{Email: "jon@acme.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("a fuzzy near-match must create, not block: %v", err)
	}

	lines := nearMatchLines(ctx, t, e, "person", ids.UUID(created.Id))
	if len(lines) != 1 {
		t.Fatalf("got %d dedupe_near_match lines for the created person, want exactly 1", len(lines))
	}
	if lines[0].MatchedID != johnID.String() {
		t.Fatalf("recorded matched_id = %s, want the incumbent %s", lines[0].MatchedID, johnID)
	}
	if lines[0].Confidence < dedupeReviewThreshold {
		t.Fatalf("recorded confidence %.4f below the review threshold %.2f", lines[0].Confidence, dedupeReviewThreshold)
	}

	// The incumbent himself was created through the same path with no
	// near neighbour — a clean create must not leave a ledger line.
	if clean := nearMatchLines(ctx, t, e, "person", johnID.UUID); len(clean) != 0 {
		t.Fatalf("a no-match create left %d dedupe_near_match lines, want 0", len(clean))
	}
}

func TestCreatePersonExactEmailStillRefusesWith409(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	johnID, _ := e.seedEmployedPerson(ctx, t, "John Doe", "john.exact@acme.test", "Acme GmbH", "acme.test")

	_, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: "Johnny Doe", Source: "manual",
		Emails: []PersonEmailInput{{Email: "JOHN.EXACT@ACME.TEST", EmailType: "work", IsPrimary: true}},
	})
	var dup *DuplicateEmailError
	if !errors.As(err, &dup) {
		t.Fatalf("exact email collision must stay the typed 409, got %v", err)
	}
	if dup.ExistingID != johnID {
		t.Fatalf("409 discloses %s, want the incumbent %s", dup.ExistingID, johnID)
	}
}

func TestCreateOrganizationFuzzyNearMatchCreatesAndRecords(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	incumbent, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Wayne Enterprises GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "wayne.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same stem, different legal suffix, no shared domain: suffix
	// normalization scores 1.0 → fuzzy review. Different legal entities
	// are a human's call, so the create proceeds and the pair records.
	created, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Wayne Enterprises Inc", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "wayne-us.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatalf("a fuzzy near-match must create, not block: %v", err)
	}

	lines := nearMatchLines(ctx, t, e, "organization", ids.UUID(created.Id))
	if len(lines) != 1 {
		t.Fatalf("got %d dedupe_near_match lines for the created org, want exactly 1", len(lines))
	}
	if lines[0].MatchedID != ids.UUID(incumbent.Id).String() {
		t.Fatalf("recorded matched_id = %s, want the incumbent %s", lines[0].MatchedID, incumbent.Id)
	}
	if lines[0].Confidence < dedupeReviewThreshold {
		t.Fatalf("recorded confidence %.4f below the review threshold %.2f", lines[0].Confidence, dedupeReviewThreshold)
	}

	if clean := nearMatchLines(ctx, t, e, "organization", ids.UUID(incumbent.Id)); len(clean) != 0 {
		t.Fatalf("a no-match create left %d dedupe_near_match lines, want 0", len(clean))
	}
}

func TestCreateOrganizationExactDomainStillRefusesWith409(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	incumbent, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Stark Industries GmbH", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "stark.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Totally Unrelated Name", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "STARK.TEST", IsPrimary: true}},
	})
	var dup *DuplicateDomainError
	if !errors.As(err, &dup) {
		t.Fatalf("exact domain collision must stay the typed 409, got %v", err)
	}
	if dup.ExistingID.UUID != ids.UUID(incumbent.Id) {
		t.Fatalf("409 discloses %s, want the incumbent %s", dup.ExistingID, incumbent.Id)
	}
}

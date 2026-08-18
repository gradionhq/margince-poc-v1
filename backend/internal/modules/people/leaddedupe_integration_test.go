// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Lead↔lead dedupe (ADR-0118/A169 §2) lives in the schema and the review
// queue: the CHECK that keeps a lead pair same-type, the fuzzy detector's
// SQL, and the merge disposition that runs the ONE lead merge — none of
// which a unit test can see.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (e *dedupeEnv) createLead(ctx context.Context, t *testing.T, name, email, company string) ids.LeadID {
	t.Helper()
	in := CreateLeadInput{FullName: &name, CompanyName: &company, Source: "inbound"}
	if email != "" {
		in.Email = &email
	}
	lead, _, err := e.store.CreateLead(ctx, in)
	if err != nil {
		t.Fatalf("create lead %q: %v", name, err)
	}
	return ids.From[ids.LeadKind](ids.UUID(lead.Id))
}

// A second lead that reads like the first — same company, a near spelling
// of the name, no shared exact key — lands on the queue as a LEAD pair.
func TestNearMatchLeadCreateLeavesAnOpenLeadCandidate(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	first := e.createLead(ctx, t, "Jonas Petersen", "jonas@nordwind.test", "Nordwind Logistik")
	second := e.createLead(ctx, t, "Jonas Peterson", "", "Nordwind Logistik")

	rows := openCandidates(ctx, t, e, entityLead)
	if len(rows) != 1 {
		t.Fatalf("open lead queue holds %d candidates, want 1", len(rows))
	}
	c := rows[0]
	got := map[string]bool{c.LeftID.String(): true, c.RightID.String(): true}
	if !got[first.String()] || !got[second.String()] {
		t.Fatalf("pair {%s,%s} does not name both leads", c.LeftID, c.RightID)
	}
	if c.Confidence < dedupeReviewThreshold {
		t.Fatalf("confidence %.4f below the review threshold", c.Confidence)
	}
	if ev := string(c.Evidence); !strings.Contains(ev, "Jonas Petersen") || !strings.Contains(ev, "Jonas Peterson") {
		t.Fatalf("evidence %s does not carry both names", ev)
	}
	// Never against a person: the person queue is untouched.
	if persons := openCandidates(ctx, t, e, entityPerson); len(persons) != 0 {
		t.Fatalf("a lead create put %d pair(s) on the PERSON queue", len(persons))
	}
}

// Two leads with the same name at different companies and unrelated
// addresses are two prospects until proven otherwise: below the threshold,
// nothing is proposed.
func TestUnrelatedLeadsAreNotProposed(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	e.createLead(ctx, t, "Anna Weber", "anna@alpha.test", "Alpha GmbH")
	e.createLead(ctx, t, "Bernd Kraus", "bernd@beta.test", "Beta AG")
	if rows := openCandidates(ctx, t, e, entityLead); len(rows) != 0 {
		t.Fatalf("unrelated leads produced %d candidate(s)", len(rows))
	}
}

// The merge disposition runs MergeLead: the loser is archived with the
// pointer, its timeline is on the survivor, and the survivor took the key it
// lacked.
func TestDedupeLeadMergeArm(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	first := e.createLead(ctx, t, "Mira Holt", "", "Holt & Co")
	second := e.createLead(ctx, t, "Mira Holtt", "mira@holt.test", "Holt & Co")
	// A note on the loser, the way the timeline write links it.
	activity := ids.NewV7()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
			 VALUES ($1, $2, 'note', 'Called', now(), 'manual', 'human:x')`, activity, e.ws); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO activity_link (id, workspace_id, activity_id, entity_type, lead_id)
			 VALUES ($1, $2, $3, 'lead', $4)`, ids.NewV7(), e.ws, activity, second.UUID)
		return err
	}); err != nil {
		t.Fatalf("seed loser activity: %v", err)
	}
	c := openCandidates(ctx, t, e, entityLead)[0]

	winner := first.UUID
	merged, err := e.store.DisposeDedupeCandidate(ctx, c.ID, "merge", &winner)
	if err != nil {
		t.Fatalf("merge dispose: %v", err)
	}
	if merged.Disposition != "merged" {
		t.Fatalf("disposition = %s, want merged", merged.Disposition)
	}
	var mergedInto *ids.UUID
	var loserArchived bool
	var survivorEmail *string
	var linkedTo *ids.UUID
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT merged_into_id, archived_at IS NOT NULL FROM lead WHERE id = $1`, second).
			Scan(&mergedInto, &loserArchived); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT email FROM lead WHERE id = $1`, first).Scan(&survivorEmail); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT lead_id FROM activity_link WHERE activity_id = $1`, activity).Scan(&linkedTo)
	}); err != nil {
		t.Fatalf("reading merge outcome: %v", err)
	}
	if mergedInto == nil || *mergedInto != winner || !loserArchived {
		t.Errorf("loser merged_into=%v archived=%t; want -> %s, archived", mergedInto, loserArchived, winner)
	}
	if survivorEmail == nil || *survivorEmail != "mira@holt.test" {
		t.Errorf("survivor email = %v; the address the loser held must move to the survivor", survivorEmail)
	}
	if linkedTo == nil || *linkedTo != winner {
		t.Errorf("loser's note links %v; want the survivor %s", linkedTo, winner)
	}
}

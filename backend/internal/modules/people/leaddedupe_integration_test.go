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
			`INSERT INTO activity (id, kind, subject, occurred_at, source, captured_by)
			 VALUES ($1, 'note', 'Called', now(), 'manual', 'human:x')`, activity); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO activity_link (id, activity_id, entity_type, lead_id)
			 VALUES ($1, $2, 'lead', $3)`, ids.NewV7(), activity, second.UUID)
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

// seedPurpose adds one consent purpose to the dedupe env's workspace.
func (e *dedupeEnv) seedPurpose(ctx context.Context, t *testing.T, key string) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO consent_purpose (id, key, label) VALUES ($1, $2, $2)`, id, key)
		return err
	}); err != nil {
		t.Fatalf("seed purpose: %v", err)
	}
	return id
}

// seedLeadConsent writes a lead-scoped consent state row plus its proof event.
func (e *dedupeEnv) seedLeadConsent(ctx context.Context, t *testing.T, lead ids.LeadID, purpose ids.UUID, state string) {
	t.Helper()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO person_consent (lead_id, purpose_id, state, captured_at, source) VALUES ($1, $2, $3, now(), 'form')`,
			lead, purpose, state); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO consent_event (lead_id, purpose_id, new_state, source, policy_text, policy_version, captured_at, captured_by)
			 VALUES ($1, $2, $3, 'form', 'seeded wording', 'v1', now(), 'human:x')`,
			lead, purpose, state)
		return err
	}); err != nil {
		t.Fatalf("seed lead consent: %v", err)
	}
}

// A merge never turns an opt-out back into a grant: the loser's withdrawal
// flips the survivor's grant, with a proof event; and the proof rows travel
// with the state, so the survivor's grants stay actionable.
func TestLeadMergeCarriesWithdrawalAndProof(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	newsletter := e.seedPurpose(ctx, t, "newsletter")
	updates := e.seedPurpose(ctx, t, "product_updates")
	survivor := e.createLead(ctx, t, "Karin Vogt", "karin@vogt.test", "Vogt KG")
	loser := e.createLead(ctx, t, "Karin Voigt", "", "Vogt KG")
	e.seedLeadConsent(ctx, t, survivor, newsletter, "granted")
	e.seedLeadConsent(ctx, t, loser, newsletter, "withdrawn")
	e.seedLeadConsent(ctx, t, loser, updates, "granted")

	if _, err := e.store.MergeLead(ctx, loser, survivor); err != nil {
		t.Fatalf("merge: %v", err)
	}
	var newsletterState, updatesState string
	var proofOnSurvivor, proofOnLoser int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT state FROM person_consent WHERE lead_id = $1 AND purpose_id = $2`, survivor, newsletter).Scan(&newsletterState); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT state FROM person_consent WHERE lead_id = $1 AND purpose_id = $2`, survivor, updates).Scan(&updatesState); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM consent_event WHERE lead_id = $1`, survivor).Scan(&proofOnSurvivor); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM consent_event WHERE lead_id = $1`, loser).Scan(&proofOnLoser)
	}); err != nil {
		t.Fatalf("read consent after merge: %v", err)
	}
	if newsletterState != "withdrawn" {
		t.Errorf("newsletter on the survivor = %q; the loser's withdrawal must win", newsletterState)
	}
	if updatesState != "granted" {
		t.Errorf("product_updates on the survivor = %q; the loser's grant must carry", updatesState)
	}
	if proofOnLoser != 0 || proofOnSurvivor < 4 {
		t.Errorf("proof rows: survivor=%d loser=%d; every event must sit on the live lead", proofOnSurvivor, proofOnLoser)
	}
}

// Merging {A,B} leaves no open pair naming A: {A,C} can no longer be decided
// and is retired rather than offered as a merge that can only fail.
func TestLeadMergeRetiresOtherPairsNamingTheLoser(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	a := e.createLead(ctx, t, "Tomas Berg", "tomas@berg.test", "Berg Bau")
	b := e.createLead(ctx, t, "Tomas Bergg", "", "Berg Bau")
	e.createLead(ctx, t, "Thomas Berg", "", "Berg Bau")
	open := openCandidates(ctx, t, e, entityLead)
	if len(open) < 2 {
		t.Fatalf("expected at least two lead pairs on the queue, got %d", len(open))
	}
	// Merge A into B through whichever pair names them both.
	var ab ids.UUID
	for _, row := range open {
		got := map[string]bool{row.LeftID.String(): true, row.RightID.String(): true}
		if got[a.String()] && got[b.String()] {
			ab = row.ID
		}
	}
	if ab.IsZero() {
		t.Fatalf("no {A,B} pair on the queue")
	}
	winner := b.UUID
	if _, err := e.store.DisposeDedupeCandidate(ctx, ab, "merge", &winner); err != nil {
		t.Fatalf("merge dispose: %v", err)
	}
	for _, row := range openCandidates(ctx, t, e, entityLead) {
		if row.LeftID == a.UUID || row.RightID == a.UUID {
			t.Errorf("an open pair still names the merged-away lead %s (%s)", a, row.ID)
		}
	}
}

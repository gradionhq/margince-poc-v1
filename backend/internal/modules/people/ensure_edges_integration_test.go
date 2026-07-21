// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// The ensure/enrich edges over a real Postgres: repeat mail reuses the
// exact-tier incumbent instead of minting twins, an impersonation-suspect
// display name lands quarantined, and the signature apply keeps its
// evidence-or-omit promise when a guarded fill loses its race — the
// evidence row is withdrawn, never left claiming an unapplied value.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ensureInput is a well-formed captured-mail counterparty; tests perturb it.
func (e *dedupeEnv) ensureInput(ctx context.Context, t *testing.T, email, display, domain string) EnsureCounterpartyInput {
	t.Helper()
	activityID := ids.New[ids.ActivityKind]()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, direction, source_system, source_id, source, captured_by)
			VALUES ($1, $2, 'email', 'hi', 'inbound', 'gmail', $3, 'gmail:seed', 'connector:gmail')`,
			activityID, e.ws, activityID.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return EnsureCounterpartyInput{
		Email: email, DisplayName: display, Domain: domain,
		OwnerID: e.rep, ActivityID: activityID,
		Source: "gmail:" + activityID.String(), CapturedBy: "connector:gmail",
	}
}

func TestEnsureCounterpartyReusesTheExactIncumbent(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	first, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "carol@ensure.test", "Carol Example", "ensure.test"))
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !first.PersonCreated || !first.OrgCreated || first.OrganizationID == nil {
		t.Fatalf("first ensure = %+v, want person + org created", first)
	}

	// The same address again: the exact tier lands on the incumbent —
	// no twin person, no second org, no second employment edge.
	second, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "carol@ensure.test", "Carol Example", "ensure.test"))
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.PersonCreated || second.PersonID != first.PersonID {
		t.Fatalf("second ensure = %+v, want the incumbent %s reused", second, first.PersonID)
	}
	if second.OrgCreated {
		t.Fatal("second ensure re-created the organization")
	}
	var employments int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM relationship
			WHERE person_id = $1 AND kind = 'employment' AND is_current_primary`, first.PersonID).Scan(&employments)
	}); err != nil {
		t.Fatal(err)
	}
	if employments != 1 {
		t.Fatalf("%d employment edges after the repeat, want 1", employments)
	}
}

func TestEnsureCounterpartyQuarantinesImpersonationSuspects(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()

	// A display name embedding an address on a DIFFERENT domain — the
	// classic spoof tell. The row still lands (hiding suspicious mail
	// would be worse), but quarantined for the review surface.
	res, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t,
		"boss@spoof.test", "ceo@real-corp.example", "spoof.test"))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var quarantined bool
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT quarantined_at IS NOT NULL FROM person WHERE id = $1`, res.PersonID).Scan(&quarantined)
	}); err != nil {
		t.Fatal(err)
	}
	if !quarantined {
		t.Fatal("an embedded-foreign-address display name must land quarantined")
	}

	if _, err := e.store.EnsureCounterparty(ctx, EnsureCounterpartyInput{Email: "  "}); err == nil {
		t.Fatal("an empty email must refuse, not create")
	}
}

func TestApplySignatureFieldsWithdrawsEvidenceWhenTheFillLosesItsRace(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	person, err := e.store.CreatePerson(ctx, CreatePersonInput{
		FullName: "Sig Edge", Source: "manual",
		Emails: []PersonEmailInput{{Email: "sig@edge.test", EmailType: "work", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	personID := ids.From[ids.PersonKind](ids.UUID(person.Id))

	// Occupy the title after candidate selection would have seen it
	// empty — the guarded fill must lose and withdraw its evidence.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE person SET title = 'Human-set CTO' WHERE id = $1`, personID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	res, err := e.store.ApplySignatureFields(ctx, personID, ids.NewV7(), []SignatureField{
		{Name: "title", Value: "AI CTO", Evidence: "AI CTO", Confidence: 0.9},
		{Name: "", Value: "   ", Evidence: ""}, // a blank value is dropped before any write
	})
	if err != nil {
		t.Fatalf("ApplySignatureFields: %v", err)
	}
	if res.Applied != 0 || res.Skipped != 2 {
		t.Fatalf("apply = %+v, want 0 applied / 2 skipped", res)
	}
	var title string
	var evidenceRows int
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT title FROM person WHERE id = $1`, personID).Scan(&title); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM person_profile_field WHERE person_id = $1`, personID).Scan(&evidenceRows)
	}); err != nil {
		t.Fatal(err)
	}
	if title != "Human-set CTO" {
		t.Fatalf("title = %q — the occupied value must never be touched", title)
	}
	if evidenceRows != 0 {
		t.Fatalf("%d evidence rows persisted for an unapplied fill, want 0", evidenceRows)
	}

	// Same race on the phone lane: an existing phone keeps its place and
	// the evidence is withdrawn.
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO person_phone (workspace_id, person_id, phone, phone_type, is_primary, position, source, captured_by)
			VALUES ($1, $2, '+49 30 9999999', 'work', true, 0, 'manual', 'human:test')`, e.ws, personID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	res, err = e.store.ApplySignatureFields(ctx, personID, ids.NewV7(), []SignatureField{
		{Name: "phone", Value: "+49 30 1234567", Evidence: "+49 30 1234567", Confidence: 0.8},
	})
	if err != nil {
		t.Fatalf("phone apply: %v", err)
	}
	if res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("phone apply = %+v, want 0 applied / 1 skipped", res)
	}

	// A sidecar-only field applies cleanly, and a second verdict for the
	// same field defers to the first (one row per person+field, forever).
	res, err = e.store.ApplySignatureFields(ctx, personID, ids.NewV7(), []SignatureField{
		{Name: "role", Value: "Decision maker", Evidence: "CTO", Confidence: 0.7},
	})
	if err != nil || res.Applied != 1 {
		t.Fatalf("role apply = %+v (err %v), want 1 applied", res, err)
	}
	res, err = e.store.ApplySignatureFields(ctx, personID, ids.NewV7(), []SignatureField{
		{Name: "role", Value: "Champion", Evidence: "CTO again", Confidence: 0.9},
	})
	if err != nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("second role verdict = %+v (err %v), want skipped (first verdict wins)", res, err)
	}

	if res, err = e.store.ApplySignatureFields(ctx, personID, ids.NewV7(), nil); err != nil || res.Applied != 0 || res.Skipped != 0 {
		t.Fatalf("empty apply = %+v (err %v), want a zero no-op", res, err)
	}
}

func TestEnsureCounterpartySuppressedAddressStaysDead(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	if err := e.store.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
			VALUES ($1, 'email', $2)`, e.ws, storekit.SuppressionHash("dead@ensure.test"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.store.EnsureCounterparty(ctx, e.ensureInput(ctx, t, "dead@ensure.test", "Dead Address", "ensure.test"))
	if !errors.Is(err, ErrCounterpartySuppressed) {
		t.Fatalf("suppressed address = %v, want ErrCounterpartySuppressed", err)
	}
}

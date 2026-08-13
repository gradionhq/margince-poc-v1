// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A purchased provider claim is personal data somebody paid a third party
// for, so the privacy machinery has to reach it on every path that reaches
// the subject's other data. These tests exercise all four, against a real
// database, because every one of them is SQL:
//
//   - Art. 17 erasure (the delete arm) removes the claims and detaches the
//     runs that bought them;
//   - the retention sweep's anonymize-in-place arm does the SAME, since it
//     leaves the person row standing and nothing cascades;
//   - Art. 15 hands the claims and the run history back;
//   - a merge keeps BOTH sides' purchases, because both were paid for.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// seedPurchase writes one completed run and one claim for a subject, through
// the REAL writer people exposes — a hand-inserted claim would prove nothing
// about what the writer produces.
func seedPurchase(t *testing.T, e *Env, personID ids.UUID) (runID string) {
	t.Helper()
	admin := e.Admin()
	err := database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO provider_run
			  (subject_kind, person_id, provider, trigger, state, input_fingerprint,
			   external_correlation_id, connection_version, connection_epoch,
			   configuration_snapshot, requested_categories, completed_at)
			VALUES ('person', $1, 'surfe', 'manual', 'completed', $2,
			        gen_random_uuid(), 1, 1, '{}'::jsonb, ARRAY['professional_email'], now())
			RETURNING id::text`, personID, "fp-"+personID.String()).Scan(&runID); err != nil {
			return err
		}
		return people.WriteProviderClaims(context.Background(), tx, runID, personID.String(), "surfe",
			[]provider.Claim{{
				Key:   provider.ClaimProfessionalEmails,
				Value: []byte(`[{"value":"bought@example.com","validation_status":"valid"}]`),
			}}, time.Now().UTC())
	})
	if err != nil {
		t.Fatal(err)
	}
	return runID
}

// seedMergeSubject writes a person with their own address, so two of them can
// coexist before a merge brings them together.
func seedMergeSubject(t *testing.T, e *Env, name string) ids.UUID {
	t.Helper()
	personID := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			 VALUES ($1, `+wsClause+`, $2, 'manual', 'human:x')`, personID, name); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO person_email (workspace_id, person_id, email, source, captured_by)
			 VALUES (`+wsClause+`, $1, $2, 'manual', 'human:x')`,
			personID, personID.String()+"@example.com")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return personID
}

// claimAndRunState reads what survives for a subject: how many claims, and
// whether the run still names them.
func claimAndRunState(t *testing.T, e *Env, personID ids.UUID, runID string) (claims int, stillNamed bool, kind string) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM person_provider_claim WHERE person_id = $1`, personID).Scan(&claims); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`SELECT person_id IS NOT NULL, subject_kind FROM provider_run WHERE id = $1`,
			runID).Scan(&stillNamed, &kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	return claims, stillNamed, kind
}

// Art. 17, the delete arm: the values go, the run stays as spend and names
// nobody.
func TestErasureRemovesProviderClaimsAndDetachesTheRunsThatBoughtThem(t *testing.T) {
	e := Setup(t)
	personID := seedSubject(t, e)
	runID := seedPurchase(t, e, personID)

	if claims, _, _ := claimAndRunState(t, e, personID, runID); claims != 1 {
		t.Fatalf("seeded %d claims, want 1 — the test proves nothing about erasure without one", claims)
	}
	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), personID, "test"); err != nil {
		t.Fatal(err)
	}

	claims, stillNamed, kind := claimAndRunState(t, e, personID, runID)
	if claims != 0 {
		t.Errorf("%d purchased claims survive the erasure — a value bought about the subject is still readable", claims)
	}
	if stillNamed {
		t.Error("the run still names the erased subject: a row saying we bought data about this person IS data about them")
	}
	if kind != "scrubbed" {
		t.Errorf("run subject_kind is %q, want scrubbed", kind)
	}

	// The spend survives, detached. An erasure removes the subject, not the
	// accounting (PI-AC-8) — which is what keeps a spend history stable.
	var runs int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM provider_run WHERE id = $1`, runID).Scan(&runs)
	}); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Error("the erasure deleted the run row: what the installation spent is an accounting fact once it names nobody")
	}
}

// The retention sweep's anonymize-in-place arm. It is a SEPARATE code path
// from ErasePerson above and the one that gets missed: it leaves the person
// row standing, so nothing cascades, and without its own statements the page
// would show a bought email beside an "Erased Subject" name.
func TestRetentionAnonymizeAlsoRemovesProviderClaims(t *testing.T) {
	e := Setup(t)
	SeedRetentionPolicies(t, e)
	personID := seedSubject(t, e)
	runID := seedPurchase(t, e, personID)

	// Age the person past every policy window so the sweep acts on them.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE person SET created_at = now() - interval '4000 days',
			                  updated_at = now() - interval '4000 days'
			 WHERE id = $1`, personID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.EvaluateWorkspace(RetentionPassCtx(e.WS)); err != nil {
		t.Fatal(err)
	}

	var anonymized bool
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT first_name IS NULL AND last_name IS NULL FROM person WHERE id = $1`,
			personID).Scan(&anonymized)
	}); err != nil {
		t.Fatal(err)
	}
	if !anonymized {
		// Not a skip: a fixture the sweep never touched would make every
		// assertion below pass for the wrong reason, which is exactly what a
		// silently-skipped privacy gate looks like.
		t.Fatal("the sweep did not anonymize the seeded subject, so this test proves nothing — fix the fixture's age or the policy window")
	}

	claims, stillNamed, _ := claimAndRunState(t, e, personID, runID)
	if claims != 0 {
		t.Errorf("%d purchased claims survive the anonymize sweep — the page would show a bought email beside an anonymized name", claims)
	}
	if stillNamed {
		t.Error("the run still names a subject the sweep just anonymized")
	}
}

// Art. 15: a subject asking what we hold gets the bought values AND the fact
// that we went out and bought them.
func TestSARHandsBackTheProviderClaimsAndTheRunHistory(t *testing.T) {
	e := Setup(t)
	personID := seedSubject(t, e)
	seedPurchase(t, e, personID)

	pkg, err := privacy.AssembleSAR(e.Admin(), e.DB(), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.ProviderClaims) != 1 {
		t.Errorf("the SAR carries %d provider claims, want 1 — a value bought about the subject is withheld from their own Art. 15 package", len(pkg.ProviderClaims))
	}
	if len(pkg.ProviderRuns) != 1 {
		t.Errorf("the SAR carries %d provider runs, want 1 — the export says what we hold while hiding that we purchased it", len(pkg.ProviderRuns))
	}
	// The export carries the value, not a reference to it.
	if len(pkg.ProviderClaims) == 1 {
		if _, ok := pkg.ProviderClaims[0]["value_json"]; !ok {
			t.Error("the exported claim carries no value_json: the subject is told a claim exists but not what it says")
		}
	}
}

// A merge brings two purchases together, and both were paid for: PI-AC-11
// says the survivor shows what BOTH sides bought.
func TestMergeKeepsBothSidesPurchasedClaims(t *testing.T) {
	e := Setup(t)
	// Two DISTINCT subjects, seeded here rather than through seedSubject:
	// that helper writes one fixed address, and a merge needs two records
	// that could plausibly be the same human without colliding on the
	// address-dedupe index first.
	survivor := seedMergeSubject(t, e, "Anna Survivor")
	source := seedMergeSubject(t, e, "Anna Source")
	seedPurchase(t, e, survivor)
	seedPurchase(t, e, source)

	store := people.NewStore(e.DB())
	if _, err := store.MergePerson(e.Admin(), ids.From[ids.PersonKind](source),
		ids.From[ids.PersonKind](survivor)); err != nil {
		t.Fatal(err)
	}

	var onSurvivor int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM person_provider_claim WHERE person_id = $1`, survivor).Scan(&onSurvivor)
	}); err != nil {
		t.Fatal(err)
	}
	if onSurvivor != 2 {
		t.Errorf("the survivor holds %d claims, want both sides' 2 — a merge that drops one throws away data the customer paid for (PI-AC-11)", onSurvivor)
	}
}

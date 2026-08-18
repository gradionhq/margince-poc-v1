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
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// seedPurchase writes one completed run and one claim for a subject.
//
// The CLAIM goes through people.WriteProviderClaims, the real writer, so
// these tests see the rows production produces. The RUN row is hand-built:
// reaching a completed run through integrations.QueueRun would need a live
// connection, a registered adapter and a job inserter, and what these tests
// are about is what happens to a run once it exists.
func seedPurchase(t *testing.T, e *Env, personID ids.UUID) (runID string) {
	t.Helper()
	return seedRun(t, e, personID, "completed", "fp-"+personID.String(), true)
}

// seedRun writes one run in a named state at a named fingerprint, optionally
// with a claim hanging off it. The fingerprint is a parameter because the
// merge collision rules are defined over it: two runs collide only when they
// share one.
func seedRun(t *testing.T, e *Env, personID ids.UUID, state, fingerprint string, withClaim bool) (runID string) {
	t.Helper()
	completed := "NULL"
	if state == "completed" || state == "no_match" {
		completed = "now()"
	}
	seedCtx := e.Admin()
	err := database.WithWorkspaceTx(seedCtx, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO provider_run
			  (subject_kind, person_id, provider, trigger, state, input_fingerprint,
			   external_correlation_id, connection_version, connection_epoch,
			   configuration_snapshot, requested_categories, completed_at)
			VALUES ('person', $1, 'surfe', 'manual', $2, $3,
			        gen_random_uuid(), 1, 1, '{}'::jsonb, ARRAY['professional_email'], `+completed+`)
			RETURNING id::text`, personID, state, fingerprint).Scan(&runID); err != nil {
			return err
		}
		if !withClaim {
			return nil
		}
		// The store's own context, not a bare one: the claim write audits the
		// arrival, and an audit row needs the actor that caused it.
		return people.WriteProviderClaims(seedCtx, tx, runID, personID.String(), "surfe",
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

// runState reads one run's state and whether it still occupies the live-run
// index at its original fingerprint.
func runState(t *testing.T, e *Env, runID string) (state, fingerprint string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT state, input_fingerprint FROM provider_run WHERE id = $1`,
			runID).Scan(&state, &fingerprint)
	}); err != nil {
		t.Fatal(err)
	}
	return state, fingerprint
}

// seedMergeSubject writes a person with their own address, so two of them can
// coexist before a merge brings them together.
func seedMergeSubject(t *testing.T, e *Env, name string) ids.UUID {
	t.Helper()
	personID := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, full_name, source, captured_by)
			 VALUES ($1, $2, 'manual', 'human:x')`, personID, name); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO person_email (person_id, email, source, captured_by)
			 VALUES ( $1, $2, 'manual', 'human:x')`,
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

// The same human as TWO person rows: an archived duplicate holding their
// address, and the live record being erased. The archived row's purchased
// claims and its runs' provider_job_id — the handle that would let the
// provider be re-asked for exactly what this erasure destroyed — must go
// too. Keying the erasure on person_id alone erases one row of a person who
// exists as two.
func TestErasureReachesAnArchivedDuplicatesPurchasedClaims(t *testing.T) {
	e := Setup(t)
	live := seedSubject(t, e)

	// The archived duplicate, carrying the SAME address. Legitimate:
	// uq_person_email_dedupe is partial on archived_at IS NULL.
	archived := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, full_name, source, captured_by, archived_at)
			 VALUES ($1, 'Selma Subject', 'manual', 'human:x', now())`, archived); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO person_email (person_id, email, source, captured_by, archived_at)
			 VALUES ( $1, $2, 'manual', 'human:x', now())`, archived, subjectEmail)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	archivedRun := seedPurchase(t, e, archived)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), live, "test"); err != nil {
		t.Fatal(err)
	}

	claims, stillNamed, kind := claimAndRunState(t, e, archived, archivedRun)
	if claims != 0 {
		t.Errorf("%d purchased claims survive on the erased subject's archived duplicate — their bought email and mobile number are still stored", claims)
	}
	if stillNamed || kind != "scrubbed" {
		t.Errorf("the archived duplicate's run still names a subject (named=%v kind=%q): its provider_job_id would let the provider be re-asked for what this erasure destroyed", stillNamed, kind)
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
	if err := svc.EvaluateInstallation(RetentionPassCtx(e.WS)); err != nil {
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

// The collision matrix, which carries the whole "nothing already charged is
// discarded" rule. Both sides hold a LIVE run at the same fingerprint, which
// the live-run unique index admits only one of — so the merge must resolve it
// rather than let the relink fail or silently drop a purchase.
func TestMergeKeepsBothLiveRunsWhenEitherMayHaveBeenPaid(t *testing.T) {
	e := Setup(t)
	survivor := seedMergeSubject(t, e, "Bea Survivor")
	source := seedMergeSubject(t, e, "Bea Source")
	// One fingerprint, two live runs, both past `queued`: each may already
	// have reached the provider, so neither may be thrown away.
	const shared = "fp-collision"
	survivorRun := seedRun(t, e, survivor, "submitting", shared, false)
	sourceRun := seedRun(t, e, source, "in_progress", shared, true)

	store := people.NewStore(e.DB())
	if _, err := store.MergePerson(e.Admin(), ids.From[ids.PersonKind](source),
		ids.From[ids.PersonKind](survivor)); err != nil {
		t.Fatal(err)
	}

	if state, fp := runState(t, e, survivorRun); state != "submitting" || fp != shared {
		t.Errorf("the survivor's live run is %s at %q, want submitting at the original fingerprint — it was untouched by the merge", state, fp)
	}
	state, fp := runState(t, e, sourceRun)
	if state != "in_progress" {
		t.Errorf("the merged-away record's live run is %s, want in_progress — it may already have been charged, so it must reach its own terminal state", state)
	}
	if fp == shared {
		t.Error("the source's run kept its fingerprint: two live runs now collide on the index the merge was supposed to clear")
	}
	if !strings.HasPrefix(fp, "merged:") {
		t.Errorf("the source's run fingerprint is %q, want the merged: scrub that takes it out of the live-run index", fp)
	}
	// Both runs still name a subject, and it is the survivor.
	var onSurvivor int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM provider_run WHERE person_id = $1`, survivor).Scan(&onSurvivor)
	}); err != nil {
		t.Fatal(err)
	}
	if onSurvivor != 2 {
		t.Errorf("the survivor holds %d runs, want both sides' 2", onSurvivor)
	}
}

// A source run still `queued` never reached the provider, so it is cancelled
// rather than carried over: the merged-away record has stopped being a
// subject, and nothing was charged.
func TestMergeCancelsTheMergedAwayRecordsUnspentRun(t *testing.T) {
	e := Setup(t)
	survivor := seedMergeSubject(t, e, "Cara Survivor")
	source := seedMergeSubject(t, e, "Cara Source")
	sourceRun := seedRun(t, e, source, "queued", "fp-unspent", false)

	store := people.NewStore(e.DB())
	if _, err := store.MergePerson(e.Admin(), ids.From[ids.PersonKind](source),
		ids.From[ids.PersonKind](survivor)); err != nil {
		t.Fatal(err)
	}

	state, fp := runState(t, e, sourceRun)
	if state != "cancelled" {
		t.Errorf("the merged-away record's queued run is %s, want cancelled — it would have enriched a row no read returns", state)
	}
	if !strings.HasPrefix(fp, "merged:") {
		t.Errorf("the cancelled run's fingerprint is %q, want the merged: scrub — a cancelled run must not hold the live-run index", fp)
	}
}

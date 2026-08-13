// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integrations

// The spend history: what this installation consumed, per month per pool.
//
// Three properties only a real database can prove, and each is a way the
// number could lie:
//
//   - it agrees with the CEILING, because both read the same rows through the
//     same expression — a page saying budget remains while the next run is
//     refused is the failure this sharing exists to prevent;
//   - a run whose outcome was never learned is reported as HELD and never
//     folded into the charged total, because the platform does not know;
//   - it survives an erasure, because a scrub detaches the subject and keeps
//     the accounting (PI-AC-8).

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// seedSpendRun writes one run with one reservation, in a named state and
// month. Hand-built rather than driven through QueueRun: what these tests are
// about is the arithmetic over a ledger, and a ledger spanning two months
// cannot be produced by a live pipeline in one test.
func seedSpendRun(t *testing.T, e *runsEnv, state, pool string, reserved int, actual *int, monthsAgo int) {
	t.Helper()
	ctx := context.Background()
	// The row's own CHECK ties the two together: a skipped run carries a
	// reason and no other state may.
	var skipReason *string
	if state == string(provider.RunSkipped) {
		reason := string(provider.SkipBudgetExhausted)
		skipReason = &reason
	}
	var runID string
	if err := e.owner.QueryRow(ctx, `
		INSERT INTO provider_run
		  (subject_kind, person_id, provider, trigger, state, skip_reason, input_fingerprint,
		   external_correlation_id, connection_version, connection_epoch,
		   configuration_snapshot, requested_categories, created_at, completed_at)
		VALUES ('person', $1, 'surfe', 'manual', $2, $4, 'fp-' || gen_random_uuid()::text,
		        gen_random_uuid(), 1, 1, '{}'::jsonb, ARRAY['professional_email'],
		        date_trunc('month', now() AT TIME ZONE 'UTC') - make_interval(months => $3),
		        now())
		RETURNING id::text`, e.mine, state, monthsAgo, skipReason).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO provider_run_reservation (run_id, pool, reserved_credits, actual_credits)
		VALUES ($1, $2, $3, $4)`, runID, pool, reserved, actual); err != nil {
		t.Fatal(err)
	}
}

func spendFor(t *testing.T, e *runsEnv, month, pool string) MonthlySpend {
	t.Helper()
	months, err := e.store.SpendByMonth(e.ctx, "surfe")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	want := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if month == "previous" {
		want = want.AddDate(0, -1, 0)
	}
	for _, m := range months {
		if m.Pool == pool && m.Month.UTC().Equal(want) {
			return m
		}
	}
	t.Fatalf("no %s spend for %s in %+v", pool, month, months)
	return MonthlySpend{}
}

// The card's number and the ceiling's number come from one definition.
func TestSpendReportsPerMonthAndAgreesWithTheCeiling(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	if _, err := e.owner.Exec(context.Background(), `DELETE FROM provider_run`); err != nil {
		t.Fatal(err)
	}
	three := 3
	// This month: one reconciled run charged 3, one unreconciled hold of 2.
	seedSpendRun(t, e, string(provider.RunCompleted), "email", 5, &three, 0)
	seedSpendRun(t, e, string(provider.RunInProgress), "email", 2, nil, 0)
	// Last month, a different pool, so the grouping has something to separate.
	seedSpendRun(t, e, string(provider.RunCompleted), "mobile", 1, nil, 1)
	// Neither of these bought anything, and neither may appear.
	seedSpendRun(t, e, string(provider.RunSkipped), "email", 4, nil, 0)
	seedSpendRun(t, e, string(provider.RunCancelled), "email", 4, nil, 0)

	current := spendFor(t, e, "current", "email")
	// 3 reconciled + 2 still held. An unreconciled hold counts at its full
	// reserved amount: assuming a refund nobody promised understates the bill.
	if current.Charged != 5 {
		t.Errorf("this month's email charge is %d, want 5 (3 actual + a 2-credit hold)", current.Charged)
	}
	if current.Runs != 2 {
		t.Errorf("this month counted %d runs, want 2 — skipped and cancelled runs bought nothing", current.Runs)
	}

	previous := spendFor(t, e, "previous", "mobile")
	if previous.Charged != 1 || previous.Pool != "mobile" {
		t.Errorf("last month's mobile spend is %+v, want 1 credit", previous)
	}

	// THE agreement: the ceiling reads the same rows the card shows, so the
	// number that refuses a run and the number a customer reads cannot differ.
	var connID string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id::text FROM provider_connection WHERE provider = 'surfe'`).Scan(&connID); err != nil {
		t.Fatal(err)
	}
	var used int
	if err := e.store.db.Tx(e.ctx, func(tx pgx.Tx) error {
		n, err := e.store.poolUsedThisMonth(e.ctx, tx, connID, "email")
		used = n
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if used != current.Charged {
		t.Errorf("the ceiling counts %d email credits this month and the card shows %d — a customer would read budget the next run does not have",
			used, current.Charged)
	}
}

// A run whose outcome was never learned is reported as held, separately.
func TestAnUnknownOutcomeIsHeldRatherThanFoldedIntoTheCharge(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	if _, err := e.owner.Exec(context.Background(), `DELETE FROM provider_run`); err != nil {
		t.Fatal(err)
	}
	seedSpendRun(t, e, string(provider.RunSubmissionUnknown), "email", 2, nil, 0)

	current := spendFor(t, e, "current", "email")
	if current.Held != 2 {
		t.Errorf("held = %d, want 2 — PI-AC-4's whole point is that we do not know, and a total that hid it would assert something the platform cannot support", current.Held)
	}
	// It still counts against the ceiling, because the credits may have gone.
	if current.Charged != 2 {
		t.Errorf("charged = %d, want 2 — a possibly-paid hold must keep occupying the budget", current.Charged)
	}
}

// Erasing a subject removes the subject, not the accounting.
func TestTheSpendHistorySurvivesAnErasure(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	if _, err := e.owner.Exec(context.Background(), `DELETE FROM provider_run`); err != nil {
		t.Fatal(err)
	}
	seedSpendRun(t, e, string(provider.RunCompleted), "email", 1, nil, 1)
	before := spendFor(t, e, "previous", "email")

	// The scrub the erasure performs: the run stops naming anybody and keeps
	// its reservations. Run here as the statement rather than through the
	// eraser, which lives in a module this package may not import.
	if _, err := e.owner.Exec(context.Background(), `
		UPDATE provider_run
		   SET person_id = NULL, subject_kind = 'scrubbed',
		       input_fingerprint = '', provider_job_id = NULL,
		       requested_by = NULL, configuration_snapshot = '{}'::jsonb
		 WHERE person_id = $1`, e.mine); err != nil {
		t.Fatal(err)
	}

	after := spendFor(t, e, "previous", "email")
	if after.Charged != before.Charged || after.Runs != before.Runs {
		t.Errorf("the month's spend changed from %+v to %+v when a subject was erased — what the installation paid is its own fact, and an Art. 17 request must not rewrite the books",
			before, after)
	}
}

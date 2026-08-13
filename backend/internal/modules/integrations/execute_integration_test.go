// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integrations

// The execution guarantees only a real database can prove, each written
// against the money rule the plan pinned:
//
//   - a queued run submits, polls and parks its claims-pending marker in the
//     terminal commit itself (PI-AC-12's crash window has no gap);
//   - an ambiguous submission parks in submission_unknown with its
//     reservation HELD — releasing it would let the next run double-spend;
//   - a disconnect before egress cancels without a call ever leaving;
//   - a disconnect while a run is in flight parks it, stores nothing, and
//     keeps the hold (PI-AC-4/5);
//   - a dead in-flight marker expires to submission_unknown, never a retry.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// sealCredential puts a key in the store's vault and points the singleton
// connection at it, the state Connect leaves behind.
func sealCredential(t *testing.T, e *runsEnv) {
	t.Helper()
	ctx := context.Background()
	// The run ledger is installation-global (no workspace column), so runs an
	// earlier test left behind would fall into this test's due-sweep. Each
	// execution test starts from an empty ledger.
	if _, err := e.owner.Exec(ctx, `DELETE FROM provider_run`); err != nil {
		t.Fatal(err)
	}
	ref, err := e.vault.Put(ctx, ids.From[ids.WorkspaceKind](e.ws), []byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		UPDATE provider_connection SET credential_ref = $1, execution_epoch = execution_epoch + 1
		 WHERE provider = 'surfe'`, string(ref)); err != nil {
		t.Fatal(err)
	}
}

func queueFor(t *testing.T, e *runsEnv, personID string) provider.Run {
	t.Helper()
	run, err := e.store.QueueRun(e.ctx, provider.QueueInput{
		PersonID: personID, Provider: "surfe", Trigger: provider.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != provider.RunQueued {
		t.Fatalf("run is %s, want queued", run.State)
	}
	return run
}

func runRow(t *testing.T, e *runsEnv, runID string) (state string, nextAttempt *time.Time, inflight *time.Time) {
	t.Helper()
	if err := e.owner.QueryRow(context.Background(), `
		SELECT state, next_attempt_at, inflight_at FROM provider_run WHERE id = $1`,
		runID).Scan(&state, &nextAttempt, &inflight); err != nil {
		t.Fatal(err)
	}
	return state, nextAttempt, inflight
}

// The full happy path against the fake, asserting the ONE property a crash
// cannot break: the claims-pending marker is written by the terminal commit,
// so a death between completion and hand-off leaves the sweep something to
// find. No claim writer is bound here, which IS that crash, permanently.
func TestSubmitPollParksTheClaimsPendingMarkerInTheTerminalCommit(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	run := queueFor(t, e, e.mine.String())

	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	state, _, _ := runRow(t, e, run.ID)
	if state != string(provider.RunInProgress) {
		t.Fatalf("after submit the run is %s, want in_progress", state)
	}

	// The sweep polls; the fake completes on the first poll. The hand-off then
	// fails (no claim writer is bound), which must NOT lose the marker.
	// Asserted on the message, not merely on non-nil: RunDueSweep joins every
	// due run's error, so any unrelated failure would otherwise satisfy this.
	err := e.store.RunDueSweep(e.ctx)
	if err == nil || !strings.Contains(err.Error(), "no claim writer is bound") {
		t.Fatalf("the sweep hid the failed hand-off — an unbound claim writer must surface, not pass silently: %v", err)
	}
	state, next, _ := runRow(t, e, run.ID)
	if state != string(provider.RunCompleted) {
		t.Fatalf("after poll the run is %s, want completed", state)
	}
	if next == nil {
		t.Fatal("the claims-pending marker is gone: a crash between the terminal commit and the hand-off would lose a paid result (PI-AC-12)")
	}

	// The reservation reconciled to what the provider actually charged.
	var actual int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT actual_credits FROM provider_run_reservation WHERE run_id = $1 AND pool = 'email'`,
		run.ID).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != 1 {
		t.Errorf("email pool reconciled to %d, want the fake's charge of 1", actual)
	}
}

// An ambiguous submission holds its reservation. poolUsedThisMonth excludes
// only skipped and cancelled, so the parked run keeps counting against the
// ceiling — releasing it would let the next run spend credits the customer
// may already have been charged.
func TestAmbiguousSubmissionParksUnknownAndHoldsTheReservation(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	// The fake keys its scenario off the subject's last name: "Ambiguous" is
	// the submission whose outcome is never learned.
	e.store.WithDomain(
		func(context.Context, pgx.Tx, string) (FenceVerdict, error) {
			return FenceVerdict{Allowed: true}, nil
		},
		nil,
		func(context.Context, pgx.Tx, string) (provider.PersonIdentifiers, error) {
			return provider.PersonIdentifiers{FirstName: "Anna", LastName: "Ambiguous", CompanyName: "Example"}, nil
		},
	)
	run := queueFor(t, e, e.mine.String())

	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	state, _, inflight := runRow(t, e, run.ID)
	if state != string(provider.RunSubmissionUnknown) {
		t.Fatalf("run is %s, want submission_unknown", state)
	}
	if inflight == nil {
		t.Fatal("inflight_at was cleared: it is the fact that the request may have landed, and only a definite refusal may clear it")
	}
	var held int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM provider_run_reservation
		 WHERE run_id = $1 AND actual_credits IS NULL`, run.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Fatal("the reservation was released on an unknown outcome — the next run could double-spend credits the customer may have been charged")
	}
}

// Disconnecting takes the provider's balance with the credential.
//
// The number was obtained BY presenting the key the disconnect destroys, so
// keeping it leaves the settings card showing "19 credits left" beside "Not
// connected" — a figure nothing can refresh and nobody has standing to assert.
// The customer's own ceilings survive, because those are their policy rather
// than the provider's reading.
func TestDisconnectClearsTheProviderBalanceAndKeepsTheCeilings(t *testing.T) {
	e := setupRuns(t, runsConfig{ceilings: map[string]int{"email": 25}})
	sealCredential(t, e)

	ctx := context.Background()
	if _, err := e.owner.Exec(ctx, `
		UPDATE provider_connection_budget b
		   SET last_known_balance = 19, balance_read_at = now()
		  FROM provider_connection c
		 WHERE c.id = b.connection_id AND c.provider = 'surfe'`); err != nil {
		t.Fatal(err)
	}

	// Its own principal: disconnecting is a DELETE on integrations, and the
	// shared rep context deliberately carries read only. Widening that would
	// weaken every other test in this file.
	admin := principal.WithActor(e.ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:admin-" + e.ws.String(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"integrations": {Read: true, Delete: true}},
			RowScope: principal.RowScopeAll,
		},
	})
	if err := e.store.Disconnect(admin, "surfe"); err != nil {
		t.Fatal(err)
	}

	var balance, ceiling *int
	var readAt *time.Time
	if err := e.owner.QueryRow(ctx, `
		SELECT b.last_known_balance, b.balance_read_at, b.monthly_ceiling
		  FROM provider_connection_budget b
		  JOIN provider_connection c ON c.id = b.connection_id
		 WHERE c.provider = 'surfe' AND b.pool = 'email'`).
		Scan(&balance, &readAt, &ceiling); err != nil {
		t.Fatal(err)
	}
	if balance != nil {
		t.Errorf("the balance survived the disconnect (%d credits) — the card would show credits for a key that no longer exists", *balance)
	}
	if readAt != nil {
		t.Errorf("the balance timestamp survived the disconnect (%s) — it dates a reading whose credential is gone", readAt.Format(time.RFC3339))
	}
	if ceiling == nil || *ceiling != 25 {
		t.Errorf("the monthly ceiling is %v, want 25 — disconnecting withdraws the provider's number, not the customer's own limit", ceiling)
	}
}

// A connection withdrawn while the run is still queued cancels it before any
// call leaves — and the egress counter proves the negative.
func TestDisconnectBeforeSubmitCancelsWithoutEgress(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	run := queueFor(t, e, e.mine.String())

	if _, err := e.owner.Exec(context.Background(), `
		UPDATE provider_connection SET status = 'disconnected', credential_ref = NULL,
		       execution_epoch = execution_epoch + 1
		 WHERE provider = 'surfe'`); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Calls()
	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	state, _, _ := runRow(t, e, run.ID)
	if state != string(provider.RunCancelled) {
		t.Fatalf("run is %s, want cancelled — nothing was spent, so cancelling is honest", state)
	}
	if e.fake.Calls() != before {
		t.Fatal("the adapter was called after the disconnect (PI-AC-5)")
	}
}

// A disconnect that lands while a run is in flight must park the run, store
// no result, and keep the hold: the purchase may have happened, and
// disconnecting is not un-spending.
func TestDisconnectInFlightParksTheRunAndStoresNothing(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	run := queueFor(t, e, e.mine.String())
	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := e.owner.Exec(context.Background(), `
		UPDATE provider_connection SET status = 'disconnected', credential_ref = NULL,
		       execution_epoch = execution_epoch + 1
		 WHERE provider = 'surfe'`); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Calls()
	if err := e.store.RunDueSweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	state, _, _ := runRow(t, e, run.ID)
	if state != string(provider.RunSubmissionUnknown) {
		t.Fatalf("run is %s, want submission_unknown — the outcome exists but may no longer be fetched", state)
	}
	if e.fake.Calls() != before {
		t.Fatal("the adapter was polled after the disconnect (PI-AC-5)")
	}
	var held int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM provider_run_reservation WHERE run_id = $1`, run.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Fatal("the hold was released for a run that may have been paid")
	}
}

// An AMBIGUOUS POLL is terminal but not a refusal, and terminalize used to
// route it into recordRefusal — which zeroes actual_credits on a
// per-successful-result provider, releasing credits the customer may already
// have been charged and letting an identical retry buy the same answer again.
func TestAmbiguousPollHoldsTheReservationAndParksUnknown(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	run := queueFor(t, e, e.mine.String())
	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// The run is in flight; the provider's poll comes back indeterminate.
	if err := e.store.settlePoll(e.ctx, e.fake.Descriptor(), "surfe", run.ID,
		pollLease{epoch: currentEpoch(t, e), jobID: "offline-x", person: e.mine.String()},
		provider.PollStatus{Outcome: provider.OutcomeAmbiguous, SafeStatusCode: "poll_timeout"}); err != nil {
		t.Fatal(err)
	}

	state, _, _ := runRow(t, e, run.ID)
	if state != string(provider.RunSubmissionUnknown) {
		t.Fatalf("an ambiguous poll left the run %s, want submission_unknown", state)
	}
	var released int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM provider_run_reservation
		 WHERE run_id = $1 AND actual_credits = 0`, run.ID).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released > 0 {
		t.Fatal("an ambiguous poll released the hold — the ceiling now under-counts a possible charge and an identical retry could buy it again")
	}
}

// currentEpoch reads the connection's live execution epoch, so a test can
// build a lease the settle path will accept.
func currentEpoch(t *testing.T, e *runsEnv) int64 {
	t.Helper()
	var epoch int64
	if err := e.owner.QueryRow(context.Background(),
		`SELECT execution_epoch FROM provider_connection WHERE provider = 'surfe'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// A hand-off that can never proceed must still exhaust. The ladder used to
// advance only after a successful lease, so a completed run whose connection
// was withdrawn stayed due forever: marker set, counter frozen, and the UI
// reporting a clean `completed` while the paid values never arrived.
func TestAWithdrawnConnectionExhaustsThePendingHandoffRatherThanLoopingForever(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	run := queueFor(t, e, e.mine.String())
	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// Complete it (no claim writer is bound, so the hand-off fails and the
	// pending marker stands), then withdraw the connection.
	if err := e.store.RunDueSweep(e.ctx); err == nil || !strings.Contains(err.Error(), "no claim writer is bound") {
		t.Fatalf("the unbound claim writer did not surface: %v", err)
	}
	if _, err := e.owner.Exec(context.Background(), `
		UPDATE provider_connection SET status = 'disconnected', credential_ref = NULL,
		       execution_epoch = execution_epoch + 1
		 WHERE provider = 'surfe'`); err != nil {
		t.Fatal(err)
	}

	// Five due passes. Each is one spent attempt whether or not it could
	// lease, so the ladder reaches its cap and the run stops being due.
	for i := 0; i < claimAttemptCap; i++ {
		if _, err := e.owner.Exec(context.Background(),
			`UPDATE provider_run SET next_attempt_at = now() - interval '1 minute' WHERE id = $1`,
			run.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.store.RunDueSweep(e.ctx); err != nil {
			t.Fatal(err)
		}
	}

	var unwritten bool
	var next *time.Time
	if err := e.owner.QueryRow(context.Background(),
		`SELECT claims_unwritten, next_attempt_at FROM provider_run WHERE id = $1`,
		run.ID).Scan(&unwritten, &next); err != nil {
		t.Fatal(err)
	}
	if !unwritten {
		t.Error("the hand-off never exhausted: the run still reports completed with claims delivered, which is not what happened")
	}
	if next != nil {
		t.Error("the run is still due: the sweep will re-select it forever")
	}
}

// An accepted job the provider never resolves must not poll forever: the
// live-run index covers in_progress, so a stuck run would block every future
// enrichment of that subject and hold its credits against the month.
func TestAnUnresolvableInProgressRunExpiresRatherThanPollingForever(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	run := queueFor(t, e, e.mine.String())
	if err := e.store.ExecuteSubmit(e.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE provider_run SET submitted_at = now() - interval '2 hours' WHERE id = $1`,
		run.ID); err != nil {
		t.Fatal(err)
	}

	before := e.fake.Calls()
	if err := e.store.RunDueSweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	state, _, _ := runRow(t, e, run.ID)
	if state != string(provider.RunSubmissionUnknown) {
		t.Fatalf("the unresolvable run is %s, want submission_unknown", state)
	}
	if e.fake.Calls() != before {
		t.Error("the expired run was polled again — expiry must never cause egress")
	}
	var held int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM provider_run_reservation WHERE run_id = $1 AND actual_credits IS NULL`,
		run.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held == 0 {
		t.Error("expiry released the hold on a run that may have been paid")
	}
}

// A submission whose worker died expires to submission_unknown — never a
// resubmit, because a retry is how one ambiguous charge becomes two certain
// ones. A fresh in-flight marker is left alone.
func TestDeadInflightExpiresToUnknownAndFreshOnesStand(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	sealCredential(t, e)
	dead := queueFor(t, e, e.mine.String())

	if _, err := e.owner.Exec(context.Background(), `
		UPDATE provider_run SET state = 'submitting', inflight_at = now() - interval '11 minutes'
		 WHERE id = $1`, dead.ID); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Calls()
	if err := e.store.RunDueSweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	state, _, inflight := runRow(t, e, dead.ID)
	if state != string(provider.RunSubmissionUnknown) {
		t.Fatalf("the dead submission is %s, want submission_unknown", state)
	}
	if inflight == nil {
		t.Fatal("inflight_at was cleared on expiry — it is the fact the run carries")
	}
	if e.fake.Calls() != before {
		t.Fatal("expiry caused egress: an expired submission must never be resubmitted")
	}

	// A FRESH in-flight marker is not expired: the worker holding it may be
	// mid-call right now.
	if _, err := e.owner.Exec(context.Background(), `
		UPDATE provider_run SET state = 'submitting', inflight_at = now() - interval '1 minute'
		 WHERE id = $1`, dead.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.store.RunDueSweep(e.ctx); err != nil {
		t.Fatal(err)
	}
	state, _, _ = runRow(t, e, dead.ID)
	if state != string(provider.RunSubmitting) {
		t.Fatalf("a fresh in-flight submission was expired to %s", state)
	}
}

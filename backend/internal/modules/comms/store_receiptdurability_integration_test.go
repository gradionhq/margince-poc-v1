// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// THE RECEIPT STANDS. Once the provider has accepted a message, the record that
// it did must survive everything that can go wrong afterwards — a seam that
// refuses, a statement Postgres aborts, a transaction that cannot be committed,
// a cancelled job, a panic, and the fault report's own failure. Each case here
// is one of those, and each asserts the same two facts: the delivery is durably
// sent, and RecordSent reported nothing the dispatcher could retry on.
//
// The stakes are why this is a suite rather than a case. A receipt that fails
// to record sends the delivery back to River, whose prior-send lookup searches
// for an identity Gmail discarded — it finds nothing, and the recipient is
// mailed a second time. The fixture, and what RecordSent does when nothing goes
// wrong, live next door in store_recordsent_integration_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// faultingReconciler is the Go-level fault: the seam refuses without ever
// reaching the database.
type faultingReconciler struct{ err error }

func (r faultingReconciler) ReconcileMessageIdentityTx(context.Context, pgx.Tx, ids.ActivityID, string, string) error {
	return r.err
}

// collidingReconciler is the DATABASE-level fault, and it is a different test
// entirely: it runs the real statement the real reconciler runs, against a
// workspace where a captured echo already holds the stamped natural key. The
// unique index answers with SQLSTATE 23505, which aborts the transaction it
// runs in — so a stub that merely returned a Go error would prove nothing about
// a receipt that has to survive one.
type collidingReconciler struct{}

func (collidingReconciler) ReconcileMessageIdentityTx(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, _, stamped string) error {
	_, err := tx.Exec(ctx, `
		UPDATE activity SET source_system = 'gmail', source_id = $2 WHERE id = $1`,
		activityID, stamped)
	return err
}

// panickingReconciler is the fault nobody plans: the seam does not refuse, it
// comes apart. A future editor's index-out-of-range, a nil map write, a typed
// nil behind an interface — the shape varies and the consequence does not.
type panickingReconciler struct{}

func (panickingReconciler) ReconcileMessageIdentityTx(context.Context, pgx.Tx, ids.ActivityID, string, string) error {
	panic("the message-identity seam came apart")
}

// connectionLosingReconciler makes the re-key's own transaction stop being
// usable. Closing the connection under it is the honest way to produce that:
// the statements have run, and it is the COMMIT that then cannot be issued —
// the failure no guard placed INSIDE a transaction can answer.
type connectionLosingReconciler struct{}

func (connectionLosingReconciler) ReconcileMessageIdentityTx(ctx context.Context, tx pgx.Tx, _ ids.ActivityID, _, _ string) error {
	return tx.Conn().Close(ctx)
}

// unreportableFault is an error that comes apart when it is asked what went
// wrong — the fault report's own path panicking, one call after the reconcile
// already failed. Contrived as a value and not as a shape: rendering a cause
// into a breadcrumb runs arbitrary Error() implementations, and this is the
// moment the code is furthest from anything a test usually reaches.
type unreportableFault struct{}

func (unreportableFault) Error() string { panic("the reconcile fault cannot describe itself") }

// cancellingSender is the provider that accepted the message as the ground
// moved: it hands back a receipt and cancels the dispatch context in the same
// breath. A job deadline expiring during the send or the read-back, and a
// worker shutting down between them, arrive here identically.
type cancellingSender struct{ cancel context.CancelFunc }

func (s cancellingSender) SendEmail(context.Context, connector.Auth, connector.EmailMessage) (connector.SendReceipt, error) {
	s.cancel()
	return connector.SendReceipt{ProviderMessageID: "gmsg-cancelled", RFC822MessageID: stampedIdentity}, nil
}

// A reconcile fault must never un-send a sent message. The receipt is the
// record that the provider ACCEPTED the message; rolling it back returns the
// delivery to a retry ladder whose prior-send lookup cannot see a rewritten
// identity, and the recipient is mailed a second time. A bookkeeping failure
// costs one duplicate timeline row, never a duplicate email.
func TestRecordSentKeepsTheReceiptWhenTheReconcileFails(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))
	fault := errors.New("activity is unavailable")

	if err := e.storeWith(faultingReconciler{err: fault}).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-1", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent over a faulting reconcile: %v — a bookkeeping fault must not surface as a failed send", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent — a pending row goes back on the ladder and the recipient is mailed twice", status)
	}
	if providerMessageID != "gmsg-1" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q): the whole re-key was rolled back",
			messageID, stagedIdentity)
	}
	if n := e.reconcileFaults(t); n != 1 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 1 — a silent degradation is one an operator never learns about", n)
	}
}

// A store with NO reconciler at all is the same fault as a reconciler that
// refuses, and must cost the same. nil is constructible so a read-only role can
// build one without the seam, and a wiring mistake must not turn that into a
// failed send: the breadcrumb names the misconfiguration where an operator
// reads, and the receipt for an already-transmitted message stands.
func TestRecordSentKeepsTheReceiptWhenTheStoreHasNoReconciler(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))

	if err := e.storeWith(nil).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-5", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent on a store with no reconciler: %v — a wiring fault must not surface as a failed send", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent", status)
	}
	if providerMessageID != "gmsg-5" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	if n := e.reconcileFaults(t); n != 1 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 1 — the role that cannot reconcile must say so where an operator reads", n)
	}
}

// A real Postgres statement error, not only a Go one: a failed statement aborts
// the transaction it runs in, and this is the case that shows the receipt is
// not in that transaction. Written together, the receipt's own UPDATE would go
// down with the aborted re-key and never commit.
func TestRecordSentKeepsTheReceiptWhenTheReconcileHitsAUniqueViolation(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))
	// The captured echo that won the race: it already holds the natural key the
	// re-key is about to claim.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE activity SET source_system = 'gmail', source_id = $2 WHERE id = $1`,
		e.activity2, stampedIdentity); err != nil {
		t.Fatalf("seeding the captured echo: %v", err)
	}

	if err := e.storeWith(collidingReconciler{}).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-2", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent over a colliding reconcile: %v", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent", status)
	}
	if providerMessageID != "gmsg-2" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	if n := e.reconcileFaults(t); n != 1 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 1", n)
	}
	// The echo is proof the violation was real: had it not been there, the
	// re-key would have succeeded and this case would test nothing.
	var echoes int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM activity WHERE workspace_id = $1 AND source_system = 'gmail' AND source_id = $2`,
		e.ws, stampedIdentity).Scan(&echoes); err != nil {
		t.Fatalf("counting rows on the stamped key: %v", err)
	}
	if echoes != 1 {
		t.Fatalf("%d activities hold the stamped identity, want 1 (the echo alone) — the collision this case rests on did not happen", echoes)
	}
}

// A PANIC in the seam costs exactly what a returned error costs. It is not an
// error the caller can inspect, so nothing about it can be handled — but the
// consequence of letting it escape is the one thing this ordering exists to
// prevent: it would unwind through WithWorkspaceTx's deferred rollback, take
// the receipt for an already-transmitted message with it, fail the job, and let
// the redelivery mail the recipient a second time.
func TestRecordSentKeepsTheReceiptWhenTheReconcilePanics(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))

	if err := e.storeWith(panickingReconciler{}).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-6", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent over a panicking reconcile: %v — a panic in bookkeeping must not surface as a failed send", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent — a pending row goes back on the ladder and the recipient is mailed twice", status)
	}
	if providerMessageID != "gmsg-6" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	if n := e.reconcileFaults(t); n != 1 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 1 — a panic an operator never hears about is one nobody fixes", n)
	}
}

// A DISPATCH WHOSE CONTEXT IS CANCELLED MID-SEND STILL RECORDS THE RECEIPT.
// This is the whole reason the two writes below run detached from the caller's
// context. Gmail has accepted the message by the time Send returns; if the job
// deadline expires or the worker is cancelled at that moment, a receipt written
// on the job's own context cannot begin, execute or commit. The dispatcher
// would answer OutcomeRetry, River would redeliver, and the connector's
// prior-send lookup — searching for an identity Gmail discarded — would find
// nothing and transmit the message again.
//
// It drives the real dispatcher over the real store, because the outcome IS the
// assertion: nothing shorter can show that a cancelled attempt does not come
// back on the ladder.
func TestDispatchRecordsTheReceiptWhenTheJobContextIsCancelledDuringTheSend(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))
	ctx, cancel := context.WithCancel(e.asSendWorker())
	defer cancel()

	dispatcher := NewDispatcher(
		e.storeWith(&recordingReconciler{}),
		fakeResolver{sender: cancellingSender{cancel: cancel}, granted: []string{sendScope}},
		liveSeat(), &stubConsent{}, nil,
		func() time.Time { return e.clockValue }, time.Hour, testMaxAttempts,
	)

	outcome, _, err := dispatcher.DispatchWithWait(ctx, id)
	if err != nil {
		t.Fatalf("dispatch over a cancelled context: %v — the send completed, so nothing here is the dispatcher's to report", err)
	}
	if outcome != OutcomeSent {
		t.Fatalf("dispatch outcome = %q, want sent — a retry re-transmits a message the provider already accepted", outcome)
	}
	if ctx.Err() == nil {
		t.Fatal("the dispatch context was never cancelled — this case would pass against a receipt that has no protection at all")
	}
	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent — a delivery left pending by a cancellation is one the recipient gets twice", status)
	}
	if providerMessageID != "gmsg-cancelled" {
		t.Errorf("provider_message_id = %q, want the receipt the provider handed back", providerMessageID)
	}
	// The re-key detaches too, and this is what says so: it runs in a SECOND
	// transaction, opened after the cancellation, so a delivery still holding
	// the staged identity means that transaction could not begin. The cost of
	// getting it wrong is smaller than a double-send and invisible without an
	// assertion — a message filed under an identity that exists in no mailbox.
	if messageID != stampedIdentity {
		t.Errorf("message_id = %q, want the stamped identity %q — the reconcile ran on the cancelled context",
			messageID, stampedIdentity)
	}
}

// A re-key transaction that cannot be COMMITTED costs one breadcrumb and never
// the receipt. The statements ran and it is the commit that fails, so no guard
// inside that transaction can answer it — the receipt is safe because it is not
// in there, having committed on its own connection before the re-key began.
func TestRecordSentKeepsTheReceiptWhenTheReconcilesTransactionIsLost(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))

	if err := e.storeWith(connectionLosingReconciler{}).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-9", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent over a lost reconcile connection: %v — the receipt had committed before the re-key was attempted", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent", status)
	}
	if providerMessageID != "gmsg-9" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	// The breadcrumb lands on a connection of its own, which is the point: the
	// report survives the transaction that could not be committed.
	if n := e.reconcileFaults(t); n != 1 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 1", n)
	}
}

// A PANIC IN THE FAULT-REPORTING PATH, one call after the reconcile already
// failed. Rendering a cause into a breadcrumb runs an arbitrary Error()
// implementation, so this is real code, not only a guard against a future
// editor — and it is the last place anything is still running when a send has
// gone wrong. Escaping here would unwind the dispatch attempt and cost the
// second email the whole ordering exists to prevent.
func TestRecordSentKeepsTheReceiptWhenTheFaultReportItselfPanics(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))

	if err := e.storeWith(faultingReconciler{err: unreportableFault{}}).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-10", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent when the fault report panicked: %v — reporting a fault must not become one", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent", status)
	}
	if providerMessageID != "gmsg-10" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	// The breadcrumb is the write that came apart, so its absence is the case
	// holding rather than a second failure.
	if n := e.reconcileFaults(t); n != 0 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 0 — the report this case makes panic must not have landed", n)
	}
}

// THE FAULT REPORT MUST NOT BE THE FAULT. The breadcrumb is an INSERT, and
// Postgres may refuse any statement; a refused report is a degradation nobody
// is told about, and it must cost nothing more than that. Written on the
// receipt's own transaction it would cost more — a refusal aborts a
// transaction, so the receipt would fail to commit, the dispatcher would answer
// retry, and the code that exists to report a fault would have caused a second
// email.
//
// The refusal is driven with data rather than schema: a NUL byte in the cause's
// message reaches `detail` as an escape jsonb cannot store. Any other
// refusal — a constraint, an RLS WITH CHECK, a full disk — fails the write
// identically, and this one needs no DDL on a shared database.
func TestRecordSentKeepsTheReceiptWhenTheBreadcrumbItselfCannotBeWritten(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))
	unloggable := errors.New("activity is unavailable\x00")

	if err := e.storeWith(faultingReconciler{err: unloggable}).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-7", RFC822MessageID: stampedIdentity}); err != nil {
		t.Fatalf("RecordSent when the breadcrumb could not be written: %v — the report of a fault must not become one", err)
	}

	status, providerMessageID, messageID := e.receipt(t, id)
	if status != StatusSent {
		t.Errorf("status = %q, want sent", status)
	}
	if providerMessageID != "gmsg-7" {
		t.Errorf("provider_message_id = %q, want the receipt's", providerMessageID)
	}
	if messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	// The breadcrumb is the row that could not be written, so its absence is
	// the case holding rather than a second failure: what is asserted is that
	// everything above it still reads back.
	if n := e.reconcileFaults(t); n != 0 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 0 — the write this case makes fail must not have landed", n)
	}
}

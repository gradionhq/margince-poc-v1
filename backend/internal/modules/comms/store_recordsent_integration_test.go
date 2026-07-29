// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// What RecordSent does with a receipt whose provider rewrote the message
// identity, and — the reason this file exists — what it does when the re-key
// that follows goes wrong. The receipt is the record that the provider ACCEPTED
// the message; nothing about bookkeeping may roll it back.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The two identities: what this system minted and staged under, and what the
// provider actually stamped on the transmitted copy.
const (
	stagedIdentity  = "019fad38-minted@margince.test"
	stampedIdentity = "CAFAR1txEuKW@mail.gmail.com"
	// conversationRoot is the identity of a message this workspace did not
	// write: the root of a thread a reply JOINS, which no re-key may move.
	conversationRoot = "root@buyer.test"
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
// unique index answers with SQLSTATE 23505, which aborts the whole surrounding
// transaction unless it is rolled back to a savepoint — so a stub that merely
// returned a Go error would prove nothing about the shape this file guards.
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

// recordingReconciler is the honoured path made observable: it writes nothing
// and remembers what the delivery store asked it to re-key.
type recordingReconciler struct {
	calls    int
	activity ids.ActivityID
	previous string
	stamped  string
}

func (r *recordingReconciler) ReconcileMessageIdentityTx(_ context.Context, _ pgx.Tx, activityID ids.ActivityID, previous, stamped string) error {
	r.calls++
	r.activity, r.previous, r.stamped = activityID, previous, stamped
	return nil
}

// storeWith rebuilds the fixture's store over a different reconciler, keeping
// the injected clock so timestamps stay assertable.
func (e *storeEnv) storeWith(identity MessageIdentityReconciler) *Store {
	return NewStore(e.store.pool, e.store.now, identity)
}

// asSendWorker is the scope the dispatch job binds: the system completing a
// send a human already authorized. The reconcile's audit row, outbox event and
// fault breadcrumb all need an actor, and the first two need a correlation id
// as well — the workspace alone is not enough.
func (e *storeEnv) asSendWorker() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:comms-send",
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// receipt reads back the three facts a receipt is made of.
func (e *storeEnv) receipt(t *testing.T, id ids.UUID) (status, providerMessageID, messageID string) {
	t.Helper()
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status, coalesce(provider_message_id, ''), message_id FROM comms_outbound WHERE id = $1`,
		id).Scan(&status, &providerMessageID, &messageID); err != nil {
		t.Fatalf("reading the delivery back: %v", err)
	}
	return status, providerMessageID, messageID
}

func (e *storeEnv) reconcileFaults(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM system_log WHERE workspace_id = $1 AND action = 'comms_identity_reconcile_failed'`,
		e.ws).Scan(&n); err != nil {
		t.Fatalf("counting reconcile-fault breadcrumbs: %v", err)
	}
	return n
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
		t.Errorf("message_id = %q, want the staged identity untouched (%q): the savepoint took the whole re-key back",
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

// The savepoint must survive a real Postgres statement error, not only a Go
// error: a failed statement aborts the surrounding transaction unless it is
// rolled back to a savepoint, so without one the receipt's own UPDATE would
// never commit either.
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

// THE FAULT REPORT MUST NOT BE THE FAULT. The breadcrumb is an INSERT, and
// Postgres may refuse any statement; a refusal on the bare transaction aborts
// it, so the receipt would fail to commit, the dispatcher would answer retry,
// and the recipient would be mailed twice — caused by the code that exists to
// report that something went wrong.
//
// The refusal is driven with data rather than schema: a NUL byte in the cause's
// message reaches `detail` as an escape jsonb cannot store. Any other
// refusal — a constraint, an RLS WITH CHECK, a full disk — poisons the
// transaction identically, and this one needs no DDL on a shared database.
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
	// the case holding rather than a second failure: without the savepoint the
	// assertions above could not have been read at all.
	if n := e.reconcileFaults(t); n != 0 {
		t.Errorf("%d reconcile-fault breadcrumbs, want 0 — the write this case makes fail must not have landed", n)
	}
}

// An identity the provider reports but no message could carry is refused
// before it becomes a natural key. It arrives from a remote response, and
// everything downstream — the echo collapse, the reply join, the threading
// headers — reads that column as a searchable identity.
func TestRecordSentRefusesAnIdentityNoMessageCouldCarry(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, stagedIdentity))
	reconciler := &recordingReconciler{}

	if err := e.storeWith(reconciler).RecordSent(e.asSendWorker(), id,
		connector.SendReceipt{ProviderMessageID: "gmsg-8", RFC822MessageID: strings.Repeat("a", 100_000) + "@mail.gmail.com"}); err != nil {
		t.Fatalf("RecordSent over an unusable identity: %v", err)
	}

	if _, _, messageID := e.receipt(t, id); messageID != stagedIdentity {
		t.Errorf("message_id = %q, want the staged identity untouched (%q)", messageID, stagedIdentity)
	}
	if reconciler.calls != 0 {
		t.Errorf("the reconciler was asked %d times, want none — there is no usable identity to move to", reconciler.calls)
	}
	// The breadcrumb records the refusal, and records it BOUNDED: the rejected
	// value is unbounded provider input, and copying it verbatim would make
	// every such send cost a hundred kilobytes of operational log.
	var detail string
	if err := e.owner.QueryRow(context.Background(), `
		SELECT detail->>'provider_message_id' FROM system_log
		 WHERE workspace_id = $1 AND action = 'comms_identity_reconcile_failed'`,
		e.ws).Scan(&detail); err != nil {
		t.Fatalf("reading the refusal breadcrumb back: %v", err)
	}
	if len(detail) > 200 {
		t.Errorf("the breadcrumb copied %d bytes of the provider's answer, want a bounded rendering", len(detail))
	}
	if !strings.Contains(detail, "100015 bytes") {
		t.Errorf("breadcrumb detail = %q, want it to name the size of what was refused", detail)
	}
}

// The delivery's own copy of the identity moves with the activity's, and its
// thread_key follows ONLY when it equalled the message's own identity: a root
// send re-roots onto what the world will reply to, a reply keeps the
// conversation root it joined.
func TestRecordSentReKeysTheDeliveryWhenTheIdentityMoved(t *testing.T) {
	for _, tc := range []struct {
		name          string
		threadKey     string
		wantThreadKey string
	}{
		{"a root send re-roots", stagedIdentity, stampedIdentity},
		{"a reply keeps its anchor's root", conversationRoot, conversationRoot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupStore(t)
			in := e.baseInput(e.activity, stagedIdentity)
			in.ThreadKey = tc.threadKey
			id := e.stage(t, in)
			reconciler := &recordingReconciler{}

			if err := e.storeWith(reconciler).RecordSent(e.asSendWorker(), id,
				connector.SendReceipt{ProviderMessageID: "gmsg-3", RFC822MessageID: stampedIdentity}); err != nil {
				t.Fatalf("RecordSent: %v", err)
			}

			_, _, messageID := e.receipt(t, id)
			if messageID != stampedIdentity {
				t.Errorf("message_id = %q, want the stamped identity %q", messageID, stampedIdentity)
			}
			var threadKey string
			if err := e.owner.QueryRow(context.Background(),
				`SELECT coalesce(thread_key, '') FROM comms_outbound WHERE id = $1`, id).Scan(&threadKey); err != nil {
				t.Fatalf("reading the delivery's thread key: %v", err)
			}
			if threadKey != tc.wantThreadKey {
				t.Errorf("thread_key = %q, want %q", threadKey, tc.wantThreadKey)
			}
			// The seam is handed the identity the message was STAGED under, not
			// the one it now carries: the activity side tells a root from a
			// reply by comparing thread_key against it.
			if reconciler.calls != 1 {
				t.Fatalf("the reconciler was asked %d times, want exactly 1", reconciler.calls)
			}
			if reconciler.activity != e.activity {
				t.Errorf("reconciled activity %s, want the delivery's %s", reconciler.activity, e.activity)
			}
			if reconciler.previous != stagedIdentity || reconciler.stamped != stampedIdentity {
				t.Errorf("reconciled (%q → %q), want (%q → %q)",
					reconciler.previous, reconciler.stamped, stagedIdentity, stampedIdentity)
			}
			if n := e.reconcileFaults(t); n != 0 {
				t.Errorf("%d reconcile-fault breadcrumbs on a clean re-key, want 0", n)
			}
		})
	}
}

// A provider that honoured the identity reports it unchanged, and a provider
// that reports none reports nothing: both leave the staged key alone and must
// not cost a seam call, a write, or a breadcrumb.
func TestRecordSentLeavesTheIdentityAloneWhenTheProviderHonouredIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stamped string
	}{
		{"honoured", stagedIdentity},
		{"not reported", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupStore(t)
			id := e.stage(t, e.baseInput(e.activity, stagedIdentity))
			reconciler := &recordingReconciler{}

			if err := e.storeWith(reconciler).RecordSent(e.asSendWorker(), id,
				connector.SendReceipt{ProviderMessageID: "gmsg-4", RFC822MessageID: tc.stamped}); err != nil {
				t.Fatalf("RecordSent: %v", err)
			}

			if _, _, messageID := e.receipt(t, id); messageID != stagedIdentity {
				t.Errorf("message_id = %q, want the staged identity %q", messageID, stagedIdentity)
			}
			if reconciler.calls != 0 {
				t.Errorf("the reconciler was asked %d times, want none — there is no identity to move", reconciler.calls)
			}
		})
	}
}

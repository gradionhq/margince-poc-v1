// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// What RecordSent does with a receipt whose provider rewrote the message
// identity: the delivery moves onto the identity the wire carries, a reply
// keeps the conversation root it joined, a provider that honoured the identity
// costs nothing, and one that answers with a value no message could carry is
// refused. This file also carries the fixture the durability suite next door
// rides (store_receiptdurability_integration_test.go), which is about what
// happens when the re-key goes wrong rather than when it works.

import (
	"context"
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

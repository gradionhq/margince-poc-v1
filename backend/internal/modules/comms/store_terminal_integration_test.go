// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// Finding 2 (security round 1): RecordSent/Park/RecordFailure are
// status-guarded, and a stale transition on an already-terminal row is a
// benign no-op reported as ErrTerminal, never a silent overwrite of a
// newer attempt's outcome. Shares storeEnv/setupStore/stage/baseInput with
// store_integration_test.go.

import (
	"context"
	"testing"
)

// A stale second RecordSent (a network partition or GC pause outliving a
// redelivered attempt) must not clobber the first attempt's real receipt.
func TestRecordSentOnAnAlreadySentDeliveryIsABenignNoOp(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-sent-twice@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := e.store.RecordSent(e.ctx, id, "receipt-first"); err != nil {
		t.Fatalf("first RecordSent: %v", err)
	}
	if err := e.store.RecordSent(e.ctx, id, "receipt-stale-retry"); err != ErrTerminal {
		t.Fatalf("second RecordSent on an already-sent delivery: got %v, want ErrTerminal", err)
	}

	var providerMessageID *string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT provider_message_id FROM comms_outbound WHERE id = $1`, id).Scan(&providerMessageID); err != nil {
		t.Fatal(err)
	}
	if providerMessageID == nil || *providerMessageID != "receipt-first" {
		t.Fatalf("provider_message_id = %v after a stale second RecordSent, want the FIRST receipt untouched", providerMessageID)
	}
}

// A stale second Park must not clobber the first Park's reason.
func TestParkOnAnAlreadyParkedDeliveryIsABenignNoOp(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-parked-twice@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := e.store.Park(e.ctx, id, "first reason"); err != nil {
		t.Fatalf("first Park: %v", err)
	}
	if err := e.store.Park(e.ctx, id, "stale second reason"); err != ErrTerminal {
		t.Fatalf("second Park on an already-parked delivery: got %v, want ErrTerminal", err)
	}

	var reason *string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT reason FROM comms_outbound WHERE id = $1`, id).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason == nil || *reason != "first reason" {
		t.Fatalf("reason = %v after a stale second Park, want the FIRST reason untouched", reason)
	}
}

// A stale RecordSent losing the race against a NEWER Park must not
// resurrect the delivery as sent — the cross-transition race, not just the
// same-transition one.
func TestRecordSentOnAParkedDeliveryIsABenignNoOp(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-parked-then-sent@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := e.store.Park(e.ctx, id, "recipient permanently rejected"); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := e.store.RecordSent(e.ctx, id, "stale-receipt"); err != ErrTerminal {
		t.Fatalf("RecordSent on a parked delivery: got %v, want ErrTerminal", err)
	}

	var status string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status FROM comms_outbound WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != StatusParked {
		t.Fatalf("status = %q after a stale RecordSent on a parked delivery, want parked (untouched)", status)
	}
}

// RecordFailure shares the same status guard: a stale transient-failure
// report arriving after a newer attempt already closed the delivery must
// not reopen it.
func TestRecordFailureOnAnAlreadySentDeliveryReportsErrTerminal(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-failure-after-sent@example.com"))
	if _, err := e.store.Load(e.ctx, id); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := e.store.RecordSent(e.ctx, id, "receipt"); err != nil {
		t.Fatalf("RecordSent: %v", err)
	}
	if err := e.store.RecordFailure(e.ctx, id, "stale failure"); err != ErrTerminal {
		t.Fatalf("RecordFailure on an already-sent delivery: got %v, want ErrTerminal", err)
	}
}

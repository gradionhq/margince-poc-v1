// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package comms

// attempts counts TRANSMISSION attempts, over the real counter.
//
// Two readers depend on that meaning: the exhaustion guard, which parks a
// delivery whose ladder is spent, and the connector's prior-send lookup, which
// fires on a non-zero count because a previous attempt may already have put
// the message on the wire. A dispatch that ends in a deferral reached no
// provider, so it must consume no rung — otherwise a merely BUSY mailbox
// parks legitimate mail as "ladder exhausted" after N pacing windows, without
// the message ever having been attempted, and the maximum-age bound the
// deployment configures becomes unreachable.
//
// The fixture (setupStore/stage/baseInput) lives in store_integration_test.go;
// the fake sender/resolver/consent in dispatcher_test.go.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// deliveryRow is what an operator (and the exhaustion guard) actually sees.
// Read through the pool rather than through Load, which would count an attempt
// of its own and change the very number under test.
func (e *storeEnv) deliveryRow(t *testing.T, id ids.UUID) (status string, attempts int, reason string) {
	t.Helper()
	var note *string
	if err := database.WithWorkspaceTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status, attempts, reason FROM comms_outbound WHERE id = $1`, id).
			Scan(&status, &attempts, &note)
	}); err != nil {
		t.Fatalf("reading delivery %s: %v", id, err)
	}
	if note != nil {
		reason = *note
	}
	return status, attempts, reason
}

func TestPacingADeliveryPastTheLadderNeverSpendsATransmitAttempt(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-paced@example.com"))

	const (
		window  = time.Minute
		maxAge  = 4 * time.Hour
		ladder  = 3
		windows = 10 // deliberately far more deferrals than the ladder is long
	)
	now := e.clockValue
	sender := &fakeSender{}
	d := NewDispatcher(e.store,
		fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{},
		[]SendPolicy{waitPolicy{d: window}},
		func() time.Time { return now }, maxAge, ladder)

	for i := range windows {
		outcome, wait, err := d.DispatchWithWait(e.ctx, id)
		if err != nil {
			t.Fatalf("deferral %d: %v", i, err)
		}
		if outcome != OutcomePostponed || wait != window {
			t.Fatalf("deferral %d → %v/%v, want OutcomePostponed/%s", i, outcome, wait, window)
		}
		status, attempts, reason := e.deliveryRow(t, id)
		if status != StatusPending {
			t.Fatalf("after %d deferral(s) the delivery is %q (%s) — a paced message must stay live", i+1, status, reason)
		}
		if attempts != 0 {
			t.Fatalf("after %d deferral(s) attempts = %d, want 0: no message reached the provider, so no rung may be spent", i+1, attempts)
		}
		now = now.Add(window)
	}
	if sender.calls != 0 {
		t.Fatalf("the sender was called %d time(s) while every dispatch was deferred", sender.calls)
	}

	// Past the configured maximum age it parks — and the reason names the
	// pacing, because that is what actually held the message back. A ladder
	// reason here would send an operator looking for a provider failure that
	// never happened.
	now = now.Add(maxAge)
	outcome, _, err := d.DispatchWithWait(e.ctx, id)
	if err != nil {
		t.Fatalf("dispatch past the maximum age: %v", err)
	}
	if outcome != OutcomeParked {
		t.Fatalf("past the %s maximum age the outcome was %v, want OutcomeParked", maxAge, outcome)
	}
	status, attempts, reason := e.deliveryRow(t, id)
	if status != StatusParked {
		t.Fatalf("status = %q, want parked", status)
	}
	if !strings.Contains(reason, "maximum age") || !strings.Contains(reason, "test_wait") {
		t.Fatalf("park reason %q names neither the age bound nor the rule that deferred it", reason)
	}
	if strings.Contains(reason, "ladder") {
		t.Fatalf("park reason %q blames the retry ladder for a delivery that never attempted transmission", reason)
	}
	// The row's own counter has to agree with that reason. It is not
	// necessarily zero — the dispatch that ended in the age park counted its
	// attempt on the way in, and a terminal row's counter is read by nothing
	// afterwards — but it must be nowhere near the ladder, or the two would
	// tell an operator different stories about why the message never went.
	if attempts >= ladder {
		t.Fatalf("attempts = %d against a ladder of %d on a delivery that never transmitted: the counter says the ladder ran out and the reason says it did not", attempts, ladder)
	}
}

// The other half of the same invariant: a dispatch that DOES reach the
// provider keeps its rung, so the exhaustion guard still parks a delivery
// whose transmissions are genuinely spent.
func TestATransmittingAttemptStillSpendsARung(t *testing.T) {
	e := setupStore(t)
	id := e.stage(t, e.baseInput(e.activity, "msg-transmits@example.com"))

	sender := &fakeSender{}
	d := NewDispatcher(e.store,
		fakeResolver{sender: sender, granted: []string{sendScope}}, stubConsent{},
		nil, func() time.Time { return e.clockValue }, time.Hour, 3)

	if outcome, _, err := d.DispatchWithWait(e.ctx, id); err != nil || outcome != OutcomeSent {
		t.Fatalf("dispatch → %v (%v), want OutcomeSent", outcome, err)
	}
	_, attempts, _ := e.deliveryRow(t, id)
	if attempts != 1 {
		t.Fatalf("attempts after one transmission = %d, want 1", attempts)
	}
	if sender.seen.Attempt != 0 {
		t.Fatalf("the first transmission reported Attempt = %d, want 0 — a first send must not run the prior-send lookup", sender.seen.Attempt)
	}
}

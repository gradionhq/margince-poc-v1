// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// What one attempt RECORDS, as opposed to what it decides: which policies a
// send is metered against and when, and the sentence a fault leaves on the
// delivery's operator-facing reason column. The verdicts themselves are in
// dispatcher_test.go and dispatcher_transmit_test.go, whose harness this rides.

// The limiter counts messages the provider actually received, so the
// dispatcher tells the chain only once transmission succeeds. Without this
// call the limiter never counts and the policy paces nothing.
func TestDispatchCountsASuccessfulSendAgainstEveryMeteringPolicy(t *testing.T) {
	meter := &recordingPolicy{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, &stubConsent{},
		waitPolicy{}, meter)

	if _, err := dispatch(context.Background(), d, store.delivery.ID); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if meter.recorded != 1 {
		t.Errorf("Recorded called %d times, want 1 — a limiter that counts checks paces nothing", meter.recorded)
	}
}

// A message that never reached the provider consumed none of the mailbox's
// quota. Counting a deferral would shrink the window every time the chain was
// merely consulted.
func TestDispatchCountsNothingAgainstAPolicyWhenTheSendIsPostponed(t *testing.T) {
	meter := &recordingPolicy{}
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{sender: &fakeSender{}, granted: []string{sendScope}}, &stubConsent{},
		waitPolicy{d: 90 * time.Second}, meter)

	got, _, err := d.DispatchWithWait(context.Background(), store.delivery.ID)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != OutcomePostponed {
		t.Fatalf("outcome = %v, want OutcomePostponed", got)
	}
	if meter.recorded != 0 {
		t.Errorf("Recorded called %d times for a message that never left", meter.recorded)
	}
}

// ONE message is metered once, and the durable record is what says so. A send
// whose receipt failed to record comes back on the ladder, where the
// connector's prior-send lookup answers from the transmission that already
// happened rather than transmitting again — so metering at the provider call
// would count that single message on both attempts and shrink the mailbox's
// window by a message nobody sent.
func TestDispatchMetersNothingWhenTheReceiptFailedToRecord(t *testing.T) {
	meter := &recordingPolicy{}
	store := &fakeStore{delivery: liveDelivery(), sentErr: errors.New("connection reset by peer")}
	d := newTestDispatcher(store, sendingResolver(), &stubConsent{}, meter)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeRetry || err == nil {
		t.Fatalf("outcome=%v err=%v, want OutcomeRetry and the cause", got, err)
	}
	if meter.recorded != 0 {
		t.Errorf("Recorded called %d times; the retry meters the same message again", meter.recorded)
	}
}

// A newer attempt that already closed the row leaves this one with nothing to
// meter: the attempt that recorded the send is the one that counted it.
func TestDispatchMetersNothingWhenANewerAttemptAlreadyClosedTheRow(t *testing.T) {
	meter := &recordingPolicy{}
	store := &fakeStore{delivery: liveDelivery(), sentErr: ErrTerminal}
	d := newTestDispatcher(store, sendingResolver(), &stubConsent{}, meter)

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if err != nil || got != OutcomeSkipped {
		t.Fatalf("outcome=%v err=%v, want OutcomeSkipped", got, err)
	}
	if meter.recorded != 0 {
		t.Errorf("Recorded called %d times for a delivery this attempt did not close", meter.recorded)
	}
}

// The reason column is durable and operator-facing, and the causes reaching it
// are arbitrary infrastructure errors. A wrapped database error names the
// SQLSTATE, the constraint and the table; none of that belongs on a row read
// alongside the message. The FULL cause still has to reach the caller, because
// the job log is where an operator diagnoses the run.
func TestDispatchRecordsAVettedSentenceRatherThanTheFaultsOwnText(t *testing.T) {
	leak := errors.New(`ERROR: relation "capture_connection" does not exist (SQLSTATE 42P01) ` +
		`violating constraint "capture_connection_pkey"`)
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store, fakeResolver{err: leak}, &stubConsent{})

	got, err := dispatch(context.Background(), d, store.delivery.ID)
	if got != OutcomeRetry {
		t.Fatalf("outcome = %v, want OutcomeRetry", got)
	}
	for _, internal := range []string{"SQLSTATE", "constraint", "capture_connection", "relation"} {
		if strings.Contains(store.failed, internal) {
			t.Errorf("reason %q leaks %q — the row an operator reads must not carry database internals", store.failed, internal)
		}
	}
	if !strings.HasPrefix(store.failed, faultPrefix) {
		t.Errorf("reason %q is not labelled as a transient fault", store.failed)
	}
	if err == nil || !errors.Is(err, leak) {
		t.Errorf("returned error = %v; the full cause must still reach the job log", err)
	}
}

// A cause that IS part of the shared connector vocabulary is named on the row,
// because that sentence is this system's own words about a fault an operator
// can act on — the redaction above exists to keep foreign text out, not to make
// every fault anonymous.
func TestDispatchNamesAFaultThatBelongsToTheConnectorVocabulary(t *testing.T) {
	store := &fakeStore{delivery: liveDelivery()}
	d := newTestDispatcher(store,
		fakeResolver{sender: &fakeSender{err: connector.ErrUnreachable}, granted: []string{sendScope}}, &stubConsent{})

	if got, _ := dispatch(context.Background(), d, store.delivery.ID); got != OutcomeRetry {
		t.Fatalf("outcome = %v, want OutcomeRetry", got)
	}
	if !strings.Contains(store.failed, "could not be reached") {
		t.Errorf("reason = %q, want the provider-unreachable sentence", store.failed)
	}
}

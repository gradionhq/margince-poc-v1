// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The outbound-send worker's branch table: one dispatch verdict in, one River
// disposition out. The dispatcher itself is proven in internal/modules/comms;
// what can only break HERE is the translation — a postponement returned as an
// error burns a rung of the ladder the dispatcher's exhaustion guard is
// counting, and a retry returned as nil completes a job whose delivery is
// still pending, so nothing ever transmits it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// stubDispatcher answers one canned verdict and records what the worker asked
// it about — the queue boundary the worker sits on, mocked; nothing else is.
type stubDispatcher struct {
	outcome   comms.Outcome
	wait      time.Duration
	err       error
	calls     int
	gotID     ids.UUID
	gotWS     ids.UUID
	sawWSBind bool
}

func (s *stubDispatcher) DispatchWithWait(ctx context.Context, id ids.UUID) (comms.Outcome, time.Duration, error) {
	s.calls++
	s.gotID = id
	s.gotWS, s.sawWSBind = principal.WorkspaceID(ctx)
	return s.outcome, s.wait, s.err
}

// sendJob builds one job for the worker with a fresh workspace/delivery pair.
func sendJob(ws, delivery ids.UUID) *river.Job[SendEmailArgs] {
	return &river.Job[SendEmailArgs]{
		Args: SendEmailArgs{Workspace: ws.String(), DeliveryID: delivery.String()},
	}
}

// River persists Kind in river_job, so changing it orphans every queued row:
// the old rows name a worker nothing registers any more and sit forever.
func TestSendEmailArgsKindIsStable(t *testing.T) {
	if got := (SendEmailArgs{}).Kind(); got != "comms_send_email" {
		t.Fatalf("SendEmailArgs.Kind() = %q, want %q — a changed kind orphans every queued send", got, "comms_send_email")
	}
}

// A postponement must reschedule, never fail. river.JobSnooze restores the
// attempt; returning an error would spend it, and on the last rung that leaves
// a delivery pending with nothing left to deliver it.
func TestSendEmailWorkerSnoozesOnAPostponedOutcome(t *testing.T) {
	dispatcher := &stubDispatcher{outcome: comms.OutcomePostponed, wait: 90 * time.Second}
	worker := &commsSendWorker{dispatcher: dispatcher}

	err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7()))

	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("Work on a postponed outcome = %v, want a river.JobSnoozeError", err)
	}
	if snooze.Duration != 90*time.Second {
		t.Fatalf("snoozed for %s, want the interval the policy asked for (90s)", snooze.Duration)
	}
}

// A policy that asks to wait for no time at all would re-run the job the
// instant it is rescheduled — a hot loop against the provider. The worker
// floors the interval instead.
func TestSendEmailWorkerFloorsAZeroPostponement(t *testing.T) {
	dispatcher := &stubDispatcher{outcome: comms.OutcomePostponed}
	worker := &commsSendWorker{dispatcher: dispatcher}

	err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7()))

	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("Work on a zero-wait postponement = %v, want a river.JobSnoozeError", err)
	}
	if snooze.Duration < minSendSnooze {
		t.Fatalf("snoozed for %s, want at least the %s floor — a zero snooze is a hot loop", snooze.Duration, minSendSnooze)
	}
}

// Sent, parked and skipped are all finished: there is nothing left for River's
// ladder to do, and returning an error would retry a delivery that is closed.
func TestSendEmailWorkerReturnsNilOnATerminalOutcome(t *testing.T) {
	for _, outcome := range []comms.Outcome{comms.OutcomeSent, comms.OutcomeParked, comms.OutcomeSkipped} {
		t.Run(string(outcome), func(t *testing.T) {
			worker := &commsSendWorker{dispatcher: &stubDispatcher{outcome: outcome}}
			if err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7())); err != nil {
				t.Fatalf("Work on a %s outcome = %v, want nil", outcome, err)
			}
		})
	}
}

// A retry is a fault, not a verdict: the cause has to reach River or the job
// completes while the delivery is still pending and nothing transmits it.
func TestSendEmailWorkerReturnsTheErrorOnRetry(t *testing.T) {
	cause := errors.New("the provider is unreachable")
	worker := &commsSendWorker{dispatcher: &stubDispatcher{outcome: comms.OutcomeRetry, err: cause}}

	err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7()))
	if !errors.Is(err, cause) {
		t.Fatalf("Work on a retry outcome = %v, want the dispatcher's cause", err)
	}
}

// A retry with no cause would complete the job silently — the same pending-row
// leak, arrived at from the other side. It fails loudly instead.
func TestSendEmailWorkerFailsARetryThatCarriesNoCause(t *testing.T) {
	worker := &commsSendWorker{dispatcher: &stubDispatcher{outcome: comms.OutcomeRetry}}

	if err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7())); err == nil {
		t.Fatal("Work on a causeless retry returned nil — the job completed with the delivery still pending")
	}
}

// comms_outbound is RLS-scoped, so every read and transition the dispatcher
// makes needs the job's own workspace on the context; without it the load
// finds nothing and the send silently never happens.
func TestSendEmailWorkerBindsTheJobsWorkspaceAndDelivery(t *testing.T) {
	ws, delivery := ids.NewV7(), ids.NewV7()
	dispatcher := &stubDispatcher{outcome: comms.OutcomeSent}
	worker := &commsSendWorker{dispatcher: dispatcher}

	if err := worker.Work(context.Background(), sendJob(ws, delivery)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !dispatcher.sawWSBind || dispatcher.gotWS != ws {
		t.Fatalf("dispatch ran under workspace %v (bound=%v), want %v", dispatcher.gotWS, dispatcher.sawWSBind, ws)
	}
	if dispatcher.gotID != delivery {
		t.Fatalf("dispatched delivery %v, want %v", dispatcher.gotID, delivery)
	}
}

// An unparseable argument names no delivery, so there is nothing to dispatch:
// the job must fail rather than dispatch a zero id, which would read as "some
// other delivery" to a row-scoped query.
func TestSendEmailWorkerRefusesAMalformedJobArgument(t *testing.T) {
	cases := map[string]SendEmailArgs{
		"workspace": {Workspace: "not-a-uuid", DeliveryID: ids.NewV7().String()},
		"delivery":  {Workspace: ids.NewV7().String(), DeliveryID: "not-a-uuid"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			dispatcher := &stubDispatcher{outcome: comms.OutcomeSent}
			worker := &commsSendWorker{dispatcher: dispatcher}
			if err := worker.Work(context.Background(), &river.Job[SendEmailArgs]{Args: args}); err == nil {
				t.Fatal("Work accepted a malformed job argument")
			}
			if dispatcher.calls != 0 {
				t.Fatalf("dispatcher was called %d time(s) for a malformed job", dispatcher.calls)
			}
		})
	}
}

// The dispatcher parks on the last rung of the ladder so a row the runner will
// never deliver again cannot look pending forever. That is only true if it is
// counting the SAME ladder River is running: the enqueue and the dispatcher's
// bound come from one constant, and this holds the enqueue half to it.
func TestSendEmailJobLadderMatchesTheDispatchersBound(t *testing.T) {
	if got := sendInsertOpts().MaxAttempts; got != sendMaxAttempts {
		t.Fatalf("enqueued MaxAttempts = %d, want %d — River's ladder and the dispatcher's exhaustion guard must be the same number", got, sendMaxAttempts)
	}
	if sendMaxAttempts <= 0 {
		t.Fatalf("sendMaxAttempts = %d; a non-positive bound silently falls back to the dispatcher's own default", sendMaxAttempts)
	}
}

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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
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
	// The identity-reconcile inside RecordSent writes an audit row and an
	// outbox event, so the scope the worker dispatches under has to carry an
	// actor and a correlation id as well as the workspace.
	gotActor         principal.Principal
	sawActor         bool
	sawCorrelationID bool
}

func (s *stubDispatcher) DispatchWithWait(ctx context.Context, id ids.UUID) (comms.Outcome, time.Duration, error) {
	s.calls++
	s.gotID = id
	s.gotWS, s.sawWSBind = principal.WorkspaceID(ctx)
	s.gotActor, s.sawActor = principal.Actor(ctx)
	_, s.sawCorrelationID = principal.CorrelationID(ctx)
	return s.outcome, s.wait, s.err
}

// sendJob builds one job for the worker with a fresh workspace/delivery pair.
func sendJob(ws, delivery ids.UUID) *river.Job[SendEmailArgs] {
	return &river.Job[SendEmailArgs]{
		Args: SendEmailArgs{Workspace: ws, DeliveryID: delivery.String()},
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

// A terminal outcome carrying an error is a broken dispatcher contract. Every
// terminal path returns nil today, which is exactly why this must not be
// allowed to pass silently: the branch that reports "nothing left to do" would
// swallow the evidence that something changed underneath it.
func TestSendEmailWorkerSurfacesAnErrorOnATerminalOutcome(t *testing.T) {
	cause := errors.New("a terminal path grew an error")
	worker := &commsSendWorker{dispatcher: &stubDispatcher{outcome: comms.OutcomeSent, err: cause}}

	err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7()))
	if !errors.Is(err, cause) {
		t.Fatalf("Work on a terminal outcome carrying an error = %v, want the error surfaced", err)
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

// The workspace alone is not enough. Recording a receipt re-keys the message
// onto the identity the provider stamped, and that write needs an actor for its
// audit row and a correlation id for its outbox event — without both the
// re-key fails and the message stays filed under an identity no reply will
// quote, silently. The actor is the SYSTEM completing a send a human already
// authorized: running it under the sender's seat would let a seat revoked
// between staging and transmit strand the message's identity.
func TestSendEmailWorkerDispatchesUnderASystemActorAndACorrelationID(t *testing.T) {
	dispatcher := &stubDispatcher{outcome: comms.OutcomeSent}
	worker := &commsSendWorker{dispatcher: dispatcher}

	if err := worker.Work(context.Background(), sendJob(ids.NewV7(), ids.NewV7())); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if !dispatcher.sawActor {
		t.Fatal("dispatch ran with no actor bound; the identity reconcile writes no audit row without one")
	}
	if dispatcher.gotActor.Type != principal.PrincipalSystem {
		t.Errorf("actor type = %q, want %q — the worker completes a send, it does not act as the sender",
			dispatcher.gotActor.Type, principal.PrincipalSystem)
	}
	if dispatcher.gotActor.ID != "system:comms-send" {
		t.Errorf("actor id = %q, want system:comms-send", dispatcher.gotActor.ID)
	}
	if !dispatcher.sawCorrelationID {
		t.Error("dispatch ran with no correlation id bound; the outbox event is refused without one")
	}
}

// An argument that names no delivery has nothing to dispatch, and one that
// names no workspace has nowhere to dispatch it: the job must fail rather than
// run against a zero id, which would read as "some other delivery" to a
// row-scoped query — or, for the workspace, as an unbound GUC failing later
// and less legibly.
func TestSendEmailWorkerRefusesAJobArgumentThatNamesNothing(t *testing.T) {
	cases := map[string]SendEmailArgs{
		"workspace": {DeliveryID: ids.NewV7().String()},
		"delivery":  {Workspace: ids.NewV7(), DeliveryID: "not-a-uuid"},
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
// never deliver again cannot look pending forever. It can only know which rung
// that is by being told River's own number, and it is: newSendWorker reads the
// bound off sendInsertOpts(), so the two are one expression and cannot be
// changed apart.
//
// What is left to check is that the one declaration is USABLE — a non-positive
// bound would be silently swapped for the dispatcher's internal default, which
// is not the ladder River is running. This test guards that, and the enqueue
// side; the dispatcher side needs no test because there is nothing there to
// diverge from.
func TestSendEmailJobDeclaresAUsableLadder(t *testing.T) {
	got := sendInsertOpts().MaxAttempts
	if got != sendMaxAttempts {
		t.Fatalf("enqueued MaxAttempts = %d, want the declared ladder %d", got, sendMaxAttempts)
	}
	if got <= 0 {
		t.Fatalf("the declared ladder is %d; a non-positive bound falls back to the dispatcher's own default, which is not River's", got)
	}
}

// stubSenders and stubGrants are the capture boundary — a database — and the
// only thing faked in the two suites below.
type stubSenders struct {
	sender  connector.EmailSender
	granted []string
	err     error
}

func (s stubSenders) SenderFor(context.Context, ids.UserID, string) (connector.EmailSender, connector.Auth, []string, error) {
	return s.sender, connector.Auth("cred"), s.granted, s.err
}

// This is the branch that permanently destroys mail if it is read wrong: the
// dispatcher PARKS on the two sentinels and RETRIES on everything else, so a
// transient fault translated into a sentinel kills a legitimate send that
// nothing was wrong with.
func TestCommsResolverTranslatesOnlyTheTwoParkingSentinels(t *testing.T) {
	transient := errors.New("keyvault timed out")
	for _, tc := range []struct {
		name string
		from error
		want error
	}{
		{"no connection parks", capture.ErrNoConnection, comms.ErrNoMailbox},
		{"a capture-only connector parks", capture.ErrConnectorCannotSend, comms.ErrCannotSend},
		{"a transient fault passes through unchanged", transient, transient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := commsResolver{registry: stubSenders{err: tc.from}}

			_, _, _, err := r.Resolve(context.Background(), ids.New[ids.UserKind](), "gmail")

			if !errors.Is(err, tc.want) {
				t.Fatalf("Resolve on %v → %v, want it to match %v", tc.from, err, tc.want)
			}
			// The cause survives translation, so an operator reading the log
			// still learns what capture actually said.
			if !errors.Is(err, tc.from) {
				t.Fatalf("Resolve dropped the underlying cause %v from %v", tc.from, err)
			}
			// A transient fault must NOT arrive wearing a parking sentinel.
			if tc.want == transient &&
				(errors.Is(err, comms.ErrNoMailbox) || errors.Is(err, comms.ErrCannotSend)) {
				t.Fatalf("a transient fault was translated into a parking sentinel: %v", err)
			}
		})
	}
}

// A resolved mailbox passes through whole: the seam, the credential and the
// grant the dispatcher's authority gate reads.
func TestCommsResolverPassesAResolvedMailboxThrough(t *testing.T) {
	want := []string{"https://www.googleapis.com/auth/gmail.send"}
	r := commsResolver{registry: stubSenders{sender: stubSender{}, granted: want}}

	sender, auth, granted, err := r.Resolve(context.Background(), ids.New[ids.UserKind](), "gmail")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sender == nil || string(auth) != "cred" || !slices.Equal(granted, want) {
		t.Fatalf("Resolve = %v/%q/%v, want the resolved mailbox unchanged", sender, auth, granted)
	}
}

type stubSender struct{}

func (stubSender) SendEmail(context.Context, connector.Auth, connector.EmailMessage) (connector.SendReceipt, error) {
	return connector.SendReceipt{}, nil
}

// stubSeatType is identity's authority seam, faked so the transmit-time seat
// translation is proven without a database.
type stubSeatType struct {
	seat principal.SeatType
	err  error
}

func (s stubSeatType) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return s.seat, s.err
}

// commsSeats.ActiveSeat is the transmit-time licensing gate: a read seat is a
// live row, not a permitted sender, and the two must not collapse into one
// answer. If this test is reverted to asking only "did the lookup return a
// row" (dropping the CanMutate check), a live read seat comes back active and
// this fails.
func TestCommsSeatsActiveSeatChecksMutationCapabilityNotMereRowExistence(t *testing.T) {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	lookupDown := errors.New("identity store timeout")

	for _, tc := range []struct {
		name       string
		authority  stubSeatType
		wantActive bool
		wantReason string
		wantErr    error
	}{
		{"a full seat may send", stubSeatType{seat: principal.SeatFull}, true, "", nil},
		{
			"a live read seat parks, not sends — a row existing is not the same fact as a permitted sender",
			stubSeatType{seat: principal.SeatRead},
			false, "read-only seat", nil,
		},
		{
			"a missing row parks as deactivated, not as read-only",
			stubSeatType{err: apperrors.ErrNotFound},
			false, "no longer active", nil,
		},
		{
			"a lookup that cannot answer must not answer — an outage is not a licensing decision",
			stubSeatType{err: lookupDown},
			false, "", lookupDown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := commsSeats{authority: tc.authority}

			active, reason, err := s.ActiveSeat(ctx, ids.New[ids.UserKind]())

			if active != tc.wantActive {
				t.Fatalf("ActiveSeat active = %v, want %v", active, tc.wantActive)
			}
			if active && reason != "" {
				t.Fatalf("ActiveSeat reason = %q, want empty for a permitted sender", reason)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("ActiveSeat reason = %q, want it to mention %q so an operator can tell the two park causes apart", reason, tc.wantReason)
			}
			switch {
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("ActiveSeat error = %v, want it to match %v", err, tc.wantErr)
			case tc.wantErr == nil && err != nil:
				t.Fatalf("ActiveSeat error = %v, want nil", err)
			}
		})
	}
}

// ActiveSeat must not be asked outside a bound workspace: it would otherwise
// read identity's authority against no tenant at all, which the RLS GUC
// contract has no answer for.
func TestCommsSeatsActiveSeatRefusesWithNoWorkspaceBound(t *testing.T) {
	s := commsSeats{authority: stubSeatType{seat: principal.SeatFull}}

	active, _, err := s.ActiveSeat(context.Background(), ids.New[ids.UserKind]())

	if err == nil {
		t.Fatal("ActiveSeat with no workspace on the context returned nil error")
	}
	if active {
		t.Fatal("ActiveSeat reported an active seat with no workspace bound to check it against")
	}
}

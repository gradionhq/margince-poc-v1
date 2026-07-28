// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The outbound-send composition's moving half: the durable job an accepted
// send is staged with, the River worker that drives one delivery attempt, and
// the two seams the comms module deliberately does not reach across — the
// capture registry that resolves a user's mailbox, and the consent store that
// answers for its recipients. comms stays River-agnostic and sibling-free;
// every edge is injected here.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// SendEmailArgs transmits ONE staged delivery. The workspace travels with it
// because comms_outbound is RLS-scoped and a job carries no session: the
// worker binds this workspace before the dispatcher reads anything.
type SendEmailArgs struct {
	Workspace  string `json:"workspace"`
	DeliveryID string `json:"delivery_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (SendEmailArgs) Kind() string { return "comms_send_email" }

// sendMaxAttempts is the retry ladder for one delivery, and it is ONE number
// on purpose: it is both the MaxAttempts River enqueues the job with and the
// bound the dispatcher's exhaustion guard parks on. If they disagreed the
// delivery either parks while River still has rungs left, or outlives its job
// and sits pending forever with nothing to deliver it.
//
// Ten rather than River's default of 25: on the default backoff (attempt⁴
// seconds) ten attempts span roughly five hours, which is long enough to ride
// out a provider outage and short enough that a message nobody can send stops
// being "on its way" the same day.
const sendMaxAttempts = 10

// minSendSnooze floors a postponement. A policy that asks to wait for no time
// at all would have River redeliver the job immediately, which is a hot loop
// against the very provider the policy is pacing us for.
const minSendSnooze = time.Second

// sendTimeout overrides River's one-minute default: one attempt unseals a
// credential from the vault, refreshes an OAuth token, may run the connector's
// prior-send lookup, and then transmits — four network round trips, each of
// which can be slow before it is wrong.
const sendTimeout = 5 * time.Minute

// sendInsertOpts is the enqueue policy for one delivery. No uniqueness: the
// delivery row is minted per send and the job names it, so there is nothing to
// deduplicate against — and a unique-by-args window would silently drop the
// second of two legitimate sends staged in the same instant.
func sendInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{MaxAttempts: sendMaxAttempts}
}

// deliveryDispatcher is the one attempt the worker drives. It exists so the
// verdict→disposition table below can be proven without a database, mirroring
// the private deliveryStore seam comms uses for the same reason;
// *comms.Dispatcher is the only implementation the product ships.
type deliveryDispatcher interface {
	DispatchWithWait(ctx context.Context, id ids.UUID) (comms.Outcome, time.Duration, error)
}

var _ deliveryDispatcher = (*comms.Dispatcher)(nil)

// commsSendWorker translates one dispatch verdict into a River disposition.
// It decides nothing itself: the dispatcher owns the gates, the policies, and
// the row's state, and this is the adapter that keeps River out of comms.
type commsSendWorker struct {
	river.WorkerDefaults[SendEmailArgs]
	dispatcher deliveryDispatcher
}

// Timeout gives one transmission room to finish over a live provider.
func (w *commsSendWorker) Timeout(*river.Job[SendEmailArgs]) time.Duration { return sendTimeout }

func (w *commsSendWorker) Work(ctx context.Context, job *river.Job[SendEmailArgs]) error {
	ws, err := ids.Parse(job.Args.Workspace)
	if err != nil {
		return fmt.Errorf("comms_send_email: workspace id: %w", err)
	}
	deliveryID, err := ids.Parse(job.Args.DeliveryID)
	if err != nil {
		return fmt.Errorf("comms_send_email: delivery id: %w", err)
	}

	outcome, wait, err := w.dispatcher.DispatchWithWait(principal.WithWorkspaceID(ctx, ws), deliveryID)
	switch outcome {
	case comms.OutcomePostponed:
		// A SNOOZE, never a returned error. River restores the attempt on a
		// snooze and spends it on an error; the dispatcher checks exhaustion
		// AFTER the policy chain, so on the last rung a deferral is returned
		// where a park would otherwise be. Spending that rung would leave the
		// row pending with nothing left to deliver it — exactly the state the
		// exhaustion guard exists to prevent.
		return river.JobSnooze(max(wait, minSendSnooze))
	case comms.OutcomeRetry:
		if err == nil {
			return fmt.Errorf("comms_send_email: delivery %s asked to be retried with no cause; River's ladder has nothing to back off on", job.Args.DeliveryID)
		}
		return err
	case comms.OutcomeSent, comms.OutcomeParked, comms.OutcomeSkipped:
		// Finished, each in its own way: the row records which, and there is
		// nothing left for the ladder to do.
		return nil
	default:
		return fmt.Errorf("comms_send_email: delivery %s reported unknown outcome %q", job.Args.DeliveryID, outcome)
	}
}

// commsResolver resolves the transmitting mailbox over the capture registry —
// the cross-module edge comms must not hold itself.
//
// The translation is the whole point of this type, and it is deliberately
// narrow. Only two capture answers are FACTS about the deployment; everything
// else is a failure to get an answer, and turning one of those into a parking
// sentinel would permanently destroy legitimate mail that nothing is wrong
// with.
type commsResolver struct{ registry *capture.Registry }

var _ comms.ConnectionResolver = commsResolver{}

//nolint:ireturn // implements comms.ConnectionResolver, whose contract returns the optional connector.Sender seam
func (r commsResolver) Resolve(ctx context.Context, userID ids.UserID, provider string) (connector.Sender, connector.Auth, []string, error) {
	sender, auth, granted, err := r.registry.SenderFor(ctx, userID, provider)
	switch {
	case errors.Is(err, capture.ErrNoConnection):
		return nil, nil, nil, fmt.Errorf("%w: %w", comms.ErrNoMailbox, err)
	case errors.Is(err, capture.ErrConnectorCannotSend):
		return nil, nil, nil, fmt.Errorf("%w: %w", comms.ErrCannotSend, err)
	case err != nil:
		// Unchanged, and therefore transient: a vault blip, a database
		// timeout, or a connector this role did not register are all reasons
		// the question could not be answered, not answers.
		return nil, nil, nil, err
	}
	return sender, auth, granted, nil
}

// mailboxAuthority answers the request-time pre-flight over the SAME registry
// the connect flow writes to, so what the user just connected is what the
// check reads.
//
// It asks about the GRANT, not the connection. Every mailbox connected before
// the send scope existed holds read-only access until its owner reconnects, so
// a check that only asked "is something connected?" would pass all of them and
// then park every send.
type mailboxAuthority struct {
	registry *capture.Registry
	provider string
}

var _ activities.MailboxAuthority = mailboxAuthority{}

func (m mailboxAuthority) SendCapable(ctx context.Context) (bool, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		// Sending is a human act (comms.Store.StageTx enforces the same
		// rule), so a principal with no app_user identity has no mailbox to
		// pre-flight and is told so here rather than at transmission.
		return false, nil
	}
	scope, sends := comms.SendScopeFor(m.provider)
	if !sends {
		return false, nil
	}
	_, _, granted, err := m.registry.SenderFor(ctx, ids.From[ids.UserKind](actor.UserID), m.provider)
	switch {
	case errors.Is(err, capture.ErrNoConnection), errors.Is(err, capture.ErrConnectorCannotSend):
		return false, nil
	case err != nil:
		// A pre-flight that cannot ask must not answer. Reporting the fault
		// refuses the send loudly instead of asserting a grant nobody read.
		return false, err
	}
	return slices.Contains(granted, scope), nil
}

// commsStager records an accepted send for transmission: the delivery row and
// the job that will carry it, both on the caller's transaction. One commit, one
// fact — a crash between them would either promise a send nothing queued or
// queue one with no timeline entry behind it.
type commsStager struct {
	store  *comms.Store
	runner *jobs.Runner
}

var _ activities.DeliveryStager = commsStager{}

// NewDeliveryStager builds the delivery machinery every send transport is
// composed with (compose.WithDelivery). The runner is insert-only in the api
// role; the worker role works what it inserts.
//
//nolint:ireturn // returns the activities.DeliveryStager seam by design: the concrete type is unexported and every caller holds the interface
func NewDeliveryStager(pool *pgxpool.Pool, runner *jobs.Runner) activities.DeliveryStager {
	return commsStager{store: comms.NewStore(pool, time.Now), runner: runner}
}

func (s commsStager) StageTx(ctx context.Context, tx pgx.Tx, in activities.DeliveryRequest) error {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("comms: staging a delivery outside workspace context")
	}
	id, err := s.store.StageTx(ctx, tx, comms.StageInput{
		ActivityID:      in.ActivityID,
		Provider:        in.Provider,
		MessageID:       in.MessageID,
		Recipients:      in.Recipients,
		Cc:              in.Cc,
		Subject:         in.Subject,
		Body:            in.Body,
		ConsentPurpose:  in.ConsentPurpose,
		InReplyTo:       in.InReplyTo,
		References:      in.References,
		ThreadKey:       in.ThreadKey,
		ListUnsubscribe: in.ListUnsubscribe,
	})
	if err != nil {
		return err
	}
	return s.runner.EnqueueTx(ctx, tx, SendEmailArgs{
		Workspace: ws.String(), DeliveryID: id.String(),
	}, sendInsertOpts())
}

// SendPacing is the deployment's outbound pacing: how many messages one
// mailbox may transmit per window, and how long a delivery may be deferred
// before it parks with a reason instead of being deferred silently forever.
// The zero value takes the defaults below.
type SendPacing struct {
	Limit  int
	Window time.Duration
	MaxAge time.Duration
}

// The pacing defaults. The rate is a BURST bound, not a quota: Gmail enforces
// its own per-user daily cap and throttles an account that bursts past it, so
// this exists to keep a legitimate run of sends from costing a user their
// mailbox's standing. The age bound is a day — past that a message nobody
// could send has stopped being news, and an operator should see why.
const (
	defaultSendRateLimit  = 30
	defaultSendRateWindow = time.Minute
	defaultSendMaxAge     = 24 * time.Hour
)

// withDefaults fills the unset knobs. A zero is read as "not configured", never
// as "no sends allowed" or "defer forever" — a forgotten flag must degrade to
// the conservative rule, not to the absence of it.
func (p SendPacing) withDefaults() SendPacing {
	if p.Limit <= 0 {
		p.Limit = defaultSendRateLimit
	}
	if p.Window <= 0 {
		p.Window = defaultSendRateWindow
	}
	if p.MaxAge <= 0 {
		p.MaxAge = defaultSendMaxAge
	}
	return p
}

// newSendWorker assembles the dispatcher the worker role drives: the delivery
// store, the mailbox resolver over the capture registry, the consent gate, and
// the policy chain. Every one of those edges crosses a module boundary, which
// is why the assembly lives here and not in comms.
func newSendWorker(pool *pgxpool.Pool, registry *capture.Registry, pacing SendPacing) *commsSendWorker {
	p := pacing.withDefaults()
	return &commsSendWorker{dispatcher: comms.NewDispatcher(
		comms.NewStore(pool, time.Now),
		commsResolver{registry: registry},
		consent.NewGate(consent.NewStore(pool)),
		[]comms.SendPolicy{comms.NewMailboxRatePolicy(p.Limit, p.Window, time.Now)},
		time.Now,
		p.MaxAge,
		// The SAME ladder length River enqueues with (sendInsertOpts): the
		// dispatcher parks on the last rung, and it can only know which rung
		// that is by being told the runner's own number.
		sendMaxAttempts,
	)}
}

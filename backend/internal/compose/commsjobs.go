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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// SendEmailArgs transmits ONE staged delivery. The workspace travels with it
// because comms_outbound is RLS-scoped and a job carries no session: the
// worker binds this workspace before the dispatcher reads anything.
type SendEmailArgs struct {
	Workspace  ids.UUID `json:"workspace_id"`
	DeliveryID string   `json:"delivery_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (SendEmailArgs) Kind() string { return "comms_send_email" }

// WorkspaceID binds this delivery to its tenant (jobs.WorkspaceScoped).
func (a SendEmailArgs) WorkspaceID() ids.UUID { return a.Workspace }

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
//
// Ten RUNGS, nine transmissions: Load counts the attempt before the dispatcher
// reaches its exhaustion guard, so the tenth dispatch arrives with Attempts
// already at ten, meets `Attempts >= maxAttempts`, and parks without asking the
// provider. The number names the ladder River is given, not a send count.
const sendMaxAttempts = 10

// minSendSnooze floors a postponement. A policy that asks to wait for no time
// at all would have River redeliver the job immediately, which is a hot loop
// against the very provider the policy is pacing us for.
const minSendSnooze = time.Second

// sendInsertOpts is the enqueue policy for one delivery, and the ONE place the
// ladder length is declared — the dispatcher's exhaustion guard reads its bound
// from here (newSendWorker) rather than from a second copy of the constant.
//
// No uniqueness: the delivery row is minted per send and the job names it, so
// there is nothing to deduplicate against — and a unique-by-args window would
// silently drop the second of two legitimate sends staged in the same instant.
//
// No queue of its own either, unlike the deep-read and rate-refresh lanes. The
// criterion those two were split out on is job LENGTH — a multi-minute crawl
// evicting short maintenance work from the default pool. A send is the short
// kind: one bounded network round trip, and a paced one snoozes rather than
// holding its slot. It belongs with the default queue's other jobs, not with
// the long ones.
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
	dispatcher deliveryDispatcher
}

// SendWorkerContext is the scope one dispatch attempt runs under. RecordSent's
// identity reconcile writes an audit row and an outbox event, and storekit
// demands an actor for the first and an actor AND a correlation id for the
// second — the workspace alone is not enough, and a reconcile that cannot
// audit itself leaves the operator no record that this mailbox rewrites
// message identities at all.
//
// It is the SYSTEM completing a send a human already authorized, not that
// human acting again: running the completion under the sender's seat would let
// a seat revoked between staging and transmit strand the message's identity,
// which is a governance rule applied where no governance decision is being
// made.
//
// Exported for the reason NewSendSeatAuthority is: the scope is assembled here,
// and a suite that rebuilt it from its own parts would be driving a dispatch
// the product does not ship — which is exactly how a binding this path depends
// on goes missing without any test noticing.
func SendWorkerContext(ctx context.Context, workspaceID ids.UUID) context.Context {
	return sendWorkerScope(principal.WithWorkspaceID(ctx, workspaceID))
}

// sendWorkerScope is SendWorkerContext's provenance half, over a context whose
// workspace is already bound. The worker takes this path because its binding
// comes from the job args' own role declaration, which refuses a zero id — a
// guarantee re-binding here would quietly discard.
func sendWorkerScope(wsCtx context.Context) context.Context {
	wsCtx = principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:comms-send",
	})
	return principal.WithCorrelationID(wsCtx, ids.NewV7())
}

func (w *commsSendWorker) Work(ctx context.Context, job *river.Job[SendEmailArgs]) error {
	deliveryID, err := ids.Parse(job.Args.DeliveryID)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("comms_send_email: delivery id: %w", err))
	}

	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}

	outcome, wait, err := w.dispatcher.DispatchWithWait(sendWorkerScope(wsCtx), deliveryID)
	switch outcome {
	case comms.OutcomePostponed:
		// A SNOOZE, never a returned error. River restores the attempt on a
		// snooze and spends it on an error; the dispatcher checks exhaustion
		// AFTER the policy chain, so on the last rung a deferral is returned
		// where a park would otherwise be. Spending that rung would leave the
		// row pending with nothing left to deliver it — exactly the state the
		// exhaustion guard exists to prevent.
		//
		// A postponement carries no error either. That is a guard on the
		// deliveryDispatcher SEAM, not on a state *comms.Dispatcher can reach:
		// its postpone returns OutcomePostponed only alongside a nil error. The
		// seam is what this worker is written against, so an implementation
		// that returned both must not have its error snoozed away — a fault
		// buried under a delay nobody reads as a failure is the one shape this
		// translation may never take.
		if err != nil {
			return jobs.FaultContext(ctx, fmt.Errorf("comms_send_email: delivery %s postponed with an error: %w", job.Args.DeliveryID, err))
		}
		return river.JobSnooze(max(wait, minSendSnooze))
	case comms.OutcomeRetry:
		if err == nil {
			return jobs.FaultContext(ctx, fmt.Errorf("comms_send_email: delivery %s asked to be retried with no cause; River's ladder has nothing to back off on", job.Args.DeliveryID))
		}
		return jobs.FaultContext(ctx, err)
	case comms.OutcomeSent, comms.OutcomeParked, comms.OutcomeSkipped:
		// Finished, each in its own way: the row records which, and there is
		// nothing left for the ladder to do. A terminal outcome carrying an
		// error is a broken contract, not a retryable fault — surface it
		// rather than let it disappear because this branch returns nil.
		if err != nil {
			return jobs.FaultContext(ctx, fmt.Errorf("comms_send_email: delivery %s reported terminal outcome %q with an error: %w",
				job.Args.DeliveryID, outcome, err))
		}
		return nil
	default:
		// The cause travels WITH the unknown outcome. A dispatcher that
		// returned a zero Outcome alongside a real error lands here, and
		// reporting only "unknown outcome" would replace the one thing that
		// says what actually went wrong with a description of the symptom.
		if err != nil {
			return jobs.FaultContext(ctx, fmt.Errorf("comms_send_email: delivery %s reported unknown outcome %q: %w",
				job.Args.DeliveryID, outcome, err))
		}
		return jobs.FaultContext(ctx, fmt.Errorf("comms_send_email: delivery %s reported unknown outcome %q", job.Args.DeliveryID, outcome))
	}
}

// mailboxSenders is the capture lookup the dispatcher's resolver needs, and
// nothing more. Narrowed to the one method for the same reason the
// deliveryDispatcher seam above exists: the translation below is the branch
// whose mis-reading permanently destroys mail, so it must be provable without
// a database. *capture.Registry is the only implementation the product ships.
type mailboxSenders interface {
	SenderFor(ctx context.Context, userID ids.UserID, provider string) (connector.EmailSender, connector.Auth, []string, error)
}

var _ mailboxSenders = (*capture.Registry)(nil)

// commsResolver resolves the transmitting connection over the capture registry —
// the cross-module edge comms must not hold itself. Both halves land on the same
// *capture.Registry; they are two fields because they are two different lookups
// (see channelSenders in commschannel.go, which holds the channel half).
//
// The translation is the whole point of this type, and it is deliberately
// narrow. Only three capture answers are FACTS about the deployment — no
// connection, a capture-only connector, and a provider this role has no
// integration for; everything else is a failure to get an answer, and turning
// one of those into a parking sentinel would permanently destroy legitimate
// mail that nothing is wrong with.
type commsResolver struct {
	registry mailboxSenders
	channels channelSenders
}

var _ comms.ConnectionResolver = commsResolver{}

//nolint:ireturn // implements comms.ConnectionResolver, whose contract returns the optional connector.EmailSender seam
func (r commsResolver) Resolve(ctx context.Context, userID ids.UserID, provider string) (connector.EmailSender, connector.Auth, []string, error) {
	sender, auth, granted, err := r.registry.SenderFor(ctx, userID, provider)
	switch {
	case errors.Is(err, capture.ErrNoConnection):
		return nil, nil, nil, fmt.Errorf("%w: %w", comms.ErrNoMailbox, err)
	case errors.Is(err, capture.ErrConnectorCannotSend):
		return nil, nil, nil, fmt.Errorf("%w: %w", comms.ErrCannotSend, err)
	case errors.Is(err, capture.ErrConnectorNotConfigured):
		return nil, nil, nil, fmt.Errorf("%w: %w", comms.ErrProviderNotConfigured, err)
	case err != nil:
		// Unchanged, and therefore transient: a vault blip, a database
		// timeout, or a connector this role did not register are all reasons
		// the question could not be answered, not answers.
		return nil, nil, nil, err
	}
	return sender, auth, granted, nil
}

// seatResolver is the live-seat half of the authority the dispatcher re-reads
// at transmit time — the shared authz seam identity implements, narrowed here
// to the one question comms asks. *identity.Service is the only implementation
// the product ships.
type seatResolver interface {
	SeatType(ctx context.Context, workspaceID, humanID ids.UUID) (principal.SeatType, error)
}

var _ seatResolver = (*identity.Service)(nil)

// commsSeats answers whether a staged delivery's sender is still a live,
// mutation-capable seat, over the SAME resolver the request-time gate reads
// — so a user deactivated or downgraded after staging loses the mailbox the
// same instant they lose the session.
//
// The translation mirrors commsResolver's: ErrNotFound is one ANSWER ("this
// human is no longer a live seat in this workspace" — identity's authority
// read filters on status and archived_at together), a live row whose seat
// cannot mutate is the other, and everything else is a failure to get an
// answer, which must not permanently destroy mail. The two answers are kept
// apart in the reason each returns: CanMutate is the SAME predicate
// platform/auth's Admit and automation's gate check before letting a
// principal mutate (A62/ADR-0047) — reading the seat and then sending anyway
// would make this the one gate on the licensing ceiling that does not enforce
// it.
type commsSeats struct{ authority seatResolver }

var _ comms.SeatAuthority = commsSeats{}

// NewSendSeatAuthority builds the live-seat gate the dispatcher re-reads at
// transmit time. Exported for the reason NewDeliveryStager is: the seam is
// assembled here, and a caller that rebuilt it from its own parts would be
// testing a translation the product does not ship.
//
//nolint:ireturn // returns the comms.SeatAuthority seam by design: the concrete type is unexported and every caller holds the interface
func NewSendSeatAuthority(pool *pgxpool.Pool) comms.SeatAuthority {
	return commsSeats{authority: identity.NewService(pool)}
}

func (s commsSeats) ActiveSeat(ctx context.Context, userID ids.UserID) (bool, string, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return false, "", errors.New("comms: resolving a delivery's sender outside workspace context")
	}
	seat, err := s.authority.SeatType(ctx, ws, userID.UUID)
	if errors.Is(err, apperrors.ErrNotFound) {
		return false, "the sender's account is no longer active; a deactivated user's mailbox may not transmit staged messages", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("comms: reading the sender's seat: %w", err)
	}
	if !seat.CanMutate() {
		return false, "the sender holds a read-only seat; a read seat may not transmit staged messages", nil
	}
	return true, "", nil
}

// commsStager records an accepted send for transmission: the delivery row and
// the job that will carry it, both on the caller's transaction. One commit, one
// fact — a crash between them would either promise a send nothing queued or
// queue one with no timeline entry behind it.
type commsStager struct {
	store  *comms.Store
	runner *jobs.Runner
}

var (
	_ activities.DeliveryStager        = commsStager{}
	_ activities.ChannelDeliveryStager = commsStager{}
)

// DeliveryMachinery is the ONE delivery path in both the shapes a message can be
// staged in. It is a single seam rather than two because there is a single
// machinery behind it — one delivery table, one status machine, one retry ladder,
// one dispatcher — and a role able to wire mail staging without channel staging
// could serve a reply surface that accepts a message nothing will ever carry.
type DeliveryMachinery interface {
	activities.DeliveryStager
	activities.ChannelDeliveryStager
}

// NewDeliveryStager builds the delivery machinery every send transport is
// composed with (compose.WithDelivery). The runner is insert-only in the api
// role; the worker role works what it inserts.
//
//nolint:ireturn // returns the DeliveryMachinery seam by design: the concrete type is unexported and every caller holds the interface
func NewDeliveryStager(pool *pgxpool.Pool, runner *jobs.Runner) DeliveryMachinery {
	return commsStager{store: comms.NewStore(pool, time.Now, activities.NewStore(pool)), runner: runner}
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
		Workspace: ws, DeliveryID: id.String(),
	}, sendInsertOpts())
}

// StageChannelTx is the same staging for a channel reply: the channel-shaped row
// and the SAME transmit job, on the caller's transaction.
//
// One job kind carries both shapes deliberately. The worker loads the delivery
// and dispatches it, and the dispatcher branches on the ROW's shape exactly once
// (comms/sendseam.go) — a second job kind would be a second path to keep in step
// with the first, and the channel is the one that would fall behind.
func (s commsStager) StageChannelTx(ctx context.Context, tx pgx.Tx, in activities.ChannelDeliveryRequest) error {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("comms: staging a channel delivery outside workspace context")
	}
	id, err := s.store.StageChannelTx(ctx, tx, comms.StageChannelInput{
		ActivityID:     in.ActivityID,
		Provider:       in.Provider,
		Recipient:      in.Recipient,
		Body:           in.Body,
		ConsentPurpose: in.ConsentPurpose,
	})
	if err != nil {
		return err
	}
	return s.runner.EnqueueTx(ctx, tx, SendEmailArgs{
		Workspace: ws, DeliveryID: id.String(),
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
		// The reconcile seam is the cross-module edge comms must not hold
		// itself: activities owns the timeline row, comms owns the delivery,
		// and the two meet here.
		comms.NewStore(pool, time.Now, activities.NewStore(pool)),
		commsResolver{registry: registry, channels: registry},
		NewSendSeatAuthority(pool),
		consent.NewGate(consent.NewStore(pool)),
		[]comms.SendPolicy{comms.NewMailboxRatePolicy(p.Limit, p.Window, time.Now)},
		time.Now,
		p.MaxAge,
		// The SAME ladder length River enqueues with (sendInsertOpts): the
		// dispatcher parks on the last rung, and it can only know which rung
		// that is by being told the runner's own number. Read off the insert
		// options rather than the constant, so the two cannot be changed
		// apart: there is exactly one place the ladder length is declared.
		sendInsertOpts().MaxAttempts,
	)}
}

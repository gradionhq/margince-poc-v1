// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// defaultScheduleGrace bounds how late a scheduled message may still fire.
//
// The rep chose a MOMENT, and that choice carries meaning a late send
// destroys: mail timed for Monday 09:00 is wrong mail at 18:00. Past this, the
// message is held for a human rather than sent (ADR-0104 §6).
const defaultScheduleGrace = time.Hour

// scheduledSendMaxAttempts bounds the timer's ladder. Its last rung holds the
// message, so an infrastructure failure surfaces as a message waiting for a
// human rather than a row nothing will ever wake again.
const scheduledSendMaxAttempts = 5

// ScheduledSendArgs wakes ONE message a rep chose to send later. It carries the
// row's id and nothing else: the row is the schedule, and everything this job
// needs to decide — whether it is still wanted, when it is due, who it fires as
// — is read from the row at wake time, never frozen into the alarm.
type ScheduledSendArgs struct {
	Workspace       ids.UUID `json:"workspace_id"`
	ScheduledSendID string   `json:"scheduled_send_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (ScheduledSendArgs) Kind() string { return "comms_scheduled_send" }

// WorkspaceID binds this alarm to its tenant (jobs.WorkspaceScoped).
func (a ScheduledSendArgs) WorkspaceID() ids.UUID { return a.Workspace }

// WithScheduleTimer wires the alarm a deferred send is accepted against.
//
// Without it a scheduling request refuses: accepting "send it Monday" with
// nothing to wake the message is a promise this surface cannot keep, and the
// rep would only discover it on Monday.
func WithScheduleTimer(timer activities.ScheduleTimer) Option {
	return func(s *Server, pool *pgxpool.Pool) {
		s.send.ScheduleTimer = timer
		s.rebuildToolRegistry(pool)
	}
}

// scheduleTimer enqueues the alarm that wakes one scheduled message.
type scheduleTimer struct {
	runner *jobs.Runner
}

// NewScheduleTimer builds the seam the send path defers through.
func NewScheduleTimer(runner *jobs.Runner) scheduleTimer {
	return scheduleTimer{runner: runner}
}

// ScheduleTx enqueues the alarm in the caller's transaction, so the row and its
// timer commit together or not at all.
func (t scheduleTimer) ScheduleTx(ctx context.Context, tx pgx.Tx, id ids.UUID, due time.Time) error {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return errors.New("compose: scheduling a send outside workspace context")
	}
	return t.runner.EnqueueTx(ctx, tx, ScheduledSendArgs{
		Workspace:       ws,
		ScheduledSendID: id.String(),
	}, &river.InsertOpts{
		ScheduledAt: due,
		MaxAttempts: scheduledSendMaxAttempts,
	})
}

// scheduledSendWorker fires one scheduled message, or holds it.
//
// The job is a dumb alarm and the ROW is the schedule. This wakes, re-reads the
// row, and does nothing if the message was cancelled, snoozes if its moment
// moved later, and otherwise fires it through the one send path — under the
// scheduling human's freshly re-derived authority, never the one they had when
// they pressed the button.
type scheduledSendWorker struct {
	pool      *pgxpool.Pool
	store     *activities.Store
	authority *identity.Service
	consent   activities.ConsentGate
	delivery  activities.DeliveryStager
	grace     time.Duration
	now       func() time.Time
}

// newScheduledSendWorker assembles the worker that fires deferred messages.
//
// It builds its store through sendStore — the SAME assembly the tool surface
// sends through — so a scheduled message is rendered by the machinery an
// immediate one is: the signature, the sender name, the unsubscribe linker and
// the draft-outcome recorder all present, all identical. A hand-built store
// here would be a second send path wearing the first one's name.
func newScheduledSendWorker(pool *pgxpool.Pool, delivery DeliveryMachinery, blob blobstore.Store, pacing SendPacing) *scheduledSendWorker {
	return &scheduledSendWorker{
		pool:      pool,
		store:     sendStore(pool, SendPath{}).WithBlobstore(blob),
		authority: identity.NewService(pool),
		consent:   consent.NewGate(consent.NewStore(InstallationDB(pool))),
		// The SAME machinery every other send stages with, handed in rather
		// than built here: firing enqueues a delivery job, and a second
		// construction site would be a second way for the two to disagree.
		delivery: delivery,
		grace:    pacing.withDefaults().ScheduleGrace,
		now:      time.Now,
	}
}

func (w *scheduledSendWorker) Work(ctx context.Context, job *river.Job[ScheduledSendArgs]) error {
	id, err := ids.Parse(job.Args.ScheduledSendID)
	if err != nil {
		return jobs.FaultContext(ctx, fmt.Errorf("comms_scheduled_send: scheduled send id: %w", err))
	}
	wsCtx, err := workspaceJobCtx(ctx, job.Args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}

	// Who this message fires as. Read under a system scope, because resolving
	// the scheduler is what tells us whose authority to adopt — the answer
	// cannot come from the authority we are still deciding.
	scheduler, kind, err := w.scheduler(sendWorkerScope(wsCtx), id)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if scheduler == (ids.UUID{}) {
		// The row is gone, cancelled, or already finished. The alarm rang for
		// a message that no longer wants sending, which is the ordinary way a
		// cancelled send stops.
		return nil
	}

	fireCtx, gone, err := w.fireAs(wsCtx, scheduler, kind)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	if gone {
		// The scheduler lost their account or their seat. Holding under a
		// system scope, because the authority we would otherwise hold under is
		// exactly the one that just failed to resolve.
		if err := w.hold(w.holdScope(wsCtx, scheduler), id, activities.HeldSenderInactive); err != nil {
			return jobs.FaultContext(ctx, err)
		}
		return nil
	}

	outcome, err := w.store.FireScheduledSend(fireCtx, id, w.grace, w.consent, w.delivery)
	if err != nil {
		if job.Attempt >= scheduledSendMaxAttempts {
			// Last rung. A row left 'scheduled' with no live timer is a message
			// nobody will ever see fail, so the ladder ends by handing it to a
			// human instead of going quiet.
			if holdErr := w.hold(w.holdScope(wsCtx, scheduler), id, activities.HeldTimerExhausted); holdErr != nil {
				return jobs.FaultContext(ctx, errors.Join(err, holdErr))
			}
			return nil
		}
		return jobs.FaultContext(ctx, fmt.Errorf("comms_scheduled_send: firing %s: %w", id, err))
	}
	if !outcome.Due.IsZero() {
		// Its moment moved later. Come back then; the row remains the schedule.
		return river.JobSnooze(time.Until(outcome.Due))
	}
	return nil
}

// scheduler reads who a pending scheduled message fires as, and what kind of
// principal that is. A zero id means there is nothing to fire.
func (w *scheduledSendWorker) scheduler(ctx context.Context, id ids.UUID) (ids.UUID, string, error) {
	var (
		userID ids.UUID
		kind   string
	)
	err := database.WithWorkspaceTx(ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT scheduled_by, principal_kind
			  FROM scheduled_send
			 WHERE id = $1 AND status = 'scheduled'`, id).Scan(&userID, &kind)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UUID{}, "", nil
	}
	if err != nil {
		return ids.UUID{}, "", fmt.Errorf("comms_scheduled_send: reading who scheduled %s: %w", id, err)
	}
	return userID, kind, nil
}

// fireAs rebuilds the authority this message fires under, live.
//
// The grants are re-read now rather than trusted from scheduling time, so a
// seat or role withdrawn in between holds the message instead of sending under
// authority its owner no longer has.
//
// The principal KIND is preserved, and that is not a detail. The send path
// withholds a human's sign-off and display name when an agent is the actor, so
// an agent-scheduled message fired as a human would go out over the approver's
// signature — something the identical immediate send would never do
// (ADR-0104 §4).
func (w *scheduledSendWorker) fireAs(ctx context.Context, userID ids.UUID, kind string) (context.Context, bool, error) {
	ws, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, false, errors.New("comms_scheduled_send: firing outside workspace context")
	}
	// ONE snapshot of grants and seat. Read separately they can compose an
	// authority the member never held — permissions from before a role change
	// with a seat from after it — which is the exact hazard EffectiveAuthority
	// exists to close, and both are ceilings on this same act.
	rbac, seat, err := w.authority.EffectiveAuthority(ctx, ws, userID)
	if errors.Is(err, apperrors.ErrNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("comms_scheduled_send: reading the sender's authority: %w", err)
	}
	if !seat.CanMutate() {
		// A read seat may not transmit, and the dispatcher would refuse this
		// message at the far end anyway. Holding here means the rep is told
		// their seat is the problem, rather than finding a released row whose
		// delivery silently parked.
		return nil, true, nil
	}

	actor := principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + userID.String(),
		UserID:      userID,
		TeamIDs:     rbac.TeamIDs,
		SeatType:    seat,
		Permissions: rbac.Permissions,
		// The rep this message belongs to, named even when they ARE the actor.
		// A fire is work done FOR somebody: the audit rows it writes should say
		// whose message it was, not merely who executed it, and an agent-kind
		// fire below carries the same human underneath.
		OnBehalfOf: userID,
	}
	if kind == "agent" {
		// An agent acting under this human's authority — which is what it was
		// when the message was scheduled, and what it must still be now.
		actor.Type = principal.PrincipalAgent
		actor.ID = "agent:" + userID.String()
	}
	fireCtx := principal.WithActor(ctx, actor)
	return principal.WithCorrelationID(fireCtx, ids.NewV7()), false, nil
}

// holdScope is the system scope a hold runs under, naming the rep it acts FOR.
//
// The system is the actor — this is the alarm doing what a human asked for
// earlier, not that human acting again — but the message is one rep's, and an
// audit trail that recorded only "the system held something" would not say
// whose message stopped. Surfacing a held message TO that rep is #1312; this is
// the provenance any such surface will read.
func (w *scheduledSendWorker) holdScope(wsCtx context.Context, scheduler ids.UUID) context.Context {
	ctx := sendWorkerScope(wsCtx)
	actor, ok := principal.Actor(ctx)
	if !ok {
		return ctx
	}
	actor.OnBehalfOf = scheduler
	return principal.WithActor(ctx, actor)
}

// hold hands a message to a human with the reason they need.
func (w *scheduledSendWorker) hold(ctx context.Context, id ids.UUID, reason string) error {
	if err := w.store.HoldScheduledSend(ctx, id, reason); err != nil {
		return fmt.Errorf("comms_scheduled_send: holding %s: %w", id, err)
	}
	return nil
}

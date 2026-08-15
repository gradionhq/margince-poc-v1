// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edge between a scheduled message the system stopped and the
// rep who has to decide about it. activities owns the message and approvals owns
// the inbox; neither imports the other, so the edge is injected here.
//
// The card a rep sees carries the same two buttons every other card carries, and
// both do real work: Accept re-arms the message for a fresh moment, Reject
// abandons it. A card whose buttons dismissed it while the message stayed held
// would be worse than no card — it would report a decision that never happened.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// heldRetryDelay is how far ahead Accept re-arms a held message.
//
// A rep accepting a held card is saying "yes, still send this" — not choosing a
// new moment, which is what the message's own reschedule surface is for. The
// delay is short enough to feel like "shortly" and long enough that a rep who
// has just fixed a consent record is not racing the timer.
const heldRetryDelay = 15 * time.Minute

// heldScheduledSendKind is the approval kind a stopped message files under —
// its own kind rather than a send kind, because what a rep decides here is
// whether to try again or give up, not whether to send.
const heldScheduledSendKind = approvals.KindScheduledSendHeld

// heldScheduledSendTarget is the entity type the card declares.
//
// `activity`, not `scheduled_send`, and the reason is the inbox rather than the
// domain: every staged target passes an object-read floor
// (approvals/targetvisibility.go objectReadable) that asks auth.Require for a
// READ grant on the named type. There is no scheduled_send RBAC object — the
// message is governed by the activity it would become, which is also the grant
// scheduling it required — so naming one here makes every card undecidable and
// invisible: written to the table and filtered out of the surface that exists to
// show it. The message itself is named in the payload.
const heldScheduledSendTarget = "activity"

// scheduledSendHeldNotifier files a stopped message in its scheduler's inbox.
type scheduledSendHeldNotifier struct {
	approvals *approvals.Service
}

// NewScheduledSendHeldNotifier builds the seam the send path holds through.
func NewScheduledSendHeldNotifier(service *approvals.Service) scheduledSendHeldNotifier {
	return scheduledSendHeldNotifier{approvals: service}
}

// heldProposal is what the card carries: enough for a rep to recognise the
// message and know which gate refused. The body is deliberately absent — the
// card points at a message the rep can open, it is not a second copy of it.
type heldProposal struct {
	ScheduledSendID string `json:"scheduled_send_id"`
	Reason          string `json:"reason"`
	Subject         string `json:"subject"`
	ScheduledAt     string `json:"scheduled_at"`
}

// NotifyHeldInTx stages the card in the caller's transaction, so the hold and
// the card a rep acts on commit together or not at all.
//
// JoinPending, because the fire path is at-least-once: a timer waking twice for
// one message must find the existing card rather than multiply rows in somebody's
// inbox. The identity is the message, so a message held twice for two reasons
// supersedes rather than competes — the live reason is the one a rep needs, and a
// stale one beside it would only ask them to guess which stopped it.
func (n scheduledSendHeldNotifier) NotifyHeldInTx(ctx context.Context, tx pgx.Tx, in activities.HeldNotice) error {
	proposal, err := json.Marshal(heldProposal{
		ScheduledSendID: in.ScheduledSendID.String(),
		Reason:          in.Reason,
		Subject:         in.Subject,
		ScheduledAt:     in.ScheduledAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("compose: describing the held message: %w", err)
	}
	identity, err := json.Marshal(map[string]string{"scheduled_send_id": in.ScheduledSendID.String()})
	if err != nil {
		return fmt.Errorf("compose: identifying the held message: %w", err)
	}
	digest := sha256.Sum256(proposal)
	_, err = n.approvals.StageOrJoinPendingInTx(ctx, tx, approvals.StageInput{
		Kind:           heldScheduledSendKind,
		ProposedChange: proposal,
		DiffHash:       hex.EncodeToString(digest[:]),
		TargetType:     heldScheduledSendTarget,
		Summary:        heldSummary(in),
		JoinPending:    true,
		Identity:       identity,
	})
	return err
}

// ResolveHeldInTx withdraws the card once the rep has acted on the message
// directly — rescheduled or cancelled it from its own surface, which IS the
// answer. Without this a rep answers once and the card asks again.
//
// Found by the message named in the payload: the card carries no target id, for
// the reason heldScheduledSendTarget gives.
func (n scheduledSendHeldNotifier) ResolveHeldInTx(ctx context.Context, tx pgx.Tx, scheduledSendID ids.UUID) error {
	pending, err := n.pendingCardsFor(ctx, tx, scheduledSendID)
	if err != nil {
		return err
	}
	for _, id := range pending {
		if _, err := n.approvals.WithdrawInTx(ctx, tx, id, "the rep rescheduled or cancelled this message"); err != nil {
			return fmt.Errorf("compose: clearing the held message's card: %w", err)
		}
	}
	return nil
}

// pendingCardsFor finds the live cards raised for one message.
func (n scheduledSendHeldNotifier) pendingCardsFor(ctx context.Context, tx pgx.Tx, scheduledSendID ids.UUID) ([]ids.ApprovalID, error) {
	rows, err := tx.Query(ctx, `
		SELECT id FROM approval
		 WHERE kind = $1 AND status = 'pending'
		   AND proposed_change->>'scheduled_send_id' = $2`,
		heldScheduledSendKind, scheduledSendID.String())
	if err != nil {
		return nil, fmt.Errorf("compose: finding the held message's card: %w", err)
	}
	defer rows.Close()
	var pending []ids.ApprovalID
	for rows.Next() {
		var id ids.ApprovalID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("compose: reading the held message's card: %w", err)
		}
		pending = append(pending, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("compose: reading the held message's cards: %w", err)
	}
	return pending, nil
}

// heldSummary is the line a rep reads at a glance. "A scheduled send was held"
// tells them nothing they can act on, so it names the message and what stopped it.
func heldSummary(in activities.HeldNotice) string {
	return fmt.Sprintf("%q was not sent: %s", in.Subject, heldReasonText(in.Reason))
}

// heldReasonText renders a hold reason as the sentence a rep needs. The codes are
// the durable record; this is the half a human reads.
func heldReasonText(reason string) string {
	switch reason {
	case activities.HeldConsentWithdrawn:
		return "a recipient withdrew consent for this purpose after it was scheduled"
	case activities.HeldSenderInactive:
		return "the sending account or its mailbox is no longer active"
	case activities.HeldPassportRevoked:
		return "the agent credential it was scheduled under has been revoked or expired — your account is fine, so send it yourself if it should still go"
	case activities.HeldMissedWindow:
		return "its moment passed while nothing was running, and it is too late to be the message that was written"
	case activities.HeldTimerExhausted:
		return "the job that wakes it ran out of attempts"
	case activities.HeldSendRefused:
		return "a gate refused it at send time"
	}
	// An unknown code still reaches the rep: a held message with no explanation
	// is worse than an unpolished one.
	return reason
}

var _ activities.HeldNotifier = scheduledSendHeldNotifier{}

// heldAcceptEffect is what Accept does: re-arm the message for a fresh moment.
//
// "Yes, still send this." The rep is not choosing a new time here — the card has
// no time picker and should not grow one — so it goes shortly, which is long
// enough that somebody who has just fixed a consent record is not racing the
// timer. A rep who wants a specific moment reschedules from the message itself.
//
// The gates run again when it fires, exactly as they did the first time. A rep
// who accepts without having fixed the underlying problem gets a second hold
// rather than a send, which is the correct answer and the reason this is safe to
// offer as a one-click action.
func heldAcceptEffect(svc *approvals.Service, store *activities.Store, timer activities.ScheduleTimer) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		id, err := heldMessageID(proposedChange)
		if err != nil {
			return err
		}
		// Read the row for its version: reschedule is version-guarded, and the
		// hold that raised this card bumped it.
		current, err := store.GetScheduledSend(ctx, id)
		if err != nil {
			return err
		}
		// RedeemAndApply, not a bare call: the approval is consumed and the
		// message moved in ONE transaction, so a failed move leaves the card
		// unconsumed and retryable. Without it a transient error would commit
		// the decision, remove the card, and leave the message held — exactly
		// the silent stop this card exists to prevent.
		return svc.RedeemAndApply(ctx, approvalID, heldScheduledSendKind, diffHash, func(tx pgx.Tx) error {
			return store.RescheduleInTx(ctx, tx, id, activities.SendSchedule{
				At: time.Now().Add(heldRetryDelay),
				TZ: current.ScheduledTZ,
			}, current.Version, current, timer)
		})
	}
}

// heldDeclineEffect is what Reject does: abandon the message.
//
// Without this the card would leave the inbox and the message would stay held
// forever — a decision recorded against a subject that never heard it. Rejecting
// a proposal usually means "do not apply it" and needs no effect; rejecting THIS
// means "stop waiting", because the thing it names is already waiting.
func heldDeclineEffect(store *activities.Store) approvals.DeclinedEffect {
	return func(ctx context.Context, tx pgx.Tx, _ ids.ApprovalID, proposedChange json.RawMessage) error {
		id, err := heldMessageID(proposedChange)
		if err != nil {
			return err
		}
		// The DECISION's transaction, handed in: the rejection and the
		// cancellation commit together, so a failed cancel takes the rejection
		// with it and the rep can try again. Rejected-but-still-held is the one
		// outcome this card cannot produce.
		return store.CancelInTx(ctx, tx, id)
	}
}

// heldMessageID reads which message a card is about.
func heldMessageID(proposedChange json.RawMessage) (ids.UUID, error) {
	var proposal heldProposal
	if err := json.Unmarshal(proposedChange, &proposal); err != nil {
		return ids.UUID{}, fmt.Errorf("compose: reading the held message's card: %w", err)
	}
	id, err := ids.Parse(proposal.ScheduledSendID)
	if err != nil {
		return ids.UUID{}, fmt.Errorf("compose: the card names no readable message: %w", err)
	}
	return id, nil
}

// heldSendActors builds what the two held-card effects need: the store that owns
// the message and the timer that re-arms it.
//
// Insert-only runner, like every other surface that stages a job it does not
// work — the api enqueues and the worker role runs it. A pool this cannot open a
// runner on reports false rather than panicking: the effects are then
// unregistered, and the decidability gate is what makes that loud (a kind with a
// decision-grant mapping and no effect fails TestEveryRegisteredEffectKind…).
func heldSendActors(pool *pgxpool.Pool) (*activities.Store, scheduleTimer, bool) {
	if pool == nil {
		return nil, scheduleTimer{}, false
	}
	inserter, err := jobs.NewInserter(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, scheduleTimer{}, false
	}
	return sendStore(pool, SendPath{}), NewScheduleTimer(inserter), true
}

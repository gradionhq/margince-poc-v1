// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The identity reconcile, and everything that keeps it from costing a second
// email. It is one concept and it lives in one file: a receipt for a message
// the provider has ALREADY accepted has committed by the time any of this
// runs, so every path here — a refusing seam, an aborted statement, a panic,
// and the fault report itself — has to end in "the receipt stands, one
// duplicate timeline row". store.go owns the delivery lifecycle; this owns the
// correction that rides inside its transaction.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// errNoReconciler names a store built with no message-identity seam at all.
// nil stays constructible because a role that only READS deliveries has no
// timeline row to re-key and should not have to drag the seam in to say so —
// but a role that TRANSMITS and cannot reconcile is a wiring mistake, which
// the composition's own fitness test is what catches before it ships.
//
// At runtime it is treated as what it is: one more reconcile fault, degrading
// to "receipt recorded, one duplicate timeline row" like every other. Letting
// it reach the savepoint instead would dereference nil INSIDE the transaction.
// reKeyGuarded would catch that panic like any other, so the cost is no longer
// a second email — but the breadcrumb would then read "the reconcile panicked"
// about a store that was misbuilt at composition time, which is not what an
// operator needs to be told. Checked here, the report names the actual fault.
var errNoReconciler = errors.New("comms: this delivery store was built with no message-identity reconciler")

// errUnusableIdentity names a receipt whose stamped RFC822 identity is not one
// a message could carry. It is a PROVIDER-shaped fault: the value was read back
// out of a remote response, and adopting it would put an unsearchable string in
// the column the echo collapse, the reply join and the threading headers all
// key on. Recorded like every other reconcile fault — the receipt stands, the
// message keeps the identity it was staged under.
var errUnusableIdentity = errors.New("comms: the provider reported a message identity no message could carry")

// reconcileIdentity moves the delivery and its timeline row onto the identity
// the provider stamped, and reports nothing: by construction there is no
// failure here the caller may act on, because the only action available —
// failing the receipt — is the one thing that must never happen. The savepoint
// returns the transaction to a usable state so the receipt still commits, and
// the breadcrumb is what an operator reads.
func (s *Store) reconcileIdentity(ctx context.Context, tx pgx.Tx, deliveryID ids.UUID, activityID ids.ActivityID, staged, stamped string) {
	if stamped == "" || stamped == staged {
		// The provider honoured the identity, reports none, or could not be
		// asked. All three mean the staged key is already the key the wire
		// carries, so there is nothing to move.
		return
	}
	if !connector.ValidMessageID(stamped) {
		// The stamped identity is parsed out of a remote provider's response
		// bytes, and everything downstream treats it as a natural key: it is
		// written onto two rows, matched against a captured echo, and read back
		// by the reply join. ValidMessageID is the ONE spelling of what counts
		// as a usable identity, the same one the send side refused to transmit
		// without — so a read-back that answers with something no message could
		// carry is a fault to record, never a key to adopt.
		s.breadcrumb(ctx, tx, deliveryID, staged, boundedIdentity(stamped), errUnusableIdentity)
		return
	}
	if s.identity == nil {
		// Checked BEFORE the savepoint, because the fault is in this store's
		// construction rather than in anything the database is about to be
		// asked. Recording it the same way every other reconcile fault is
		// recorded is what keeps a misconfigured role from turning a
		// bookkeeping gap into a second email.
		s.breadcrumb(ctx, tx, deliveryID, staged, stamped, errNoReconciler)
		return
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "comms: opening the identity-reconcile savepoint",
			"err", err, "delivery_id", deliveryID)
		return
	}
	if reKeyErr := s.reKeyGuarded(ctx, sp, deliveryID, activityID, staged, stamped); reKeyErr != nil {
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			// Without a clean rollback the transaction stays poisoned and the
			// receipt cannot commit either — there is nothing left to record
			// the fault on, so the log is the whole record.
			slog.ErrorContext(ctx, "comms: rolling back the identity reconcile",
				"err", rbErr, "cause", reKeyErr, "delivery_id", deliveryID)
			return
		}
		s.breadcrumb(ctx, tx, deliveryID, staged, stamped, reKeyErr)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "comms: committing the identity reconcile",
			"err", err, "delivery_id", deliveryID)
	}
}

// reKeyGuarded runs the re-key and turns a PANIC into an ordinary reconcile
// fault, so the caller's savepoint answers it the way it answers every other
// one.
//
// Nothing on the path visibly panics today. The guard is structural rather than
// an audit of the current code: a panic escaping here would unwind through
// WithWorkspaceTx's deferred rollback, take the already-accepted receipt with
// it, fail the job, and let the redelivery transmit the message a second time —
// the double-send this whole ordering exists to prevent. That consequence is
// the same whether the fault is a returned error or an index out of range in
// code somebody adds next year, so the answer to it must be too.
func (s *Store) reKeyGuarded(ctx context.Context, tx pgx.Tx, deliveryID ids.UUID, activityID ids.ActivityID, staged, stamped string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("comms: the message-identity reconcile panicked: %v", r)
		}
	}()
	return s.reKey(ctx, tx, deliveryID, activityID, staged, stamped)
}

// reKey writes the stamped identity onto the delivery and then hands the
// timeline row to the module that owns it.
//
// thread_key moves ONLY when it equalled the message's own identity, the same
// condition the activity side applies and for the same reason: a conversation
// ROOT re-roots onto the identity the world will reply to, while a REPLY's
// thread key is the root of the conversation it joined and belongs to that
// conversation, not to this message.
func (s *Store) reKey(ctx context.Context, tx pgx.Tx, deliveryID ids.UUID, activityID ids.ActivityID, staged, stamped string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE comms_outbound
		   SET message_id  = $2,
		       thread_key  = CASE WHEN thread_key = $3 THEN $2 ELSE thread_key END
		 WHERE id = $1`, deliveryID, stamped, staged); err != nil {
		return fmt.Errorf("comms: re-keying the delivery: %w", err)
	}
	return s.identity.ReconcileMessageIdentityTx(ctx, tx, activityID, staged, stamped)
}

// breadcrumb records a re-key this installation could not complete, on the
// caller's transaction AFTER the reconcile savepoint has been rolled back —
// writing it on the poisoned savepoint would fail with the fault it is trying
// to report. The delivery keeps the identity it was staged under, so the
// operator reading this row is reading why one message will appear on the
// timeline twice.
//
// It takes a SAVEPOINT of its own, and that is not defensive habit. This is an
// INSERT, and every Postgres statement can be refused; a refusal on the bare
// transaction aborts it, the receipt then fails to commit, RecordSent returns a
// non-terminal error, the dispatcher answers OutcomeRetry, and the redelivery
// mails the recipient a second time. The error-reporting path would have caused
// the exact failure it exists to report. It only ever runs when something has
// already gone wrong, which is the worst moment to be the last unguarded writer
// in the transaction.
func (s *Store) breadcrumb(ctx context.Context, tx pgx.Tx, deliveryID ids.UUID, staged, stamped string, cause error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "comms: opening the identity-reconcile breadcrumb savepoint",
			"err", err, "cause", cause, "delivery_id", deliveryID)
		return
	}
	if _, err := storekit.LogSystem(ctx, sp, "comms_identity_reconcile_failed", map[string]any{
		"delivery_id":         deliveryID.String(),
		"staged_message_id":   staged,
		"provider_message_id": stamped,
		"reason":              cause.Error(),
	}); err != nil {
		slog.ErrorContext(ctx, "comms: recording the identity-reconcile fault",
			"err", err, "cause", cause, "delivery_id", deliveryID)
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			slog.ErrorContext(ctx, "comms: rolling back the identity-reconcile breadcrumb",
				"err", rbErr, "cause", cause, "delivery_id", deliveryID)
		}
		return
	}
	if err := sp.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "comms: committing the identity-reconcile breadcrumb",
			"err", err, "cause", cause, "delivery_id", deliveryID)
	}
}

// boundedIdentity renders a REJECTED provider identity for the breadcrumb.
// Everything else the breadcrumb writes is this installation's own; this one
// field is whatever a remote response contained, and it reached here precisely
// because it is not a shape any message could carry — it may be megabytes long,
// or hold the NUL byte a jsonb value cannot even store. A fault report that
// cannot be written is no report, so the value is clipped and its
// non-printables replaced, with the original length kept because that is the
// fact that actually diagnoses a runaway read-back.
func boundedIdentity(id string) string {
	const keep = 120
	clipped := id
	if len(clipped) > keep {
		clipped = clipped[:keep]
	}
	var b strings.Builder
	for _, r := range clipped {
		// utf8.RuneError also stands in for the byte a clip landed mid-rune on.
		if r < 0x20 || r == 0x7F || r == utf8.RuneError {
			b.WriteByte('.')
			continue
		}
		b.WriteRune(r)
	}
	if len(id) > keep {
		fmt.Fprintf(&b, "…(%d bytes)", len(id))
	}
	return b.String()
}

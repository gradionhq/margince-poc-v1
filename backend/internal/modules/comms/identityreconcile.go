// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The identity reconcile, and everything that keeps it from costing a second
// email. It is one concept and it lives in one file: the receipt for a message
// the provider has ALREADY accepted is committed and gone by the time any of
// this runs, so every path here — a refusing seam, an aborted statement, a lost
// connection, a panic, and the fault report itself — has to end in "the receipt
// stands, one duplicate timeline row". store.go owns the delivery lifecycle;
// this owns the correction that follows it in a transaction of its own.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
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
// it reach the transaction instead would dereference nil inside it. The recover
// boundary would catch that panic like any other, so the cost is the same — but
// the breadcrumb would then read "the reconcile panicked" about a store that
// was misbuilt at composition time, which is not what an operator needs to be
// told. Checked here, the report names the actual fault.
var errNoReconciler = errors.New("comms: this delivery store was built with no message-identity reconciler")

// errUnusableIdentity names a receipt whose stamped RFC822 identity is not one
// a message could carry. It is a PROVIDER-shaped fault: the value was read back
// out of a remote response, and adopting it would put an unsearchable string in
// the column the echo collapse, the reply join and the threading headers all
// key on. Recorded like every other reconcile fault — the receipt stands, the
// message keeps the identity it was staged under.
var errUnusableIdentity = errors.New("comms: the provider reported a message identity no message could carry")

// reconcileIdentity moves the delivery and its timeline row onto the identity
// the provider stamped, in a transaction of its own, and reports nothing: by
// construction there is no failure here the caller may act on, because the
// receipt this corrects is already committed and the only action available —
// failing it — is the one thing that must never happen. The breadcrumb is what
// an operator reads instead.
//
// EVERYTHING is inside the recover boundary, not only the seam call. A panic
// escaping this function would unwind the dispatch attempt, fail the job, and
// let the redelivery transmit an already-accepted message a second time — so
// the guard has to cover the fault report and the transaction plumbing too,
// which is where a panic is least expected and therefore most likely to be
// let through. Nothing on this path visibly panics today; the boundary is
// structural, because the consequence is the same whatever the shape of the
// fault or the year it is introduced in.
//
// It opens its own bounded, uncancellable context for the same reason the
// receipt has one: the message is already sent, and the caller's deadline
// expiring is no reason to leave the timeline naming an identity that exists
// nowhere on the wire.
func (s *Store) reconcileIdentity(callerCtx context.Context, deliveryID ids.UUID, activityID ids.ActivityID, staged, stamped string) {
	ctx, cancel := detachedWrite(callerCtx)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "comms: the message-identity reconcile came apart",
				"panic", r, "delivery_id", deliveryID)
		}
	}()

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
		s.breadcrumb(ctx, deliveryID, staged, boundedIdentity(stamped), errUnusableIdentity)
		return
	}
	if s.identity == nil {
		// Checked BEFORE the transaction opens, because the fault is in this
		// store's construction rather than in anything the database is about to
		// be asked. Recording it the same way every other reconcile fault is
		// recorded is what keeps a misconfigured role from turning a
		// bookkeeping gap into a second email.
		s.breadcrumb(ctx, deliveryID, staged, stamped, errNoReconciler)
		return
	}
	if err := s.reKeyGuarded(ctx, deliveryID, activityID, staged, stamped); err != nil {
		s.breadcrumb(ctx, deliveryID, staged, stamped, err)
	}
}

// reKeyGuarded runs the re-key in its own transaction and turns a PANIC into an
// ordinary reconcile fault, so the breadcrumb names it the way it names every
// other one. The outer boundary in reconcileIdentity would contain the panic
// either way; recovering it here is what turns it into something an operator
// finds on disk rather than only in a log line.
func (s *Store) reKeyGuarded(ctx context.Context, deliveryID ids.UUID, activityID ids.ActivityID, staged, stamped string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("comms: the message-identity reconcile panicked: %v", r)
		}
	}()
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.reKey(ctx, tx, deliveryID, activityID, staged, stamped)
	})
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

// breadcrumb records a re-key this installation could not complete, in a
// transaction of its own — the one the re-key failed in is gone, and it is gone
// precisely because whatever happened in it may have made it unusable. The
// delivery keeps the identity it was staged under, so the operator reading this
// row is reading why one message will appear on the timeline twice.
//
// A breadcrumb that cannot be written is logged and dropped. This is an INSERT
// like any other, and Postgres may refuse any statement — but the receipt it
// would once have taken down with it committed long before this ran, so the
// worst case is now what it should always have been: a degradation nobody is
// told about in the database, rather than a second email.
func (s *Store) breadcrumb(ctx context.Context, deliveryID ids.UUID, staged, stamped string, cause error) {
	if err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := storekit.LogSystem(ctx, tx, "comms_identity_reconcile_failed", map[string]any{
			"delivery_id":         deliveryID.String(),
			"staged_message_id":   staged,
			"provider_message_id": stamped,
			"reason":              cause.Error(),
		})
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "comms: recording the identity-reconcile fault",
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

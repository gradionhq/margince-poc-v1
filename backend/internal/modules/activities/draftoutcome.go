// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The send half of the voice learning loop (ADR-0066 §4). Drafting hands the
// caller an opaque reference to the text a model served; the send that carries
// that reference back is what says whether the human sent that text or
// reworded it first, which is the only evidence a later corpus decision has
// that the profile is drafting in the owner's voice. activities never imports
// ai — the composition root injects the recorder, as it does the unsubscribe
// linker.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// DraftOutcomeRecorder resolves the learning signal a served draft opened,
// inside the send's own transaction so the judgment commits with the message
// or not at all.
//
// Its two answers are deliberately asymmetric, and reading them the same way
// is the mistake this comment exists to prevent:
//
//   - recorded=false with a NIL error is every learning-domain answer: a
//     reference this installation never issued, one whose served text an
//     erasure already removed, one another user owns, one a previous send
//     already decided, or a sender who is not human. None of them blocks the
//     send. A message that legitimately went out must never be refused over a
//     learning signal, and answering "nothing to record" for a row the caller
//     may not touch keeps a foreign reference indistinguishable from an
//     unknown one.
//   - a NON-NIL error is a genuine fault — SQL, connection — and it DOES fail
//     the send. It arrives inside the transaction that already holds the
//     activity and the delivery, and half of that write shape must never
//     commit, so the whole send rolls back with it.
type DraftOutcomeRecorder interface {
	RecordSendOutcomeTx(ctx context.Context, tx pgx.Tx, draftRef, finalBody string) (recorded bool, err error)
}

// WithDraftOutcome wires the voice learning loop's send half onto the send
// path. A store composed without a recorder sends normally and learns nothing:
// the seam is optional wiring, and a deployment that runs no voice profile has
// no signal to close.
func (h Handlers) WithDraftOutcome(recorder DraftOutcomeRecorder) Handlers {
	h.store = h.store.WithDraftOutcome(recorder)
	return h
}

// WithDraftOutcome is the store-level wiring the handler option delegates to:
// the outcome belongs to the send path, which the MCP tool surface enters
// without passing through any handler.
func (s *Store) WithDraftOutcome(recorder DraftOutcomeRecorder) *Store {
	clone := *s
	clone.draftOutcome = recorder
	return &clone
}

// recordDraftOutcome closes the learning signal this send resolves, if it
// resolves one at all. A send composed independently of any draft carries no
// reference and must not cost a query for a row that cannot exist.
//
// Whether a signal was actually recorded is not the send's business: every
// refusal is a learning-domain answer the sender can neither observe nor act
// on, so only a fault propagates.
func (s *Store) recordDraftOutcome(ctx context.Context, tx pgx.Tx, draftRef, finalBody string) error {
	if s.draftOutcome == nil || draftRef == "" {
		return nil
	}
	_, err := s.draftOutcome.RecordSendOutcomeTx(ctx, tx, draftRef, finalBody)
	return err
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The send itself, in the two halves every caller shares: the preparation that
// reads live state and passes the gates, and the transaction that writes.
//
// They are split because a scheduled send fires from a worker and must hold its
// own row's lock across the write (ADR-0104 §3). An immediate send and a
// deferred one therefore run byte-identical writes, rather than two copies of
// them that drift.

import (
	"context"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SendEmail runs the governed send: origin resolution → the guard sequence
// above → deliverability → the outbound activity and its delivery, committed
// together in the write shape.
//
// There is exactly one send. A reply and an account-started message differ
// only in the origin they arrive with (ADR-0087 §1); every invariant below
// the resolution — the authorization order, the consent gate, deliverability,
// identity minting, single-transaction staging — is reached by both.
func (s *Store) SendEmail(ctx context.Context, origin SendOrigin, in SendEmailInput, gate ConsentGate, stager DeliveryStager) (crmcontracts.Activity, error) {
	prepared, err := s.prepareSend(ctx, origin, in, gate, stager)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	var sent crmcontracts.Activity
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		sent, err = s.sendPreparedTx(ctx, tx, origin, prepared, stager)
		return err
	}); err != nil {
		return crmcontracts.Activity{}, err
	}
	return sent, nil
}

// preparedSend is one message with every live read already done and every gate
// already passed — rendered, snapshotted, and ready to be written.
//
// It exists because a scheduled send fires from a worker rather than a request,
// and the fire must hold the scheduled row's lock across the same writes an
// immediate send performs (ADR-0104 §3). Splitting preparation from the write
// lets both callers share this one transaction body instead of growing a second
// copy of it that would drift.
type preparedSend struct {
	in        SendEmailInput
	message   outboundMessage
	messageID string
}

// prepareSend runs everything an outbound send needs BEFORE it writes: origin
// resolution, the guard sequence, the sign-off, deliverability, attachment
// snapshots, markup sanitizing and the sender's display name.
//
// Every one of these is a live read of state that may have changed since a
// scheduled message was composed, which is exactly why a scheduled send runs
// them again at fire rather than trusting what it froze (ADR-0104 §2).
func (s *Store) prepareSend(ctx context.Context, origin SendOrigin, in SendEmailInput, gate ConsentGate, stager DeliveryStager) (preparedSend, error) {
	links, err := origin.resolve(ctx, s)
	if err != nil {
		return preparedSend{}, err
	}
	if err := s.refuseUnsendable(ctx, in, gate, stager); err != nil {
		return preparedSend{}, err
	}

	// The sender's own sign-off, appended by the SERVER rather than written by
	// the model or typed by the rep.
	//
	// Every drafting prompt in this product tells the model not to write one —
	// "the composer adds the sender's own; a name you guessed would go out over
	// the wrong signature" — and until now nothing did, so every message went
	// out unsigned and the instruction described a step that did not exist.
	//
	// Before deliverability, so the signature sits under the message and ABOVE
	// the unsubscribe footer. A sign-off below the legal footer reads as part of
	// it, which is the arrangement every mail client's own "signature before
	// quoted text" setting exists to avoid.
	signed, err := s.signedBody(ctx, in.Body)
	if err != nil {
		return preparedSend{}, err
	}

	// Deliverability is derived here, after the gates, so both transports
	// get it and neither can send marketing mail without it.
	derived, err := s.deliverability(ctx, signed, in.Recipients, in.ConsentPurpose)
	if err != nil {
		return preparedSend{}, err
	}
	messageID := MintMessageID(s.messageIDDomain())

	// The files, resolved to snapshots while the sender's own read gate still
	// applies. It runs before the transaction for the same reason the body work
	// does — the transaction holds writes only — and it refuses the whole send
	// when one file cannot be resolved, because a message carrying fewer files
	// than the sender attached is one nobody is told is wrong.
	files, err := s.resolveAttachments(ctx, in.AttachmentIDs)
	if err != nil {
		return preparedSend{}, err
	}

	// Caller markup is filtered BEFORE anything of ours is added to it, so the
	// signature and the unsubscribe footer below are not themselves subject to
	// a filter they would only ever pass — and so what the allowlist judges is
	// exactly what the caller sent.
	safeHTML, err := SanitizeOutboundHTML(in.HTMLBody)
	if err != nil {
		return preparedSend{}, err
	}

	// The markup alternative gets the SAME sign-off and the same unsubscribe
	// footer, in its own syntax. Two alternatives of one message that disagreed
	// would be two messages, and which one the recipient reads is their client's
	// decision rather than ours — including whether they can unsubscribe.
	htmlBody, err := s.signedHTML(ctx, safeHTML, derived)
	if err != nil {
		return preparedSend{}, err
	}

	// Who the recipient sees this is from. Resolved here, beside the signature
	// and before the transaction, because both answer "who is sending this" and
	// a message whose header and sign-off named different people would be one
	// message telling two stories.
	fromName, err := s.senderDisplayName(ctx)
	if err != nil {
		return preparedSend{}, err
	}

	return preparedSend{
		in:        in,
		messageID: messageID,
		message: outboundMessage{
			in:              in,
			messageID:       messageID,
			fromName:        fromName,
			body:            derived.transmitted,
			recordedBody:    derived.recorded,
			htmlBody:        htmlBody,
			files:           files,
			listUnsubscribe: derived.listUnsubscribe,
			to:              toRecipients(in.Recipients, in.Cc, in.Bcc),
			links:           links,
		},
	}, nil
}

// sendPreparedTx writes one prepared send: the outbound activity, its delivery,
// and the draft outcome, in the CALLER's transaction.
//
// It takes the transaction rather than opening one so a scheduled send can hold
// its own row's lock across these writes and transition it in the same commit
// (ADR-0104 §3). An immediate send passes a transaction it opened itself, so
// both paths produce byte-identical writes and DRAFT-AC-N-7's invariant — one
// activity, one delivery, one job, or none of them — holds for both.
func (s *Store) sendPreparedTx(ctx context.Context, tx pgx.Tx, origin SendOrigin, p preparedSend, stager DeliveryStager) (crmcontracts.Activity, error) {
	// An account-started send names its own addressees, so each must
	// belong to someone this sender can read (ADR-0087 §2). It runs
	// AFTER the consent gate: the gate is the recipients' own answer
	// about being written to at all, and a caller must not learn that a
	// stranger withheld consent by watching which of two refusals a
	// typed address produces.
	if err := s.resolveRecipients(ctx, tx, origin, p.in.Recipients); err != nil {
		return crmcontracts.Activity{}, err
	}
	chain, err := origin.threading(ctx, tx, p.messageID)
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	sent, _, err := logActivityInTx(ctx, tx, p.message.activity(chain))
	if err != nil {
		return crmcontracts.Activity{}, err
	}
	if err := stager.StageTx(ctx, tx, p.message.delivery(ids.UUID(sent.Id), chain)); err != nil {
		return crmcontracts.Activity{}, err
	}
	// in.Body, not message.body: the judgment is about the text the HUMAN
	// approved, and the two differ once a footer is applied. The reason
	// in.Body still holds that text is that deliverability() returns a NEW
	// local and never rewrites in — the transmitted body is that derived
	// local. So this is correct because in is immutable, NOT because of
	// where the footer is applied relative to this call, and moving the
	// transaction boundary does not make it wrong.
	if err := s.recordDraftOutcome(ctx, tx, p.in.DraftRef, p.in.Body); err != nil {
		return crmcontracts.Activity{}, err
	}
	return sent, nil
}

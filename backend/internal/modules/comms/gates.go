// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The gates one dispatch attempt runs, in the order dispatcher.go calls them:
// send authority, attachment carriage, attachment integrity, seat, consent.
//
// Every one of them returns outcomeUndecided to mean "not my business, carry
// on", and each keeps the same split between an ANSWER and a FAULT — a refusal
// parks with a reason a human can act on, while a failure to LEARN the answer
// retries, so an outage never destroys a legitimate send.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// gateSendAuthority refuses a delivery this installation's own knowledge of the
// provider says can never leave, and returns outcomeUndecided when it may.
//
// It reads the PROVIDER's answer about a credential — granted is the scope list
// the resolver just read from the provider, not a copy stored when the grant was
// made — and it applies the scope check only where the provider HAS a scope to
// check. A credential carrying no OAuth grant is its own authority: the resolver
// either produced one or reported that it could not, so demanding a scope of it
// would park every message the provider can actually send, with a reason naming
// a connector limitation that does not exist.
//
// Both refusals PARK. Neither a provider this installation cannot transmit
// through nor a connection the provider never granted the send scope is repaired
// by waiting; the scope one names reconnecting, which is the act that repairs it.
func (d *Dispatcher) gateSendAuthority(ctx context.Context, del Delivery, granted []string) (Outcome, time.Duration, error) {
	switch scope, capability := SendScopeFor(del.Provider); capability {
	case CannotSend:
		return d.park(ctx, del.ID, fmt.Sprintf("provider %q cannot send messages", del.Provider))
	case SendsWithScope:
		if !slices.Contains(granted, scope) {
			return d.park(ctx, del.ID, "this mailbox connection was not granted the send scope; reconnect it to enable sending")
		}
	case SendsWithoutScope:
		// Nothing to intersect: the resolved credential is the whole authority,
		// and the seat gate is what still binds the human who lent it.
	}
	return outcomeUndecided, 0, nil
}

// gateAttachmentCarriage refuses a delivery whose channel cannot carry the files
// it was staged with (ADR-0086/A131 §2).
//
// IT PARKS. It does not strip, it does not convert the files to links, and it
// does not transmit the covering text alone. Stripping is the one behaviour the
// ADR exists to forbid, because the failure is silent: the sender sees a
// timeline entry with an attachment chip — the timeline records what was STAGED
// — the recipient sees a message referring to a file that is not there, and
// nobody is told. The record of what was sent is then permanently wrong, so even
// a later investigation reconstructs the wrong history.
//
// Parking rather than refusing outright is deliberate too: the composer already
// knows the channel's capability and warns before the human presses send, so a
// mismatch HERE means something changed after staging — a channel reconnected as
// a different provider, a file added by a later edit. The human should get the
// message back with a reason, which is what parking does.
//
// It checks FOUR things, not one: whether files may go at all, how many may ride
// in one message, how large each may be, and — for a transport that carries
// text-with-files as a caption — how long the covering text may be. All four are
// the provider's own limits, published on the channel directory so the composer
// warns first, and every one of them parks for the same reason: a message that
// went out missing a file, or truncated to fit a caption, is not the message a
// human approved.
//
// The reason names the channel and the files, because "this could not be sent"
// with no subject leaves a person guessing which of the two to fix.
func (d *Dispatcher) gateAttachmentCarriage(ctx context.Context, del Delivery, seam sendSeam) (Outcome, time.Duration, error) {
	if len(del.Attachments) == 0 {
		return outcomeUndecided, 0, nil
	}
	if reason := carriageRefusal(del, seam.carriage); reason != "" {
		return d.park(ctx, del.ID, reason)
	}
	return outcomeUndecided, 0, nil
}

// carriageRefusal is why this message may not go out as staged, or "" when it
// may. ONE function so the four refusals read together and none can be added
// without a reason a person can act on.
//
// A zero bound means "no limit beyond the contract's own", never "zero allowed"
// — the only field that says nothing may go is Carries. A connector that
// declares carriage without naming a limit is therefore held to the contract's
// own caps rather than parked on every send.
func carriageRefusal(del Delivery, carriage connector.Carriage) string {
	if !carriage.Carries {
		return fmt.Sprintf(
			"the %s channel cannot carry files, and this message has %s attached; it was not sent, "+
				"because sending the text alone would misrepresent what it contains",
			del.Provider, strings.Join(attachmentNames(del), ", "))
	}
	if carriage.MaxFiles > 0 && len(del.Attachments) > carriage.MaxFiles {
		return fmt.Sprintf(
			"the %s channel carries at most %d file(s) in one message and this has %d; it was not sent, "+
				"because sending some of them would misrepresent what it contains — send them as separate messages",
			del.Provider, carriage.MaxFiles, len(del.Attachments))
	}
	for _, file := range del.Attachments {
		if carriage.MaxBytesPerFile > 0 && file.ByteSize > carriage.MaxBytesPerFile {
			return fmt.Sprintf(
				"%q is larger than the %d MiB the %s channel accepts for one file; it was not sent — "+
					"share it another way, or send a smaller version",
				file.Filename, carriage.MaxBytesPerFile>>20, del.Provider)
		}
	}
	if body := utf8.RuneCountInString(del.Body); carriage.MaxBodyWithFiles > 0 && body > carriage.MaxBodyWithFiles {
		return fmt.Sprintf(
			"the %s channel carries the text of a message with files as a caption, which holds at most "+
				"%d characters, and this message has %d; it was not sent, because neither shortening it nor "+
				"sending it as two messages would be what you wrote — shorten it and send again",
			del.Provider, carriage.MaxBodyWithFiles, body)
	}
	return ""
}

// attachmentNames is the staged filenames, for a reason a person can act on:
// "this could not be sent" with no subject leaves them guessing which file.
func attachmentNames(del Delivery) []string {
	names := make([]string, 0, len(del.Attachments))
	for _, file := range del.Attachments {
		names = append(names, file.Filename)
	}
	return names
}

// gateAttachmentIntegrity refuses a delivery carrying a file that may no longer
// be sent — one archived since staging, or one the sender has since lost the
// right to read (ADR-0086/A131 §3).
//
// The staging check is NOT this check. It answered about the moment the human
// pressed send, and a delivery sits on a retry ladder for as long as the maximum
// age allows; a grant withdrawn in between is precisely the case this exists to
// catch, and the message would carry the sender's own address out with a file
// they can no longer read.
//
// It PARKS rather than retries, and it does not strip the file and send the
// rest, for the reason the carriage gate above spells out: a message whose
// recipient sees fewer files than the timeline records is a permanently wrong
// record that nobody is told about. Parking hands the message back with the
// reason, which is the only outcome that leaves a human able to act.
//
// A missing authority parks, exactly as a missing seat authority does. This lane
// reaches a real external mailbox, so an unwired gate is a deployment defect
// that must fail closed rather than wave every attachment through.
func (d *Dispatcher) gateAttachmentIntegrity(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	if len(del.Attachments) == 0 {
		return outcomeUndecided, 0, nil
	}
	if d.attachments == nil {
		return d.park(ctx, del.ID, "no attachment authority is configured on this send path")
	}
	attachmentIDs := make([]ids.UUID, 0, len(del.Attachments))
	for _, file := range del.Attachments {
		attachmentIDs = append(attachmentIDs, file.AttachmentID)
	}
	ok, reason, err := d.attachments.EnsureTransmittable(ctx, del.UserID, attachmentIDs)
	if err != nil {
		return d.retry(ctx, del.ID, err)
	}
	if !ok {
		return d.park(ctx, del.ID, reason)
	}
	return outcomeUndecided, 0, nil
}

// gateSeat refuses a delivery whose sender is no longer a live,
// mutation-capable seat, and returns outcomeUndecided when they are.
//
// It PARKS rather than retries, because both an off-boarding and a downgrade
// to a read seat are answers: the authority that staged this message is gone
// either way, and no amount of waiting restores it. Retrying would keep the
// batch alive for the whole maximum age, which is the exposure this gate
// closes. A seat authority that could not ANSWER is the opposite case and
// retries, so an identity-store outage does not destroy every send in flight.
func (d *Dispatcher) gateSeat(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	if d.seats == nil {
		// A send path with no seat authority wired is a deployment defect, and
		// this lane reaches a real external mailbox. Fail closed, exactly as
		// the missing consent authority below does.
		return d.park(ctx, del.ID, "no seat authority is configured on this send path")
	}
	active, reason, err := d.seats.ActiveSeat(ctx, del.UserID)
	if err != nil {
		return d.retry(ctx, del.ID, err)
	}
	if !active {
		return d.park(ctx, del.ID, reason)
	}
	return outcomeUndecided, 0, nil
}

// gateConsent asks the authoritative suppression question and returns
// outcomeUndecided when every addressee may still be mailed.
func (d *Dispatcher) gateConsent(ctx context.Context, del Delivery) (Outcome, time.Duration, error) {
	if d.consent == nil {
		// A send path with no consent authority wired is a deployment defect.
		// Retrying would hide the misconfiguration behind a delivery that
		// quietly never goes out.
		return d.park(ctx, del.ID, "no consent authority is configured on this send path")
	}
	// EVERY subject this delivery reaches is asked about, not just the To line:
	// a Cc'd person is owed the same suppression, and this call is the only one
	// that runs after they could have withdrawn. consentRecipients is what makes
	// the question shape-agnostic — mail's addressees and a channel's single
	// recipient arrive here as the same list.
	switch err := d.consent.RequireGrantedForRecipients(ctx, consentRecipients(del), del.ConsentPurpose); {
	case errors.Is(err, apperrors.ErrConsentNotGranted):
		// An answer: consent is absent, and no amount of waiting brings it
		// back.
		return d.park(ctx, del.ID, fmt.Sprintf(
			"consent for purpose %q is not granted for these recipients", del.ConsentPurpose,
		))
	case err != nil:
		// NOT an answer. A consent service that is merely down must not
		// permanently destroy a consented send — getting this branch backwards
		// silently kills legitimate mail.
		return d.retry(ctx, del.ID, err)
	}
	return outcomeUndecided, 0, nil
}

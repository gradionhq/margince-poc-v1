// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// The dispatcher's ONE branch on provider class (telegram-oa design §8.3), and
// the at-most-once guard that branch turns on (§8.4).
//
// Everything about a delivery that depends on its SHAPE is settled here and
// nowhere else: which credential authorizes it, which provider call transmits
// it, and whether a retry of that call could ever discover that an earlier
// attempt already went. Past this file the authority gate, the seat gate, the
// consent gate, the pacing chain, the retry ladder and the four dispositions are
// one code path for mail and for a messaging channel alike.
//
// Keeping it to one branch is not tidiness. A second branch downstream would be
// two send paths wearing one name, and the one exercised less — the channel, by
// a wide margin — is the one that would quietly stop matching the rules the mail
// path keeps.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// sendSeam is one resolved credential together with the provider call it
// authorizes, in the shape the delivery's own row declares.
type sendSeam struct {
	// granted is the scope list the PROVIDER says this credential holds, which
	// the authority gate intersects against SendScopeFor. NIL for a channel
	// credential — a bot token carries no OAuth grant, which is exactly what
	// SendsWithoutScope means, so there is nothing to intersect.
	granted []string

	// carriage is what the resolved connector says its provider can carry. The
	// ZERO value for a connector that does not implement
	// connector.AttachmentCarrier — carries nothing — which is the whole point of
	// the seam having no default: an adapter that never declared carriage cannot
	// be mistaken for capable, and a staged message with files parks instead of
	// going out stripped (ADR-0086/A131).
	//
	// A descriptor rather than a bool because the provider's real limits — how
	// many files, how large, and how long the covering text may be when files
	// ride with it — are what the carriage gate checks.
	carriage connector.Carriage

	// transmit hands the delivery to the provider, already bound to the resolved
	// credential and to the row it was built from. One call for either
	// transport is what lets the receipt handling, the metering and the failure
	// classification below stay shape-blind.
	//
	// The FILES are a parameter rather than something the closure reads for
	// itself, and that is the at-most-once contract rather than a style choice:
	// the dispatcher resolves them BEFORE it commits the in-flight marker, so a
	// blobstore fault or an over-size refusal cannot leave a message that never
	// reached the provider parked as one whose outcome nobody learned.
	transmit func(ctx context.Context, files []connector.OutboundFile) (connector.SendReceipt, error)

	// detectsPriorSend reports whether a RETRY of this seam could discover that
	// an earlier attempt already put the message on the wire.
	//
	// Mail can: the RFC822 identity the message was staged under is searchable
	// at the provider, so a mail delivery rides the retry ladder as it always
	// has. Telegram's sendMessage has neither an idempotency key nor a
	// prior-send lookup, so no later attempt could ever tell — and such a seam
	// instead records that a transmission is in flight BEFORE the call, and
	// refuses to retry an outcome it never learned.
	detectsPriorSend bool
}

// resolveSeam resolves the delivery's transmitting credential and binds the
// provider call for it. THIS is the branch on provider class, and it reads the
// ROW's shape discriminator rather than the provider name: the schema guarantees
// a row is mail-shaped or channel-shaped and never half of each, so a channel
// delivery cannot be rendered as mail even if a provider were mis-registered.
func (d *Dispatcher) resolveSeam(ctx context.Context, del Delivery) (sendSeam, error) {
	if del.IsChannel() {
		sender, auth, err := d.resolver.ResolveChannel(ctx, del.UserID, del.Provider)
		if err != nil {
			return sendSeam{}, err
		}
		return sendSeam{carriage: connector.CarriageOf(sender), transmit: func(ctx context.Context, files []connector.OutboundFile) (connector.SendReceipt, error) {
			return sender.SendMessage(ctx, auth, connector.ChannelMessage{
				// The provider plus the account id ARE the recipient key. The
				// username is deliberately absent: a handle can be released and
				// re-claimed, so nothing may route on it.
				Recipient: connector.ChannelIdentity{
					Provider:      del.Provider,
					ChannelUserID: del.ChannelRecipient(),
				},
				Body:    del.Body,
				ReplyTo: del.InReplyTo,
				// The delivery's own id is the idempotency anchor: minted per
				// send, never reused, and durable — which is what the seam asks
				// for. Telegram cannot honour it, hence the in-flight marker
				// below, but a provider that grows an idempotency key gets one
				// that was already stable.
				IdempotencyKey: del.ID.String(),
				Attempt:        transmissionsBefore(del),
				// The set the carriage gate already cleared, under the same
				// all-or-nothing obligation the mail seam carries.
				Files: files,
			})
		}}, nil
	}
	sender, auth, granted, err := d.resolver.Resolve(ctx, del.UserID, del.Provider)
	if err != nil {
		return sendSeam{}, err
	}
	return sendSeam{
		granted:          granted,
		detectsPriorSend: true,
		carriage:         connector.CarriageOf(sender),
		transmit: func(ctx context.Context, files []connector.OutboundFile) (connector.SendReceipt, error) {
			// Every staged field travels: a retry must rebuild an identical
			// message, and a field dropped here is a header silently missing
			// from real mail.
			return sender.SendEmail(ctx, auth, connector.EmailMessage{
				To: del.Recipients, Cc: del.Cc, Bcc: del.Bcc,
				Subject: del.Subject, Body: del.Body, HTMLBody: del.HTMLBody,
				FromName:            del.FromName,
				MessageID:           del.MessageID,
				InReplyTo:           del.InReplyTo,
				References:          del.References,
				ListUnsubscribe:     del.ListUnsubscribe,
				ListUnsubscribePost: rfc8058Post(del.ListUnsubscribe),
				Attempt:             transmissionsBefore(del),
				// The set the carriage gate already cleared. Reaching here with
				// files means the connector declared it carries them, so the
				// adapter's obligation is to transmit ALL of them or fail —
				// never a subset, never as links (ADR-0086/A131).
				Files: files,
			})
		},
	}, nil
}

// transmissionsBefore is how many attempts this delivery already made, which is
// what Attempt means on both seams. Load counted the CURRENT attempt before the
// dispatcher reached here, so a first transmission reports zero and a
// connector's prior-send lookup fires only on a real retry.
func transmissionsBefore(del Delivery) int { return max(del.Attempts-1, 0) }

// unknownOutcomeReason is what a delivery whose outcome the provider never
// reported records. It is written for the human who has to decide what happens
// next, because nothing automatic can decide it for them: the message may have
// arrived, and only the conversation itself can say.
const unknownOutcomeReason = "the provider never confirmed whether this message was delivered, " +
	"and it will not be retried: a second attempt could deliver it twice with nothing able to tell. " +
	"Check the conversation and send again if it did not arrive"

// unreachableRecipientReason is what a delivery the provider permanently refuses
// to address records. It names the RECIPIENT because that is the true cause, and
// it says what does not help: the two actions an operator would otherwise reach
// for — retry, and reconnect the channel — are both wasted here.
const unreachableRecipientReason = "the messaging provider will not deliver to this recipient: " +
	"they blocked the sender, or their account no longer exists. " +
	"Retrying and reconnecting the channel both change nothing — reach them another way"

// filesNotCarriedReason is what a delivery refused BELOW the carriage gate
// records — the connector honouring its own bounds when its declaration and its
// send path disagree, or the read seam refusing an aggregate no declared bound
// describes (#2047).
//
// It says what does not help, for the same reason the recipient reason does: the
// refusal is deterministic, so every retry produces it again, and the ladder
// would spend its whole length re-reading the files before saying anything.
//
// It deliberately does NOT prescribe a remedy, and that is a correction rather
// than vagueness. The gate above names one per case — the file, the limit, the
// character count — because it knows which bound was missed. Down here the cause
// could be a total nobody published or a file with no bytes at all, and "send
// fewer or smaller files" is advice nobody can follow for a file that is already
// as small as it gets. The cause is logged where the reason cannot carry it.
const filesNotCarriedReason = "this channel refused to carry the files this message was staged with, " +
	"and it was not sent: sending the text without them would misrepresent what it contains. " +
	"Retrying changes nothing — check the files against what this channel accepts, or share them another way"

// guardAtMostOnce protects the seams whose retries cannot detect a prior send,
// and returns outcomeUndecided for the ones that can — mail resolves through
// here untouched.
//
// It does the two things §8.4 requires, in this order and no other. A delivery
// that ALREADY carries the marker had an earlier attempt reach the provider with
// its outcome never recorded — a crash, a killed worker, a cancelled job — and
// nothing can ask the provider what became of that message, so it parks. A
// delivery that does not carries one from now on, committed before the call
// rather than after it, which is what makes the crash case visible at all.
func (d *Dispatcher) guardAtMostOnce(ctx context.Context, del Delivery, seam sendSeam) (Outcome, time.Duration, error) {
	if seam.detectsPriorSend {
		return outcomeUndecided, 0, nil
	}
	if del.InFlightAt != nil {
		return d.park(ctx, del.ID, unknownOutcomeReason)
	}
	if err := d.store.MarkInFlight(ctx, del.ID); err != nil {
		if errors.Is(err, ErrTerminal) {
			return OutcomeSkipped, 0, nil
		}
		// Nothing was transmitted, and the marker is absent precisely because
		// this write failed, so the ladder may safely try the whole attempt
		// again — and the row says so, like every other pre-transmit fault.
		// The failure note may of course fail for whatever reason the marker
		// did; retry then hands back both causes and the disposition is the one
		// this line would have returned anyway.
		return d.retry(ctx, del.ID, fmt.Errorf("comms: marking the transmission in flight: %w", err))
	}
	return outcomeUndecided, 0, nil
}

// outboundFiles renders the staged snapshot into the provider-neutral shape.
//
// The BYTES are absent here and fetched by the transmit path, because the
// snapshot is the record of what was attached and not a copy of the files
// themselves: keeping megabytes on a delivery row would make every retry
// re-read them and every audit carry them.
// attachedFiles pairs the staged snapshot with the bytes the object store
// holds, which is what the connector needs to build a part.
//
// The snapshot supplies the filename and the content type — what the message
// SAID it carried at the moment a human sent it — and the store supplies what
// is actually in the file. Reading the metadata again here instead would let a
// rename between staging and transmit change the name on a message already
// approved.
func (d *Dispatcher) attachedFiles(ctx context.Context, del Delivery) ([]connector.OutboundFile, error) {
	if len(del.Attachments) == 0 {
		return nil, nil
	}
	if d.attachments == nil {
		// The integrity gate has already parked a delivery with files and no
		// authority, so this is unreachable rather than tolerated — and saying
		// so is better than a nil-deref if that ordering ever changes.
		return nil, errors.New("comms: no attachment authority is configured on this send path")
	}
	attachmentIDs := make([]ids.UUID, 0, len(del.Attachments))
	for _, file := range del.Attachments {
		attachmentIDs = append(attachmentIDs, file.AttachmentID)
	}
	bodies, err := d.attachments.ReadForSend(ctx, del.UserID, attachmentIDs)
	if err != nil {
		return nil, err
	}
	if len(bodies) != len(del.Attachments) {
		return nil, fmt.Errorf(
			"comms: the store returned %d files for a message carrying %d",
			len(bodies), len(del.Attachments))
	}
	out := make([]connector.OutboundFile, 0, len(del.Attachments))
	for i, file := range del.Attachments {
		out = append(out, connector.OutboundFile{
			AttachmentID: file.AttachmentID.String(),
			Filename:     file.Filename,
			ContentType:  file.ContentType,
			ByteSize:     file.ByteSize,
			Checksum:     file.Checksum,
			Body:         bodies[i],
		})
	}
	return out, nil
}

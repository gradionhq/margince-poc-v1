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

	// transmit hands the delivery to the provider, already bound to the resolved
	// credential and to the row it was built from. One call for either
	// transport is what lets the receipt handling, the metering and the failure
	// classification below stay shape-blind.
	transmit func(ctx context.Context) (connector.SendReceipt, error)

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
		sender, auth, err := d.resolver.ResolveChannel(ctx, del.Provider)
		if err != nil {
			return sendSeam{}, err
		}
		return sendSeam{transmit: func(ctx context.Context) (connector.SendReceipt, error) {
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
		transmit: func(ctx context.Context) (connector.SendReceipt, error) {
			// Every staged field travels: a retry must rebuild an identical
			// message, and a field dropped here is a header silently missing
			// from real mail.
			return sender.SendEmail(ctx, auth, connector.EmailMessage{
				To: del.Recipients, Cc: del.Cc,
				Subject: del.Subject, Body: del.Body,
				MessageID:           del.MessageID,
				InReplyTo:           del.InReplyTo,
				References:          del.References,
				ListUnsubscribe:     del.ListUnsubscribe,
				ListUnsubscribePost: rfc8058Post(del.ListUnsubscribe),
				Attempt:             transmissionsBefore(del),
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
		// again.
		return OutcomeRetry, 0, fmt.Errorf("comms: marking the transmission in flight: %w", err)
	}
	return outcomeUndecided, 0, nil
}

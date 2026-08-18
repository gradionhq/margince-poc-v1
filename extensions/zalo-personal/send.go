// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The transport half of this unit: replying, from the CRM, into a conversation
// on the member's own personal account (ADR-0107/A158, DESIGN §5).
//
// WHOSE CREDENTIAL TRANSMITS is not a choice here — this unit has exactly one
// kind of credential and it belongs to a person. A reply leaves as the member
// who staged it, on the session they scanned in themselves, and there is no
// installation credential to fall back to. That is also why Live exists: the
// core must be able to ask "can this member still send" without spending a
// session to find out.
//
// THIS UNIT NEVER OPENS A CONVERSATION. A reply is staged in the ordinary
// timeline reply box, against a person who wrote first; the core checks the
// seat, the permissions and the consent, and only then is Send called.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// maxSendRunes bounds what this unit will transmit.
//
// IT IS THIS CONNECTOR'S GUARD, NOT ZALO'S LIMIT, and saying so matters: no
// message-length cap has been measured on the personal protocol, so a tight
// guess here would refuse messages a rep can legitimately send. It is set far
// above any plausible chat message and far below a pasted document, which is
// the accident it defends against — a whole page reaching a provider whose
// refusals carry no hint about which parameter was the problem. If Zalo's own
// cap turns out to be lower, Zalo's refusal is a definite answer the core
// already reads correctly.
const maxSendRunes = 10000

// session is the part of a resumed Zalo session this unit uses. It is an
// interface so the handlers can be exercised without a Zalo — not to abstract
// over two implementations, of which there is one, but because the alternative
// is a transmission seam proven only in production.
type session interface {
	// UID is which account this session belongs to, as the provider states it.
	UID() string
	// SendText posts one plain-text message to a 1:1 thread.
	SendText(ctx context.Context, toUID, body string) (zaloReceipt, error)
}

// resumeFunc turns a sealed credential into a working session.
type resumeFunc func(ctx context.Context, sealed zaloSealed) (session, error)

// resumeSession is the production one.
//
// The options are EMPTY on purpose: the user agent and the device id that
// identify this session travel inside the sealed document, and supplying a
// second copy here would let the two disagree — at which point Zalo sees a
// different device and the session stops being the one the member scanned in.
func resumeSession(ctx context.Context, sealed zaloSealed) (session, error) {
	resumed, err := zaloResume(ctx, sealed, zaloOptions{})
	if err != nil {
		// Returned explicitly rather than as (resumed, err): a typed nil in an
		// interface is non-nil to every caller that checks it.
		return nil, err
	}
	return resumed, nil
}

// send transmits one message on the member's own session.
func send(ctx context.Context, rt extension.Runtime, msg extension.OutboundMessage) (extension.Receipt, error) {
	return sendVia(ctx, rt, msg, resumeSession)
}

func sendVia(ctx context.Context, rt extension.Runtime, msg extension.OutboundMessage,
	resume resumeFunc,
) (extension.Receipt, error) {
	body := strings.TrimSpace(msg.Body)
	switch {
	case body == "":
		return extension.Receipt{}, fmt.Errorf("%w: an empty message has nothing to deliver", extension.ErrInvalid)
	case len([]rune(body)) > maxSendRunes:
		return extension.Receipt{}, fmt.Errorf("%w: this message is %d characters, over the %d this connector will transmit — shorten it, or send it as a document instead", extension.ErrInvalid, len([]rune(body)), maxSendRunes)
	}
	// The recipient is the provider's ACCOUNT ID and never a display name: a
	// name is re-assignable, so routing on the readable one delivers to whoever
	// holds it today.
	recipient := msg.Recipient.ChannelUserID
	if recipient == "" {
		return extension.Receipt{}, fmt.Errorf("%w: this delivery names no Zalo account to send to", extension.ErrInvalid)
	}
	sealed, err := unsealSession(ctx, rt, msg.Member)
	if err != nil {
		return extension.Receipt{}, err
	}
	resumed, err := resume(ctx, sealed)
	if err != nil {
		// A handshake that failed transmitted NOTHING, whatever went wrong, so
		// it is an ordinary refusal the core may retry — never the
		// unknown-outcome class.
		return extension.Receipt{}, fmt.Errorf("zalo-personal: this member's session could not be resumed: %w", err)
	}
	receipt, err := resumed.SendText(ctx, recipient, body)
	if err != nil {
		return extension.Receipt{}, transmissionRefusal(err)
	}
	// The provider's own id for the sent message, which is what makes a later
	// reply anchorable — and the id the member's own echo will carry back when
	// capture lands, so the connector can tell its own send from an inbound.
	return extension.Receipt{ProviderMessageID: receipt.MsgID}, nil
}

// live answers whether this member can still send, FROM THE ROW.
//
// Not by resuming the session, and the difference is not an optimisation: the
// core's pre-flight runs several times per delivery, and a personal Zalo
// session is a scarce, human-recovered thing — re-handshaking on every check
// would spend the credential to answer a question the row already holds, and a
// session evicted by that traffic costs the member a QR re-scan with their
// phone.
//
// A confirmed "no" answers FALSE and the delivery parks where a human can see
// it; an inability to tell returns an ERROR and the delivery is retried.
// Collapsing the two would either strand a deliverable message or re-send a
// refused one.
func live(ctx context.Context, rt extension.Runtime, member extension.UserID) (bool, error) {
	var found *connection
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		found, err = connectionOf(ctx, tx, string(member))
		return err
	}); err != nil {
		return false, err
	}
	// No row is a confirmed no, not a fault: the member never connected, or
	// disconnected, and there is nothing to retry into. A row that is
	// `disconnected` or `needs_reconnect` is the same answer for a reason the
	// screen already states to them.
	return found != nil && found.Status == statusConnected, nil
}

// unsealSession reads back the member's own sealed credential.
//
// A member with nothing on deposit is ErrForbidden rather than a transport
// failure: they withdrew their account, or never connected it, and no number of
// retries produces a credential. It is the one PERMANENT refusal the pre-flight
// half of this file has.
func unsealSession(ctx context.Context, rt extension.Runtime, member extension.UserID) (zaloSealed, error) {
	var sealed zaloSealed
	raw, err := rt.Secrets().GetUser(ctx, member, sessionKey)
	if err != nil {
		if errors.Is(err, extension.ErrSecretNotFound) {
			return sealed, fmt.Errorf("%w: this member has no Zalo session on deposit, so nothing can be sent as them", extension.ErrForbidden)
		}
		return sealed, err
	}
	if err := json.Unmarshal(raw, &sealed); err != nil {
		// Nothing later decodes it either, and a retry would spend the ladder
		// discovering that.
		return sealed, fmt.Errorf("%w: the sealed session is not the shape this unit wrote — the member must connect again", extension.ErrForbidden)
	}
	return sealed, nil
}

// transmissionRefusal maps a failure of the CALL THAT TRANSMITS, which carries
// one class nothing before it can: the message may already be at the recipient.
//
// This provider honours no idempotency key and offers no prior-send lookup, so
// a request whose answer never came back is a question no later attempt can
// settle. The core retries every refusal it is not told is unanswerable, so
// reporting one as an ordinary transport failure sends the rep's message twice
// — the one failure a human cannot undo. ErrSendOutcomeUnknown stops the
// delivery instead and leaves the uncertainty on the record.
//
// EVERYTHING ELSE IS AN ANSWER and stays an ordinary error: a refusal Zalo
// actually sent, a rejected recipient, a body it would not take. Those are
// definite, and the core is right to treat them as proof nothing was
// transmitted. This is the ONE function that draws the line, so the line is
// testable on its own.
func transmissionRefusal(err error) error {
	if errors.Is(err, errUnanswered) {
		return fmt.Errorf("%w: %s", extension.ErrSendOutcomeUnknown, err.Error())
	}
	return err
}

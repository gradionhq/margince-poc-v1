// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The two VALUES the protocol layer hands the unit layer: one drained message,
// and one roster entry. Nothing here speaks the wire — the listener that opens
// the socket, requests the backlog and decodes the four payload encodings owns
// the behaviour, and this file owns only the shape it answers in.
//
// They are split out because the seam between the layers is the thing worth
// being able to read on its own. Everything above it (the fairness order, the
// three filters, the record mapping, the cursor) is driven end to end in tests
// against these two structs, so the unit layer is proven without a socket.

import "time"

// zaloInbound is ONE message as the drain read it off the socket.
//
// UIDFrom AND IDTo BOTH MATTER, and a reader who takes only one of them
// introduces the defect this unit's filters exist to prevent. Verified against a
// real capture: this member's OWN send comes back as an ordinary inbound frame
// carrying the SAME msgId, with `uidFrom: "0"` and the counterparty in `idTo`;
// a message somebody sent them has the counterparty in `uidFrom` and
// `idTo: "0"`. So direction is read from the pair, never from the id.
type zaloInbound struct {
	// MsgID is the provider's own identifier for this message. It is the half of
	// the natural key this unit supplies and the value the cursor holds, so it
	// must be what the provider reports identically on a re-read.
	MsgID string
	// UIDFrom is the sender's Zalo account id, or "0" when the sender is this
	// member — the echo of their own send.
	UIDFrom string
	// IDTo is the recipient's Zalo account id, or "0" when the recipient is this
	// member.
	IDTo string
	// DName is what the provider says the sender calls themselves. Untrusted
	// remote text: it names a person on a screen and routes nothing.
	DName string
	// MsgType is the provider's own kind for the message body (`webchat` for
	// plain chat text).
	MsgType string
	// Content is the message as a human reads it.
	Content string
	// OccurredAt is when the message happened AT ZALO, decoded from the frame's
	// own `ts`. A zero value is refused by the capture seam rather than
	// defaulted, because a timeline ordered by when a poll noticed is a timeline
	// of this system's own scheduling.
	OccurredAt time.Time
	// Raw is the provider's frame as received, kept as evidence — the original
	// document rather than a re-encoding of the fields above.
	Raw []byte
}

// selfSent reports that this frame is this member's OWN outbound message coming
// back, which the protocol delivers as an ordinary inbound frame.
//
// It is a DIRECTION test and it cannot be a dedupe-on-id job: the echo carries
// the same msgId the send returned, so an id-based check would either drop the
// member's real messages or keep the echo depending on which arrived first.
// Without this test every rep reply lands a second time on the customer's
// timeline, attributed inbound — the customer appearing to say what the rep
// said.
func (f zaloInbound) selfSent() bool { return f.UIDFrom == selfUID }

// counterparty is the OTHER end of this conversation: the sender for a message
// this member received, and the recipient for one they sent.
func (f zaloInbound) counterparty() string {
	if f.selfSent() {
		return f.IDTo
	}
	return f.UIDFrom
}

// selfUID is how the protocol spells "this member" in either direction field.
// Zalo does not repeat the account's own id in a frame delivered to it, so "0"
// is the whole of the signal.
const selfUID = "0"

// zaloFriend is ONE entry of the member's own Zalo roster, and it carries THREE
// FIELDS BECAUSE THE STRUCT IS THE ENFORCEMENT.
//
// The provider's roster row also reports a date of birth and presence telemetry
// — when the person was last active, and on which device. Capturing a contact's
// presence history into the CRM because they happen to be on a rep's personal
// roster is precisely what the verdict list exists to prevent, so those fields
// are dropped at the wire rather than mapped and ignored: a field this type does
// not have is a field no later change accidentally stores.
//
// The roster is ENRICHMENT ONLY. A first-time prospect is by definition not on
// it, and a drained frame already carries the account id and display name a
// record needs — so a roster call that fails degrades to what the frames say and
// never fails a tick or a screen.
type zaloFriend struct {
	// UserID is the counterparty's Zalo account id, the same value a frame's
	// UIDFrom carries and the key a verdict is stored under.
	UserID string
	// DisplayName is what the provider says they call themselves. Untrusted
	// remote text.
	DisplayName string
	// Avatar is the picture the provider reports, for a member's own screen. It
	// identifies nobody.
	Avatar string
}

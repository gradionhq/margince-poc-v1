// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// One send's derived facts, and the two rows they become.
//
// Apart from the send decision itself because they answer a different question:
// SendEmail decides WHETHER a message may go, and this decides what the
// timeline and the delivery each receive once it may. The split matters most
// where the two rows deliberately DIFFER — the wire body carries a live
// unsubscribe token and the recorded one carries a redacted copy, and having
// both projections in one place is what keeps that difference deliberate.

import "github.com/gradionhq/margince/backend/internal/shared/kernel/ids"

// outboundMessage is one send's derived facts, computed before the
// transaction opens so the transaction holds writes only. The timeline row and
// the delivery are two renderings of THIS value, which is why they are built
// side by side: a field that disagreed between them would be a message whose
// record and whose transmission say different things.
type outboundMessage struct {
	in        SendEmailInput
	messageID string
	// body is what the recipient receives; recordedBody is what the
	// workspace keeps. They differ by exactly one thing — the live
	// preference token the footer carries — because the timeline row is
	// served back to any seat holding activity:read, and that token is a
	// bearer credential over the recipient's consent record (see
	// redactedToken). Only the delivery may read body.
	body         string
	recordedBody string
	// htmlBody is the markup alternative, empty for a plain-text send. It
	// carries the SAME sign-off and the same unsubscribe footer as body: two
	// alternatives of one message that disagreed would be two messages, and the
	// recipient's client decides which one they read.
	htmlBody        string
	listUnsubscribe string
	to              []string
	links           []ActivityLinkInput
}

// htmlAlternativeNote marks a timeline row whose message also went out as
// markup. A fixed sentence rather than the markup itself: the row is served to
// every seat holding activity:read and rendering a sender's HTML there would
// put caller-supplied markup into our own document.
const htmlAlternativeNote = "This message was also sent as HTML."

// activity is the timeline row the send commits.
func (m outboundMessage) activity(chain threading) LogActivityInput {
	direction, sourceSystem := "outbound", SendProvider
	recorded := m.recordedBody
	if m.htmlBody != "" {
		// The timeline keeps the PLAIN alternative, and says so when a markup
		// one also went out.
		//
		// The two parts are supplied independently by the caller, so nothing
		// makes them say the same thing — a recipient whose client renders
		// markup can read something the plain part never mentioned. Every
		// activity:read on this row would otherwise show only the benign half
		// and give no sign the other exists, which is worse than showing markup
		// nobody can read: it is a record that looks complete.
		recorded += "\n\n[" + htmlAlternativeNote + "]"
	}
	return LogActivityInput{
		Kind:         "email",
		Subject:      &m.in.Subject,
		Body:         &recorded,
		Direction:    &direction,
		Links:        m.links,
		Source:       sourceManual,
		SourceSystem: &sourceSystem,
		SourceID:     &m.messageID,
		ThreadKey:    chain.threadKey,
		// This row IS the sent copy — its natural key is the one the provider's
		// echo carries, so the echo's upsert will find it and write nothing.
		// The correspondence evidence the echo used to bring therefore has to
		// be written here or it is never written at all (ADR-0072 §1: an
		// outbound activity to an address is what makes it
		// correspondence-positive).
		CounterpartyEmail:            primaryCounterparty(m.to, m.in.Recipients),
		CounterpartyOutboundAttested: true,
	}
}

// delivery is the same message as the delivery machinery receives it.
func (m outboundMessage) delivery(activityID ids.UUID, chain threading) DeliveryRequest {
	return DeliveryRequest{
		ActivityID:      ids.From[ids.ActivityKind](activityID),
		Provider:        SendProvider,
		MessageID:       m.messageID,
		Recipients:      m.to,
		Cc:              m.in.Cc,
		Subject:         m.in.Subject,
		Body:            m.body,
		HTMLBody:        m.htmlBody,
		ConsentPurpose:  m.in.ConsentPurpose,
		InReplyTo:       chain.inReplyTo,
		References:      chain.references,
		ThreadKey:       chain.threadKey,
		ListUnsubscribe: m.listUnsubscribe,
	}
}

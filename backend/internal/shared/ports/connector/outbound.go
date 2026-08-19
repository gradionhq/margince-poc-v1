// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// The OUTBOUND half of the connector seam: what a provider needs to transmit a
// message as the connected user, and what it reports back.
//
// Apart from the capture side because the two are optional in different ways —
// a capture-only provider implements none of this, and the frozen Connector
// interface names none of it — and because the message shape below is the one
// place the wire format of an outbound mail is described.

import (
	"context"
	"errors"
	"strings"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// EmailSender is the OPTIONAL outbound seam a connector implements when its
// provider can transmit a message as the connected user. Type-asserted like
// Watcher and Backfiller, so the frozen Connector interface is unchanged and a
// capture-only provider simply does not implement it.
//
// SendEmail MUST be idempotent on msg.MessageID. Job delivery is at-least-once,
// so a provider that retransmits on a retry mails the recipient twice; a
// connector whose provider can look up a prior send by RFC822 Message-ID must
// do so whenever msg.Attempt > 0 and return the existing receipt instead.
//
// That obligation has a precondition, and an implementation MUST refuse a
// message that fails it (EmailMessage.Validate) before any provider I/O: an
// identity the prior-send lookup cannot search for makes the idempotency
// guarantee unkeepable, and transmitting anyway is the double-send this seam
// exists to prevent.
type EmailSender interface {
	SendEmail(ctx context.Context, auth Auth, msg EmailMessage) (SendReceipt, error)
}

// AttachmentCarrier is how a sending connector declares whether it can transmit
// files (ADR-0086/A131).
//
// THERE IS NO DEFAULT, and that is the whole design. The obvious way to add
// attachments — put a files field on the message, teach the mail adapter
// multipart, let the others ignore it — compiles everywhere and silently
// transmits the covering text without the file. The sender sees a timeline entry
// with an attachment chip, because the timeline records what was STAGED; the
// recipient sees a message referring to a file that is not there; nobody is
// told. That failure is silent, invisible at the call site, and permanent,
// because the record of what was sent is now wrong.
//
// So a sender that does not implement this seam is treated as carrying nothing,
// and a staged message with files PARKS rather than going out stripped. An
// adapter that gains the ability declares it here; one that never had it needs
// no change and cannot be mistaken for capable.
type AttachmentCarrier interface {
	// CarriesAttachments reports whether this connector's provider transmits
	// files alongside the message body.
	CarriesAttachments() bool
}

// OutboundFile is one file to transmit — the published extension.OutboundFile,
// aliased for the reason part.go states.
type OutboundFile = extension.OutboundFile

// EmailMessage is one message to transmit, in provider-NEUTRAL form. The
// connector owns the wire encoding — Gmail takes base64url RFC822, Graph takes
// JSON — so no caller ever builds MIME. It is the mirror of Normalize, which
// owns decoding on the way in.
type EmailMessage struct {
	To []string
	Cc []string
	// Bcc receives the message and is rendered into NO header.
	//
	// The distinction lives here rather than in the renderer because it is a
	// fact about the addressees, not about one wire format: a provider that
	// takes an addressee list separately from the message (Graph, SES) needs
	// the same separation, and a renderer that had to infer it would have to
	// be told twice.
	Bcc []string
	// FromName is the sender's display name, or empty to send a bare address.
	//
	// A From header with no display name shows the address's LOCAL PART in every
	// mail client, so a message from lars@gradion.com arrives from "lars". The
	// address itself is the connector's own (the connected mailbox); this is the
	// human the CRM records as having sent it.
	FromName string

	Subject string
	Body    string // text/plain; always present, and the only part a text client reads

	// HTMLBody is the same message as markup, or empty for a plain-text send.
	//
	// It never REPLACES Body. A message carrying both goes out as
	// multipart/alternative with the plain part first, which is what lets a
	// client that cannot or will not render HTML — a screen reader, a terminal
	// client, a spam filter reading the cheapest part — still receive the words.
	// A mail with markup and no plain alternative is the shape that arrives as
	// a blank message.
	HTMLBody string

	// MessageID is the RFC822 message identity WITHOUT angle brackets —
	// "abc@host", never "<abc@host>". Stored and compared in this form because
	// that is how mail parsing yields it, so the copy the provider files back
	// into the mailbox carries a key that matches the one recorded at send.
	// The connector adds the brackets when it renders the header.
	MessageID string

	// InReplyTo threads onto an existing conversation, also unbracketed. Empty
	// starts a new thread.
	InReplyTo string

	// References is the unbracketed ancestry chain, oldest first.
	References []string

	// ListUnsubscribe and ListUnsubscribePost carry the RFC 8058 header pair for
	// a marketing send; both empty for a transactional purpose, which has nothing
	// to unsubscribe from.
	ListUnsubscribe     string
	ListUnsubscribePost string

	// Attempt is 0 on the first transmission and increments on every retry. It is
	// how a connector knows to run the prior-send lookup the contract requires.
	Attempt int

	// Files are the attachments this message carries. A connector handed a
	// non-empty set has already been asked whether it carries attachments — the
	// dispatcher parks otherwise — so reaching here with files means transmitting
	// them.
	//
	// THE INVARIANT, stated where an implementer reads it: no adapter may
	// transmit a message whose attachment set differs from the one it was
	// handed. Not a subset, not converted to links, not silently dropped. If it
	// cannot send all of them it returns an error and the delivery parks.
	Files []OutboundFile
}

// ErrInvalidMessageID marks an outbound message carrying no usable RFC822
// identity. It is the idempotency contract failing its precondition: Send is
// required to be idempotent on MessageID, and an identity the provider's
// prior-send lookup cannot search for makes that guarantee unkeepable. A
// message sent under one would mail its recipient again on every retry, and
// the copy the provider files back would key onto no activity.
var ErrInvalidMessageID = errors.New("connector: outbound message carries no usable RFC822 message identity")

// maxMessageIDLen bounds a message identity at a length a header can actually
// carry. RFC 5322 caps a header line at 998 octets, and this system renders the
// identity into Message-ID, In-Reply-To and a References chain that holds
// several of them at once, so the usable ceiling is far below that line limit
// — 512 is already an order of magnitude above what any provider mints (a
// Gmail identity is around forty characters). The bound matters because an
// identity is not only rendered: it is READ BACK out of a provider response of
// up to 96 MiB and adopted as a natural key, a thread key and a log field. An
// unbounded "valid" identity is a remote party choosing how many bytes this
// installation stores per sent message.
const maxMessageIDLen = 512

// ValidMessageID reports whether id is a usable RFC822 message identity in the
// UNBRACKETED form this system stores and compares: an addr-spec with exactly
// one '@', both sides non-empty, no whitespace, angle brackets, or ASCII
// control character (the connector adds the brackets at the wire), and no
// longer than a header line can carry. Control characters are rejected
// wholesale, not just the tab/CR/LF an editor is likely to type: any of them
// would render a malformed Message-ID header on the wire, and a provider that
// mangles or strips one on receipt breaks the retry path's rfc822msgid: lookup
// — the search that stops an at-least-once redelivery from mailing the
// recipient twice.
//
// It is the ONE spelling of that question, so the identity a send transmits
// under, the identity a threading header is derived from, and the identity a
// provider reports back cannot disagree about what counts.
func ValidMessageID(id string) bool {
	if len(id) > maxMessageIDLen {
		return false
	}
	local, domain, found := strings.Cut(id, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") {
		return false
	}
	for _, r := range id {
		switch {
		case r == ' ' || r == '<' || r == '>':
			return false
		case r <= 0x1F || r == 0x7F: // the full ASCII control range (C0 + DEL)
			return false
		}
	}
	return true
}

// Validate refuses a message no provider should be handed. It is the sender
// boundary's own precondition — checked before any provider I/O, so a message
// that cannot be retried safely is never transmitted a first time.
func (m EmailMessage) Validate() error {
	if !ValidMessageID(m.MessageID) {
		return ErrInvalidMessageID
	}
	return nil
}

// SendReceipt is what the provider confirmed: its own message identity, and
// the RFC822 identity the transmitted copy actually carries.
//
// The provider's CONVERSATION id is deliberately absent. This system threads on
// the RFC822 message identity — comms_outbound.thread_key and activity.thread_key
// both hold a Message-ID derived from References/In-Reply-To, which is what
// capture keys reply detection on. A provider's own conversation id (Gmail's
// threadId) lives in a different namespace, joins nothing here, and carrying it
// would invite a reader to key on a value no query reads.
//
// The RFC822 identity is the opposite case, and the distinction is worth
// holding onto: it joins everything here. A Message-ID is a REQUEST, not a
// guarantee — Gmail discards the client's and mints its own — so the identity
// this system records has to be the one the wire carries, not the one it asked
// for.
type SendReceipt struct {
	ProviderMessageID string
	// RFC822MessageID is the unbracketed Message-ID on the transmitted copy.
	//
	// EMPTY covers two different facts, and they are deliberately not
	// distinguished HERE. No re-key is OWED — the provider honoured the
	// identity it was given, or reports none — or no re-key is POSSIBLE,
	// because the read-back could not be answered. The first is a correct
	// no-op. The second is a degradation: on a provider that rewrites, the
	// message stays keyed on an identity the wire does not carry, and its
	// captured echo lands as a second timeline row.
	//
	// The field stays a plain identity because the alternative is worse at this
	// seam: a receipt that reported "unknown" would be asking every caller to
	// carry a recovery path, and the recovery does not belong to the caller.
	// ProviderMessageID is durable on the delivery, so a later pass can re-ask
	// the provider for the identity of a message it already accepted — that
	// pass is the fix for the degradation, and it is not in this change.
	RFC822MessageID string
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

// The channel surface: a unit supplying TRANSPORT for a messaging provider
// (DESIGN-SP5 §9, ADR-0107/A158).
//
// It is a separate declaration from IngressSource on purpose. Ingress says
// "records arrive from here"; a channel says "messages can leave through here",
// and the two are neither implied by nor sufficient for each other — a unit may
// capture a provider it cannot send on, which is the capture-only case below.
//
// The provider named here is NOT an activity kind. A channel message lands as
// kind `message` with this provider on its own column; that separation is the
// decision this whole arc implements, and a unit that could name a kind would
// undo it from the outside.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// providerGrammar and maxProviderLength are `channel_provider.provider`'s own
// CHECK, restated here so a unit's declaration is refused at boot with an
// explanation rather than at the first write with a constraint name. The two
// are the same rule in two places on purpose; a fitness test holds them equal.
var providerGrammar = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

const maxProviderLength = 32

// Channel is one messaging provider a unit supplies transport for.
//
// Inert declaration plus function fields, matching Job and Subscription: the
// Runtime arrives per invocation, and the unit holds no handle into the core.
type Channel struct {
	// Provider is the unit's key for the provider — a row in channel_provider,
	// and never an activity kind.
	//
	// The grammar is ProviderRef's (`^[a-z][a-z0-9_]*$`, 32 runes), which is
	// the CHECK on channel_provider.provider itself. It is deliberately NOT
	// IngressSource.System's, and the difference is easy to miss: that one is
	// kebab (`a-z0-9` with `-`), this one is snake (`_`). A provider is a row
	// in a table with its own constraint, so the declaration must satisfy the
	// column rather than a sibling declaration that happens to look similar —
	// `deal-room` is a legal ingress system and an illegal provider.
	Provider string

	// Send transmits one message.
	//
	// NIL IS MEANINGFUL: a channel declared without it is capture-only, and a
	// reply attempt is answered with the deployment fact rather than a fault.
	// This follows Job's idiom (a nil Handle is no entry) rather than
	// Subscription's (which refuses nil at Validate), and the difference is
	// real: a subscription with no handler is a listener nobody hears, always a
	// mistake; a channel with no Send is the documented capture-only case.
	Send MessageSender

	// Live answers, for ONE member, whether this unit's connection to the
	// provider is usable for sending right now — without spending the
	// credential, the same shape as a dry run.
	//
	// It takes no message because liveness does not depend on what is being
	// sent, only on whether the member's connection still exists and has not
	// been revoked since a delivery was staged. A confirmed "not usable"
	// answers false; "I could not tell" returns an error, and the core treats
	// those two differently — one parks a delivery, the other retries it.
	//
	// REQUIRED whenever Send is non-nil: a transport that can send and cannot
	// say whether it still may is one the core would have to guess about at the
	// exact moment guessing is most expensive.
	Live ConnectionLiveChecker
}

// MessageSender transmits one outbound message on a unit's channel.
type MessageSender func(ctx context.Context, rt Runtime, msg OutboundMessage) (Receipt, error)

// ConnectionLiveChecker answers whether one member's connection is usable now.
type ConnectionLiveChecker func(ctx context.Context, rt Runtime, member UserID) (bool, error)

// OutboundMessage is one message handed to a unit for transmission.
type OutboundMessage struct {
	// Member is WHOSE credential transmits — the rep who staged the send, not
	// the caller who released it. A unit sends as a person, never as the
	// installation.
	Member UserID
	// Recipient is the provider account id, never the username: a username is
	// re-assignable and an account id is not, so routing on the readable one
	// delivers to whoever holds the handle today.
	Recipient ChannelIdentity
	Body      string
	// ReplyTo anchors the message on a provider message id, empty for none.
	ReplyTo string
	// IdempotencyKey is the delivery id: minted per send and never reused, so a
	// retried attempt is recognisable to a provider that supports it.
	IdempotencyKey string
	// Attempt counts from 1. A unit that cannot make its send idempotent can at
	// least log the difference between a first try and a repeat.
	Attempt int
}

// Receipt is what a provider returns for an accepted message.
type Receipt struct {
	// ProviderMessageID is the provider's own id for the sent message, and it
	// is what makes a later reply anchorable. Empty is allowed: not every
	// provider returns one, and a unit inventing an id would make a thread key
	// that matches nothing.
	ProviderMessageID string
}

// ChannelIdentity is a party at a messaging provider.
//
// The name deliberately matches core's own connector.ChannelIdentity rather
// than introducing a second name for the same routing key: a silent rename at a
// security-sensitive identity is exactly the drift a matching name avoids
// having to test for.
type ChannelIdentity struct {
	// Provider is the transport the account belongs to. It is carried rather
	// than assumed from the channel that routes the message, because a record
	// naming a party has to be readable on its own.
	Provider string
	// ChannelUserID is the provider's account id.
	ChannelUserID string
	// DisplayName is what a human calls them, for a person record that has no
	// other name yet. Never used for routing.
	DisplayName string
}

// Validate enforces the provider grammar and the Send/Live pairing, and nothing
// else — every other rule a channel is subject to is a rule about the composed
// SET (a provider may not collide with another unit's, or with a core
// connector's), which one declaration cannot see and the boot resolves.
func (c Channel) Validate() error {
	if !providerGrammar.MatchString(c.Provider) {
		return fmt.Errorf("%w: channel provider %q must start with a letter and contain only lower-case letters, digits and underscores — it is a channel_provider row and has to satisfy that column's own CHECK",
			ErrInvalid, c.Provider)
	}
	if len([]rune(c.Provider)) > maxProviderLength {
		return fmt.Errorf("%w: channel provider %q is longer than %d characters",
			ErrInvalid, c.Provider, maxProviderLength)
	}
	// The pairing, refused HERE rather than at the send: a transport that can
	// transmit and cannot report its own liveness would force the core to
	// decide between parking a delivery it might have sent and retrying one it
	// already did, at the one moment that choice is unrecoverable.
	if c.Send != nil && c.Live == nil {
		return fmt.Errorf("%w: channel %q declares Send without Live — a transport that can send must be able to say whether it still may",
			ErrInvalid, c.Provider)
	}
	return nil
}

// ErrSendOutcomeUnknown is the one refusal a Send MUST report rather than
// describe: the request went out and no usable answer came back — a timeout, a
// reset connection, a response that could not be read. The message may be on
// its way and may not, and nothing the unit has can tell.
//
// IT IS NOT A DEGREE OF FAILURE, it is a different kind. Every other error a
// Send returns is read by the core as a DEFINITE answer from the provider, and
// therefore as proof that nothing was transmitted — the delivery goes back on
// the retry ladder. A channel has no prior-send lookup and no provider-honoured
// idempotency key, so no later attempt can ever discover that an earlier one
// already arrived; report an unanswered POST as an ordinary transport failure
// and the recipient gets the rep's message twice, with nothing in the system
// able to detect it.
//
// So the rule is narrow and worth stating twice: a failure of the CALL that
// transmits is this class. A failure to open a client, to unseal a credential,
// or a refusal the provider actually sent, is not — those are answers.
//
// A delivery reported this way STOPS, with the uncertainty on the record for a
// human to resolve. That is deliberately worse than a retry for the unit author
// and better for the recipient.
var ErrSendOutcomeUnknown = errors.New("extension: the provider never reported the outcome of this transmission")

// SuppliesTransport reports whether this channel can carry an outbound message
// at all. It is the declaration's own answer to the question the transport
// directory publishes, so the endpoint and the boot cannot disagree about it.
func (c Channel) SuppliesTransport() bool { return c.Send != nil }

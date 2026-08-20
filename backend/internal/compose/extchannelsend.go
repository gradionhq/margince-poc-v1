// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The send half of the unit channel surface: how a delivery staged against a
// UNIT's transport reaches that unit's Send (DESIGN-SP5 §9, ADR-0107/A158).
//
// Everything upstream of here is the product's ordinary channel reply — the
// rep's reply box, the recipient resolved from the conversation, the consent
// gate, the staged delivery, the retry ladder. This file is the last edge: the
// core's own MessageSender seam, implemented over a unit's declaration.
//
// WHOSE CREDENTIAL TRANSMITS is the whole difference from a core connector. A
// core channel binding is the workspace's one bot; a unit's is the MEMBER's own
// sealed secret, so the delivery's user id is not decoration here — it selects
// the credential. It is the sending human's id, staged with the delivery and
// re-read at transmission, which is why a message cannot be made to leave under
// somebody else's connection by anything that happens after staging.

import (
	"context"
	"errors"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// unitTransport is one composed unit channel, resolved by provider: which unit
// declared it, and the declaration itself.
type unitTransport struct {
	unit    extension.Name
	version string
	channel extension.Channel
}

// composedUnitTransport finds the unit that supplies a provider in THIS boot's
// composition.
//
// It reads the composed declarations for composedIngressFor's reason — the set
// the boot reconciliation validated is the only honest answer — and the boot has
// already refused a provider claimed twice (preflightChannels) or shared with a
// core connector (unitChannelFacts), so the first match here is the only match.
func composedUnitTransport(provider string) (unitTransport, bool) {
	for _, ext := range ComposedExtensions() {
		for _, ch := range ext.Channels {
			if ch.Provider == provider {
				return unitTransport{unit: ext.Name, version: string(ext.Version), channel: ch}, true
			}
		}
	}
	return unitTransport{}, false
}

// unitChannelSender transmits one delivery through a unit's declared Send,
// under one member's credential.
//
// It is built per resolve rather than held, because the member it is bound to is
// the delivery's and a sender shared across deliveries would be a credential
// shared across people.
type unitChannelSender struct {
	transport unitTransport
	member    extension.UserID
	deps      extensionRuntimeBinding
}

var _ connector.MessageSender = unitChannelSender{}

// SendMessage hands the message to the unit.
//
// auth is IGNORED, and the signature keeps it because the seam is shared with
// core connectors, whose credential the core unseals and passes. A unit's is the
// unit's: it lives under the unit's own secret scope, sealed per member, and the
// core never holds it — which is also why nothing here can log or leak it.
//
// The Runtime is UNATTENDED, the same shape a job tick gets. Nobody is at the
// keyboard when a delivery transmits: the human staged it, the seat gate re-read
// them, and the worker is running the ladder. What the unit may do with an
// unattended Runtime is already decided — its own tables and its own secrets,
// with the governed core port shut.
func (s unitChannelSender) SendMessage(ctx context.Context, _ connector.Auth, msg connector.ChannelMessage) (connector.SendReceipt, error) {
	if s.transport.channel.Send == nil {
		// Reachable only if the sendable set and the declaration disagree, which
		// the reconcile builds one from the other to prevent. Refused by name
		// rather than dereferenced, because the alternative is a nil call in the
		// worker at the moment a rep's message is due to leave.
		return connector.SendReceipt{}, fmt.Errorf("%w: extension %q declares the transport %q with no sender",
			comms.ErrCannotSend, s.transport.unit, s.transport.channel.Provider)
	}
	rt := sendRuntimeFor(ctx, string(s.transport.unit), s.transport.version,
		"channel/"+s.transport.channel.Provider, s.deps)
	defer rt.release()
	// extension.OutboundMessage has no file field, so a unit has nowhere to put
	// one — and a message handed here with files would go out as text that lies
	// about its contents. It refuses instead (connector.ErrFilesNotCarried); the
	// published surface growing a file field is what removes this.
	if len(msg.Files) > 0 {
		return connector.SendReceipt{}, connector.ErrFilesNotCarried
	}
	receipt, err := s.transport.channel.Send(ctx, rt, extension.OutboundMessage{
		Member: s.member,
		// The provider plus the account id ARE the recipient key, exactly as the
		// dispatcher built them. The display handle is deliberately not carried:
		// a handle can be released and re-claimed, so nothing may route on it.
		Recipient: extension.ChannelIdentity{
			Provider:      msg.Recipient.Provider,
			ChannelUserID: msg.Recipient.ChannelUserID,
		},
		Body:           msg.Body,
		ReplyTo:        msg.ReplyTo,
		IdempotencyKey: msg.IdempotencyKey,
		// The seam counts retries from 0 and the published surface from 1, and
		// the shift is stated rather than shared: a unit logging "attempt 1" for
		// the first try is what a human reads, and a unit is other people's code
		// that should not have to know the core's convention.
		Attempt: msg.Attempt + 1,
	})
	if err != nil {
		return connector.SendReceipt{}, unitSendRefusal(err)
	}
	// No RFC822 identity: a channel message has none, which is why the channel
	// seam keys its retry safety on the idempotency key instead.
	return connector.SendReceipt{ProviderMessageID: receipt.ProviderMessageID}, nil
}

// unitSendRefusal translates a unit's refusal into the core's own, and it
// carries exactly ONE class across because only one changes what the
// dispatcher does.
//
// The channel seam does not detect a prior send: it records a transmission as
// in flight BEFORE the call, and then treats every error that is not
// ErrSendOutcomeUnknown as a definite answer from the provider — which is to
// say as proof that nothing went out — clears the marker, and puts the delivery
// back on the ladder. That is correct for a refusal the provider actually sent
// and catastrophic for a POST whose answer was lost: the recipient gets the
// rep's message twice and nothing in the system can tell.
//
// So a unit reporting extension.ErrSendOutcomeUnknown STOPS the delivery, with
// the uncertainty on the record. Everything else rides the ladder, including a
// revoked credential — which parks on the next attempt, where Live answers
// false, rather than being classified from an error string here.
func unitSendRefusal(err error) error {
	if errors.Is(err, extension.ErrSendOutcomeUnknown) {
		return fmt.Errorf("%w: %w", connector.ErrSendOutcomeUnknown, err)
	}
	return err
}

// unitSendCapable answers the REQUEST-time pre-flight for a unit transport:
// can this member send on it right now.
//
// It asks the unit's own Live, which is the same question the delivery asks and
// deliberately the same answer — a rep told "you can send" at the composer and
// then parked at transmission has been told two things by one installation.
//
// A unit that cannot ANSWER reports the fault rather than a verdict, which is
// the mail arm's own rule one branch below: a pre-flight that cannot ask must
// not answer, because asserting a capability nobody read is how a rep learns at
// transmission what they should have been told at the composer.
func unitSendCapable(ctx context.Context, transport unitTransport, member ids.UserID) (bool, error) {
	if !transport.channel.SuppliesTransport() {
		return false, nil
	}
	return unitConnectionLive(ctx, transport, extension.UserID(member.String()), boundExtensionRuntime())
}

// resolveUnitChannel binds a unit's transport to the member whose credential
// carries the delivery, and answers the deployment facts the dispatcher parks on.
//
// LIVENESS IS ASKED HERE, before the message is handed over, and that is the
// whole reason Channel.Live is required of a unit that declares a Send. A member
// who disconnected between staging and transmission has nothing to retry into,
// so the delivery must PARK where a human can see it — while a provider that
// could not answer is a failure to get an answer, and parking on one would
// destroy a send that nothing is wrong with. The two are told apart by the
// unit's own contract: false versus an error.
//
//nolint:ireturn // half of ResolveChannel's own return, whose contract is the optional connector.MessageSender seam
func (r commsResolver) resolveUnitChannel(ctx context.Context, userID ids.UserID, transport unitTransport) (connector.MessageSender, connector.Auth, error) {
	if !transport.channel.SuppliesTransport() {
		// The unit captures on this provider and cannot send on it — the
		// documented capture-only case, and a fact about the deployment rather
		// than about this message.
		return nil, nil, fmt.Errorf("%w: extension %q supplies %q for capture only",
			comms.ErrCannotSend, transport.unit, transport.channel.Provider)
	}
	deps := boundExtensionRuntime()
	member := extension.UserID(userID.String())
	live, err := unitConnectionLive(ctx, transport, member, deps)
	if err != nil {
		return nil, nil, err
	}
	if !live {
		return nil, nil, fmt.Errorf("%w: this member has no live connection to %q",
			comms.ErrNoMailbox, transport.channel.Provider)
	}
	return unitChannelSender{transport: transport, member: member, deps: deps}, nil, nil
}

// unitConnectionLive asks the unit whether this member can still send, on a
// Runtime of its own.
//
// A separate Runtime from the send's, and released before the send is built: the
// two are two invocations of the unit, and a Runtime that outlived its call is
// the thing release exists to prevent.
func unitConnectionLive(ctx context.Context, transport unitTransport, member extension.UserID, deps extensionRuntimeBinding) (bool, error) {
	rt := sendRuntimeFor(ctx, string(transport.unit), transport.version,
		"channel/"+transport.channel.Provider, deps)
	defer rt.release()
	live, err := transport.channel.Live(ctx, rt, member)
	if err != nil {
		// Deliberately NOT a deployment fact: the unit says it could not tell,
		// and the dispatcher retries what it cannot classify.
		return false, fmt.Errorf("compose: extension %q could not confirm this member's %q connection: %w",
			transport.unit, transport.channel.Provider, err)
	}
	return live, nil
}

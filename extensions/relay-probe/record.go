// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// One notification becomes one record the core can land — and, before that, the
// decision about which notifications are worth landing at all.

import (
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// directedTypes are the notification types that mean somebody addressed this
// member: a direct message, a mention, a reply in a thread they are in.
//
// It is an ALLOWLIST, and a type this unit has never heard of is not captured.
// That direction is deliberate: the cost of missing a new directed type is a
// message absent from a timeline until somebody adds a line here, and the cost
// of the other direction is every reaction, every system notice and every
// future notification kind landing on the CRM's shared timeline as a customer
// interaction. One is a gap; the other is a corpus nobody asked for and cannot
// easily unpick.
//
// The vocabulary was read off a live deployment. `reaction` is the one it is
// most important to exclude and the one an inbox is mostly made of.
var directedTypes = map[string]bool{
	"dm":              true,
	"dm_thread_reply": true,
	"mention":         true,
	"channel_mention": true,
	"thread_reply":    true,
}

// directed reports whether this item is one a member was addressed by, and it
// answers the bot question too.
//
// THE SERVER-SIDE FILTER DOES NOT WORK. The API takes `source=human` and
// answering it changes nothing — the same reactions and the same bot traffic
// come back either way, measured against the deployment. A unit that passed the
// parameter and trusted it would believe it had filtered and would not have, so
// the filtering is here, where it can be tested.
func directed(item inboxItem) bool {
	return directedTypes[item.Type] && !item.Metadata.SenderIsBot
}

// recordFor builds the record one directed notification lands as.
//
// sender is the resolved account behind item.SenderID, and member is the
// connection's own account. BOTH ends are named in Addresses, and that is not
// bookkeeping: the core decides whether a message is purely internal — two
// colleagues on the installation's own domains, which is not evidence of a
// customer relationship — by asking whether every party is internal, and it
// reads an EMPTY address set as "this connector could not enumerate the
// parties", which keeps the message. So a record with one end named would
// silently disable a gate rather than pass it.
func recordFor(item inboxItem, sender, member providerUser, providerWorkspace string) (extension.Record, error) {
	if sender.Email == "" {
		return extension.Record{}, fmt.Errorf("relay: the sender of notification %d resolved to no address", item.ID)
	}
	return extension.Record{
		System: ingressSystem,
		// The provider's own id, which is what makes a replay a no-op. It is
		// namespaced by the provider workspace because two Relay deployments
		// number their notifications independently, and this unit's records
		// share one provenance namespace across every connection.
		Key: fmt.Sprintf("%s:%d", providerWorkspace, item.ID),
		Activity: extension.ActivityFields{
			// A message, on this unit's own transport — the two axes stated
			// separately (ADR-0107/A158). It landed as a `note` before this unit
			// supplied a channel, which was honest then and is not now: a note
			// carries no transport, so nothing could be replied to and every
			// captured conversation was a dead end on the timeline.
			//
			// The provider is a LITERAL rather than the `provider` constant, for
			// the reason the ingress declaration is one: the core holds a unit to
			// the channels it DECLARED, and that declaration is read statically
			// from the AST without compiling the unit. A test holds the two equal.
			Kind:            extension.ActivityKindMessage,
			ChannelProvider: "relay",
			// The provider's title is the human-readable "who did what"; the
			// body is the message preview. Neither is fetched in full: a CRM
			// timeline entry is a pointer to a conversation, not a copy of it.
			Subject:    strings.TrimSpace(item.Title),
			Body:       strings.TrimSpace(item.Body),
			OccurredAt: item.CreatedAt,
			Direction:  extension.DirectionInbound,
		},
		// Namespaced by provider AND deployment, per the core's own channel
		// rule. A bare channel id would share activity.thread_key with every
		// other source, where two of them can collide and join a stranger's
		// conversation onto this one.
		ThreadKey:    fmt.Sprintf("relay:%s:%s", providerWorkspace, item.ChannelID),
		Counterparty: counterpartyOf(item, sender),
		Addresses:    []string{sender.Email, member.Email},
		// The provider's record as received, kept as evidence. It is the
		// original document rather than a re-encoding of the fields above, so
		// what the installation stores is what the provider said.
		Raw: item.Raw,
	}, nil
}

// directMessageTypes are the notification types whose channel is a PAIR: this
// member and the sender, and nobody else. They are the subset of directedTypes
// that carries a reply address.
var directMessageTypes = map[string]bool{
	"dm":              true,
	"dm_thread_reply": true,
}

// counterpartyOf names the human at the other end, and the shape decides which
// ladder resolves them.
//
// The two ladders are different, which is why the shape is a real choice rather
// than a formality. An address alone goes through the mail ladder, where a
// corporate domain is DEFERRED to the pending inbox and only a freemail sender
// mints a person. An account goes through the channel ladder, which binds the
// account to a person the reply path can then resolve — and there the address,
// where this unit holds one, rides along as matching evidence rather than as a
// second way of naming anybody.
//
// A DIRECT MESSAGE is named by the SENDER'S OWN ACCOUNT ID, not by the channel
// the message arrived in. The core keys the binding on (provider, account id)
// and treats that row as the person: it is what an erasure suppresses and what
// the reply path resolves. Keying it on a conversation instead would make the
// same colleague two people when they DM two members, and an erasure armed for
// one of those would leave their other chats capturable. The send resolves the
// conversation from the account when it needs one (client.dmChannelWith).
//
// A MENTION IN A SHARED CHANNEL is named by address instead. The room gives
// this unit no per-person address to bind, and a reply resolved from the room
// would post in front of everyone in it.
//
// A mention is therefore captured, reads on the timeline, and is not answerable
// on this transport. That is the honest state for a room this unit has no
// private conversation in.
func counterpartyOf(item inboxItem, sender providerUser) extension.Counterparty {
	if !directMessageTypes[item.Type] {
		return extension.Counterparty{
			Email:       sender.Email,
			DisplayName: sender.name(),
			Domain:      mailDomain(sender.Email),
			Direction:   extension.DirectionInbound,
		}
	}
	return extension.Counterparty{
		DisplayName: sender.name(),
		Direction:   extension.DirectionInbound,
		ChannelIdentity: extension.ChannelIdentity{
			Provider:      "relay",
			ChannelUserID: sender.ID,
			DisplayName:   sender.name(),
		},
		// The address is CARRIED, not used to name anybody: the account above
		// names the sender and the core prefers it. What this buys is the
		// colleague already captured from mail being recognised as the same
		// human instead of quietly becoming a second contact — this unit knows
		// the address, and dropping it would throw away evidence only this side
		// has. The core admits it because the ingress source declares the email
		// merge key; without that declaration this record would be refused.
		Email: sender.Email,
	}
}

// mailDomain is the lower-cased domain half of an address, or empty when there
// is not one. It is for the MAIL shape alone: the core's suppression gates key
// on it, and a unit that left it empty there would not be opting out of them —
// it would be failing to answer, which those gates read as "keep". A channel
// record short-circuits past every one of those gates, so it names no domain.
func mailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

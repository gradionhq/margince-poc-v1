// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

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
		return extension.Record{}, fmt.Errorf("dispact: the sender of notification %d resolved to no address", item.ID)
	}
	return extension.Record{
		System: ingressSystem,
		// The provider's own id, which is what makes a replay a no-op. It is
		// namespaced by the provider workspace because two Dispact deployments
		// number their notifications independently, and this unit's records
		// share one provenance namespace across every connection.
		Key: fmt.Sprintf("%s:%d", providerWorkspace, item.ID),
		Activity: extension.ActivityFields{
			Kind: "note",
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
		ThreadKey: fmt.Sprintf("dispact:%s:%s", providerWorkspace, item.ChannelID),
		Counterparty: extension.Counterparty{
			Email:       sender.Email,
			DisplayName: sender.name(),
			Domain:      mailDomain(sender.Email),
			Direction:   extension.DirectionInbound,
		},
		Addresses: []string{sender.Email, member.Email},
		// The provider's record as received, kept as evidence. It is the
		// original document rather than a re-encoding of the fields above, so
		// what the installation stores is what the provider said.
		Raw: item.Raw,
	}, nil
}

// mailDomain is the lower-cased domain half of an address, or empty when there
// is not one. The core's suppression gates key on it, and a unit that left it
// empty would not be opting out of them — it would be failing to answer, which
// those gates read as "keep".
func mailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

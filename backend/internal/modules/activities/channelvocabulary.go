// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The channel vocabulary: which kind names a messaging conversation, and which
// transports this installation can actually reply on.
//
// Split from the send path because they are different questions with different
// lifetimes — the kind is fixed by the contract, the transport set is decided at
// boot by what the binary composed — and ADR-0107/A158 is the decision that
// stopped one answer standing in for the other.

import (
	"sort"
	"sync"
)

// channelProviders is the derived channel-kind vocabulary (DESIGN-SP4 §4):
// which activity kinds are messaging-channel conversations this installation
// can actually reply on. Set once at boot by compose's registry reconcile
// (internal/compose), from the composed core-connector and unit set — this
// module must not import either to compute it itself.
//
// A plain mutex-guarded package var, not a "register once, panic on
// duplicate" idiom: compose's own registry construction can legitimately run
// more than once in a process (a role-specific alternate wiring path, a
// one-shot backfill helper), and every one of those calls must be able to
// call SetChannelProviders again with the same or a narrower set — the last
// call in a boot sequence is authoritative, not the first.
var (
	channelProvidersMu sync.RWMutex
	channelProviders   = map[string]bool{"telegram": true} // the pre-registry default, replaced at first boot
)

// SetChannelProviders replaces the derived channel-kind vocabulary wholesale.
// Exported so compose — which knows the composed connector/unit set this
// module must not reach across to enumerate — can set it; nothing in this
// module calls it.
func SetChannelProviders(providers []string) {
	next := make(map[string]bool, len(providers))
	for _, p := range providers {
		next[p] = true
	}
	channelProvidersMu.Lock()
	channelProviders = next
	channelProvidersMu.Unlock()
}

// KindMessage is the one interaction kind that names a channel conversation
// (ADR-0107/A158). Which transport carried it is activity.channel_provider, a
// separate axis — reading one off the other is exactly what that decision
// retired.
const KindMessage = "message"

// IsChannelKind reports whether an activity kind names a messaging-channel
// conversation.
//
// Since the narrowing this is a comparison, not a set membership test: every
// channel message is `message` regardless of transport. Kept as a function
// rather than inlined at its callers because those callers ask a domain
// question — "is this conversation repliable in principle" — and the answer
// changing shape once already is the argument for it having one name.
func IsChannelKind(kind string) bool {
	return kind == KindMessage
}

// SendableChannelProviders lists the transports this installation composed a
// sender for. Exported so the composition root can publish which registered
// transports can actually carry a message, without keeping a second copy of a
// set this package already owns — two copies is how the send path and the
// directory would come to disagree.
func SendableChannelProviders() []string {
	channelProvidersMu.RLock()
	defer channelProvidersMu.RUnlock()
	out := make([]string, 0, len(channelProviders))
	for p := range channelProviders {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// CanSendOnProvider reports whether this installation composed a transport
// that can carry a reply on the given provider.
//
// This is deliberately NOT the same question as IsChannelKind. A row may name a
// perfectly real transport this binary did not compose — whatsapp is registered
// but has no connector — and the honest answer there is "this is a message, and
// no reply can leave this installation for it". Answering both questions with
// one set is what let a kind stand in for a transport in the first place.
func CanSendOnProvider(provider string) bool {
	channelProvidersMu.RLock()
	defer channelProvidersMu.RUnlock()
	return channelProviders[provider]
}

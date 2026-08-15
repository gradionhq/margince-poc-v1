// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What `GET /v1/channel-providers` publishes about a transport, and where those
// facts come from (ADR-0107/A158).
//
// They live in the composition root because that is the only layer that knows
// what this binary COMPOSED. A module cannot answer it — `internal/modules` may
// not enumerate connectors, and `channel_provider` carries no workspace_id to
// scope a query by, so a module has no unscoped pool to read it from either.

import (
	"fmt"
	"strings"
	"unicode"
)

// credentialWorkspaceBot and credentialPerMember are the two shapes a channel
// credential takes. Closed on purpose, unlike the provider vocabulary: this
// describes the SHAPE of a credential, which is installation-independent, not
// which providers exist, which is not.
const (
	credentialWorkspaceBot = "workspace_bot" //nolint:gosec // G101 false positive: the SHAPE a credential takes, published in the contract's enum — not a credential
	credentialPerMember    = "per_member"
)

// transportCore and transportUnit are who SUPPLIES a transport: a connector
// compiled into the core, or an extension unit under extensions/. Closed for
// credentialWorkspaceBot's reason — it describes the shape of the supply, not
// which providers exist.
const (
	transportCore = "core"
	transportUnit = "unit"
)

// channelProviderFacts is one transport as the discovery endpoint publishes it.
type channelProviderFacts struct {
	provider string
	// transport is who supplies it. It is a fact about the INSTALLATION rather
	// than about the provider: `telegram` is a core transport here and could be
	// a unit's somewhere else, which is why the collision between the two is a
	// boot failure rather than a naming convention.
	transport         string
	label             string
	credentialModel   string
	suppliesTransport bool
}

// coreProviderLabels names the transports this binary compiles in, for the
// cases where title-casing the id gets the name wrong.
//
// A map rather than an optional interface on the connector port: exactly one
// provider needs it today (WhatsApp, whose title-cased id reads "Whatsapp"),
// and a port method with one implementer is an abstraction ahead of its second
// caller. When a unit declares its own channel in the slice that gives it one,
// the label arrives with the declaration and this map stays what it says it is
// — the CORE names.
var coreProviderLabels = map[string]string{
	"whatsapp": "WhatsApp",
}

// providerLabel is what a human reads where the raw id would otherwise appear.
//
// It is derived or compiled in, never operator-configured, because the endpoint
// is readable by every authenticated seat: a provider id plus a display name is
// not privileged, but anything an administrator typed might be.
func providerLabel(provider string) string {
	if known, ok := coreProviderLabels[provider]; ok {
		return known
	}
	// Title-case the id, treating `_` as a word break so `deal_room` reads
	// "Deal Room" rather than "Deal_room". The grammar channel_provider
	// enforces means this is the whole space of shapes to handle.
	words := strings.Split(provider, "_")
	for i, w := range words {
		if w == "" {
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// channelProviderFactsFor describes every provider the registry knows, marking
// the ones that can actually carry a message.
//
// registered is every row the registry holds; sending is the subset the binary
// composed a MessageSender for. The difference is the honest case this endpoint
// exists to publish: whatsapp is registered so a hand-logged WhatsApp message
// can name what carried it, and nothing composes it, so it supplies no
// transport until A103's connector lands.
//
// credential_model is workspace_bot for every core connector, because a core
// channel connector binds ONE bot for the installation. A unit's is per_member —
// see unitChannelFacts.
func channelProviderFactsFor(registered, sending []string) []channelProviderFacts {
	sends := make(map[string]bool, len(sending))
	for _, p := range sending {
		sends[p] = true
	}
	out := make([]channelProviderFacts, 0, len(registered))
	for _, p := range registered {
		out = append(out, channelProviderFacts{
			provider:          p,
			transport:         transportCore,
			label:             providerLabel(p),
			credentialModel:   credentialWorkspaceBot,
			suppliesTransport: sends[p],
		})
	}
	return out
}

// unitChannelFacts describes every transport this boot's UNITS supply, and
// holds the one rule a unit's declaration cannot check for itself: it may not
// shadow a core connector.
//
// THE COLLISION IS THE SHARPEST FAILURE THIS SURFACE HAS, which is why it is a
// boot failure and not a warning. A unit declaring `telegram` would take over
// the row the workspace's own bot is registered under, and every Telegram reply
// a rep wrote would then leave on the unit's per-member credential instead —
// the same message, sent by a different person, with nothing on the screen
// different. Refusing at boot costs an installation a rename.
//
// It lives HERE rather than in preflightChannels because this is the first
// point at which both sets exist: the core's transports are decided when the
// capture registry is built, which can happen after extension registration, so
// the preflight would answer from an empty set and pass the collision it exists
// to catch. The unit-vs-unit half is still the preflight's — that one needs no
// core knowledge, and refusing it earlier is a better error.
//
// credential_model is per_member for every unit transport, because that is what
// the tier makes available: a unit holds one sealed secret per member (the
// user-scoped SecretsRequest) and has no installation credential to send under.
// A unit that grows a workspace-wide one declares it, and this derives it.
//
// The label is DERIVED from the id rather than declared, which is the same
// decision providerLabel documents: this endpoint is readable by every
// authenticated seat, and a derived name cannot carry text somebody typed.
func unitChannelFacts(coreProviders []string) ([]channelProviderFacts, error) {
	core := make(map[string]bool, len(coreProviders))
	for _, p := range coreProviders {
		core[p] = true
	}
	var out []channelProviderFacts
	for _, ext := range ComposedExtensions() {
		for _, ch := range ext.Channels {
			if core[ch.Provider] {
				return nil, fmt.Errorf("compose: extension %q declares the transport %q, which a core connector already supplies — a message on it would leave on the unit's credential instead of the workspace's, so rename the unit's channel", ext.Name, ch.Provider)
			}
			out = append(out, channelProviderFacts{
				provider:          ch.Provider,
				transport:         transportUnit,
				label:             providerLabel(ch.Provider),
				credentialModel:   credentialPerMember,
				suppliesTransport: ch.SuppliesTransport(),
			})
		}
	}
	return out, nil
}

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

// channelProviderFacts is one transport as the discovery endpoint publishes it.
type channelProviderFacts struct {
	provider          string
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
// channel connector binds ONE bot for the installation. per_member arrives with
// the unit channel surface, where a member deposits their own secret — the
// constant is named here so the vocabulary is complete rather than growing a
// second spelling when that lands.
func channelProviderFactsFor(registered, sending []string) []channelProviderFacts {
	sends := make(map[string]bool, len(sending))
	for _, p := range sending {
		sends[p] = true
	}
	out := make([]channelProviderFacts, 0, len(registered))
	for _, p := range registered {
		out = append(out, channelProviderFacts{
			provider:          p,
			label:             providerLabel(p),
			credentialModel:   credentialWorkspaceBot,
			suppliesTransport: sends[p],
		})
	}
	return out
}

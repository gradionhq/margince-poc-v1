// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension_test

// The published channel declaration: what a unit may say it transports, and the
// two rules one declaration can settle for itself. Everything else a channel is
// subject to — colliding with another unit's provider, or with a core
// connector's — is a fact about the composed SET, and lives in the boot.

import (
	"context"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func aSender() extension.MessageSender {
	return func(context.Context, extension.Runtime, extension.OutboundMessage) (extension.Receipt, error) {
		return extension.Receipt{}, nil
	}
}

func aLiveCheck() extension.ConnectionLiveChecker {
	return func(context.Context, extension.Runtime, extension.UserID) (bool, error) { return true, nil }
}

// A transport that can send and a transport that only captures are both
// well-formed, and the second is the one worth pinning: a nil Send is the
// documented capture-only case, not an incomplete declaration.
func TestAWellFormedChannelIsAcceptedSendingOrNot(t *testing.T) {
	for name, ch := range map[string]extension.Channel{
		"a transport that sends": {Provider: "dispact", Send: aSender(), Live: aLiveCheck()},
		"a capture-only channel": {Provider: "dispact"},
		"digits and underscores": {Provider: "deal_room2"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ch.Validate(); err != nil {
				t.Fatalf("a well-formed channel was refused: %v", err)
			}
		})
	}
}

// The grammar is `channel_provider.provider`'s own CHECK, restated so a unit is
// refused at BOOT with an explanation rather than at the first write with a
// constraint name.
//
// The kebab case is the one that matters: DESIGN-SP5 §9 claimed a channel
// provider shares IngressSource.System's grammar, and it does not — ingress is
// kebab, a provider is snake — so `deal-room` is a legal ingress system and an
// illegal provider. A declaration that took the sibling's rule would boot and
// then violate the column.
func TestTheProviderGrammarIsTheRegistryColumnsAndNotTheIngressDeclarations(t *testing.T) {
	for name, provider := range map[string]string{
		"empty":                     "",
		"kebab, which ingress uses": "deal-room",
		"leading digit":             "1chat",
		"leading underscore":        "_chat",
		"upper case":                "Dispact",
		"a dot":                     "dispact.chat",
		"a space":                   "deal room",
		"over the length cap":       strings.Repeat("c", 33),
	} {
		t.Run(name, func(t *testing.T) {
			if err := (extension.Channel{Provider: provider}).Validate(); err == nil {
				t.Fatalf("the grammar accepted %q, which channel_provider's own CHECK would refuse", provider)
			}
		})
	}
}

// Send and Live travel together, and it is refused at the DECLARATION rather
// than at the send: a transport that can transmit and cannot report its own
// liveness would leave the core choosing between parking a delivery it might
// have sent and retrying one it already did, at the one moment that choice is
// unrecoverable.
func TestATransportThatCanSendMustBeAbleToSayWhetherItStillMay(t *testing.T) {
	err := extension.Channel{Provider: "dispact", Send: aSender()}.Validate()
	if err == nil {
		t.Fatal("a channel declaring Send without Live was accepted")
	}
	if !strings.Contains(err.Error(), "Live") {
		t.Errorf("the refusal %q does not name the missing half, which is the one thing the author has to add", err)
	}
	// The other way round is legal: a capture-only channel may still answer
	// whether the member's connection exists, and there is nothing unsafe about
	// knowing it.
	if err := (extension.Channel{Provider: "dispact", Live: aLiveCheck()}).Validate(); err != nil {
		t.Fatalf("a capture-only channel that can report liveness was refused: %v", err)
	}
}

// SuppliesTransport is the declaration's own answer to the question the
// transport directory publishes, so the endpoint and the boot cannot disagree
// about which registered transports a reply can actually leave on.
func TestSuppliesTransportIsTheDeclarationsOwnAnswer(t *testing.T) {
	if (extension.Channel{Provider: "dispact"}).SuppliesTransport() {
		t.Error("a channel with no Send reported that it supplies transport; every reply staged against it would park")
	}
	if !(extension.Channel{Provider: "dispact", Send: aSender(), Live: aLiveCheck()}).SuppliesTransport() {
		t.Error("a channel with a Send reported that it supplies none")
	}
}

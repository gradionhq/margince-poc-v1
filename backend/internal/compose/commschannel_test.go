// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The channel resolve's translation, proven without a database for the reason
// the mailbox one is (commsjobs_test.go): a fact read as a fault leaves a
// message queued against an integration that does not exist, and a fault read as
// a fact destroys a message nothing was wrong with. The two mistakes are
// invisible in opposite directions.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// stubChannelSenders is the capture channel lookup, faked. It hands back the bot
// token itself as the credential, which is what the real resolve does — a
// channel binding has no OAuth bundle to unwrap.
type stubChannelSenders struct {
	sender connector.MessageSender
	err    error
}

func (s stubChannelSenders) ChannelSenderFor(context.Context, string) (connector.MessageSender, connector.Auth, error) {
	return s.sender, connector.Auth("bot-token"), s.err
}

type stubChannelSender struct{}

func (stubChannelSender) SendMessage(context.Context, connector.Auth, connector.ChannelMessage) (connector.SendReceipt, error) {
	return connector.SendReceipt{}, nil
}

func TestChannelResolverTranslatesOnlyTheDeploymentFacts(t *testing.T) {
	transient := errors.New("keyvault timed out")
	for _, tc := range []struct {
		name string
		from error
		want error
	}{
		{"no bot bound parks", capture.ErrNoConnection, comms.ErrNoMailbox},
		{"a capture-only connector parks", capture.ErrConnectorCannotSend, comms.ErrCannotSend},
		{"a provider this role never compiled in parks", capture.ErrConnectorNotConfigured, comms.ErrProviderNotConfigured},
		{"a transient fault passes through unchanged", transient, transient},
		// Two live bindings in one workspace is a FAULT, not a deployment fact:
		// capture refuses to guess between them, and an operator disconnecting
		// the surplus one repairs every reply still pending. Parked, those
		// replies would be destroyed by a misconfiguration that was fixed.
		{"an ambiguous binding is a fault an operator repairs", capture.ErrChannelConnectionAmbiguous, capture.ErrChannelConnectionAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := commsResolver{channels: stubChannelSenders{err: tc.from}}

			_, _, err := r.ResolveChannel(context.Background(), capture.ProviderTelegram)

			if !errors.Is(err, tc.want) {
				t.Fatalf("ResolveChannel on %v → %v, want it to match %v", tc.from, err, tc.want)
			}
			// The cause survives translation, so the job log still says what
			// capture actually answered.
			if !errors.Is(err, tc.from) {
				t.Fatalf("ResolveChannel dropped the underlying cause %v from %v", tc.from, err)
			}
			if tc.want == tc.from && (errors.Is(err, comms.ErrNoMailbox) ||
				errors.Is(err, comms.ErrCannotSend) || errors.Is(err, comms.ErrProviderNotConfigured)) {
				t.Fatalf("a fault was translated into a parking sentinel: %v", err)
			}
		})
	}
}

// A resolved binding passes through whole. There is deliberately no scope list to
// carry: a bot token holds no OAuth grant, and the dispatcher's authority gate
// reads SendsWithoutScope rather than intersecting an empty one.
func TestChannelResolverPassesAResolvedBindingThrough(t *testing.T) {
	r := commsResolver{channels: stubChannelSenders{sender: stubChannelSender{}}}

	sender, auth, err := r.ResolveChannel(context.Background(), capture.ProviderTelegram)
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if sender == nil || string(auth) != "bot-token" {
		t.Fatalf("ResolveChannel = %v/%q, want the resolved binding unchanged", sender, auth)
	}
}

// The registration this whole path rests on: without the Telegram connector on
// the registry, ChannelSenderFor finds nothing to type-assert the message seam
// off and every reply a rep writes parks as "this installation has no Telegram
// integration". A registration is exactly the kind of wiring a unit test of the
// resolver alone would never notice was missing.
func TestTheCaptureRegistryCarriesTheTelegramConnector(t *testing.T) {
	reg := NewCaptureRegistry(nil, nil, CaptureConfig{})
	for _, desc := range reg.Connectors() {
		if desc.Name == capture.ProviderTelegram {
			return
		}
	}
	t.Fatalf("the capture registry lists %v — the send path cannot resolve a bot binding without %q on it",
		reg.Connectors(), capture.ProviderTelegram)
}

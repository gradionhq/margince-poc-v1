// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The unit-transport resolve, proven without a database for the reason the core
// one is (commschannel_test.go): a deployment fact read as a fault leaves a
// message queued against a connection that is gone, and a fault read as a fact
// destroys a message nothing was wrong with. Here the two are told apart by what
// the UNIT answered — false versus an error — so this is where that contract is
// pinned.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// declaresTransport installs a composed set holding one unit that supplies one
// channel, restoring whatever the process had when the test ends.
//
// It writes the REAL registry rather than a stub, because what is under test is
// that the resolve reads the boot's own composed declarations: a fake set here
// would prove it consults something, not that it consults that.
func declaresTransport(t *testing.T, unit string, ch extension.Channel) {
	t.Helper()
	composeUnit(t, extension.Extension{
		Name: extension.Name(unit), Version: "1.0.0", Channels: []extension.Channel{ch},
	})
}

// capturedSend records what the core handed the unit, and answers what the test
// told it to. It stands in for a unit's own Send — the only seam this file is
// about — so it is a boundary double rather than a re-implementation.
type capturedSend struct {
	got     extension.OutboundMessage
	receipt extension.Receipt
	err     error
}

func (c *capturedSend) send(_ context.Context, _ extension.Runtime, msg extension.OutboundMessage) (extension.Receipt, error) {
	c.got = msg
	return c.receipt, c.err
}

// answersLive is a unit's Live, fixed.
func answersLive(live bool, err error) extension.ConnectionLiveChecker {
	return func(context.Context, extension.Runtime, extension.UserID) (bool, error) { return live, err }
}

// The boot preflight for a channel declaration, which holds the two rules
// Channel.Validate cannot: the same provider declared twice inside one unit,
// and the same provider claimed by two DIFFERENT units.
//
// Both are facts about the composed SET rather than about a declaration, and
// both refuse at BOOT rather than at the send — the send is the moment the
// mistake becomes a message somebody receives. The THIRD collision, against a
// core connector, is deliberately not here: see unitChannelFacts.
func TestThePreflightHoldsChannelDeclarationsToTheComposedSet(t *testing.T) {
	sender := &capturedSend{}
	mine := extension.Extension{
		Name: "mine", Version: "1.0.0",
		Channels: []extension.Channel{{Provider: "mine_chat", Send: sender.send, Live: answersLive(true, nil)}},
	}

	claimed := map[string]extension.Name{}
	if err := preflightChannels(mine, claimed); err != nil {
		t.Fatalf("a well-formed channel declaration was refused: %v", err)
	}
	if claimed["mine_chat"] != "mine" {
		t.Errorf("the provider was not recorded as claimed by %q; the next unit's collision would go unseen", mine.Name)
	}

	// The published grammar, reached through the preflight — so a declaration
	// that arrived outside the generator path is held to the same rule the
	// manifest generator applies.
	ungrammatical := mine
	ungrammatical.Channels = []extension.Channel{{Provider: "mine-chat"}}
	if err := preflightChannels(ungrammatical, map[string]extension.Name{}); err == nil {
		t.Error("a provider the registry grammar refuses was accepted; it would fail on the column instead, under a constraint name")
	}

	twice := mine
	twice.Channels = append(slices.Clone(mine.Channels), mine.Channels[0])
	if err := preflightChannels(twice, map[string]extension.Name{}); err == nil {
		t.Error("one provider declared twice within a unit was accepted")
	}

	// The collision that matters most of the three that live here: two units,
	// one provider, and nothing downstream able to say whose transport a
	// message travelled on.
	theirs := extension.Extension{
		Name: "theirs", Version: "1.0.0",
		Channels: []extension.Channel{{Provider: "mine_chat"}},
	}
	err := preflightChannels(theirs, claimed)
	if err == nil {
		t.Fatal("two units claiming one provider were accepted; one provider names one transport")
	}
	// Both names, because an author reading this has no other way to find the
	// other side of the clash.
	if !strings.Contains(err.Error(), "mine") || !strings.Contains(err.Error(), "theirs") {
		t.Errorf("the refusal %q names only one side of the collision", err)
	}
}

// A unit's transport is resolved instead of the capture registry's — and the
// registry is deliberately wired to answer "no such connector", which is what it
// would really say about a provider it never compiled in. Reaching it would park
// a message the installation can perfectly well send.
func TestResolveChannelPrefersTheUnitThatSuppliesTheTransport(t *testing.T) {
	sender := &capturedSend{}
	declaresTransport(t, "mine", extension.Channel{
		Provider: "mine_chat", Send: sender.send, Live: answersLive(true, nil),
	})
	r := commsResolver{channels: stubChannelSenders{err: errors.New("the capture registry was asked and should not have been")}}

	resolved, auth, err := r.ResolveChannel(context.Background(), ids.New[ids.UserKind](), "mine_chat")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if resolved == nil {
		t.Fatal("ResolveChannel returned no sender for a transport a composed unit supplies")
	}
	// No credential crosses this seam: a unit's is sealed under its own secret
	// scope, per member, and the core never holds it.
	if auth != nil {
		t.Errorf("auth = %q, want nil — a unit's credential is the unit's and does not travel through the core", auth)
	}
}

// The two deployment facts a unit transport can report, and the one fault it
// must NOT be mistaken for. A member who disconnected has nothing to retry into,
// so the delivery parks where a human can see it; a provider that could not
// answer is a failure to get an answer, and parking on one would destroy a send
// that nothing is wrong with.
func TestResolveUnitChannelTranslatesOnlyTheDeploymentFacts(t *testing.T) {
	provider := errors.New("the provider timed out")
	for _, tc := range []struct {
		name    string
		channel extension.Channel
		want    error
	}{
		{
			name:    "a capture-only transport parks",
			channel: extension.Channel{Provider: "mine_chat"},
			want:    comms.ErrCannotSend,
		},
		{
			name: "a member who disconnected parks",
			channel: extension.Channel{
				Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(false, nil),
			},
			want: comms.ErrNoMailbox,
		},
		{
			name: "a provider that could not answer is retried",
			channel: extension.Channel{
				Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(false, provider),
			},
			want: provider,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaresTransport(t, "mine", tc.channel)
			r := commsResolver{}

			_, _, err := r.ResolveChannel(context.Background(), ids.New[ids.UserKind](), "mine_chat")

			if !errors.Is(err, tc.want) {
				t.Fatalf("ResolveChannel → %v, want it to match %v", err, tc.want)
			}
			if tc.want == provider && (errors.Is(err, comms.ErrNoMailbox) ||
				errors.Is(err, comms.ErrCannotSend) || errors.Is(err, comms.ErrProviderNotConfigured)) {
				t.Fatalf("a fault was translated into a parking sentinel: %v", err)
			}
		})
	}
}

// What the core hands the unit is the delivery, unchanged where it matters and
// shifted exactly where the two surfaces differ.
func TestTheUnitSenderHandsOverTheDeliveryTheDispatcherBuilt(t *testing.T) {
	sender := &capturedSend{receipt: extension.Receipt{ProviderMessageID: "provider-42"}}
	member := ids.New[ids.UserKind]()
	declaresTransport(t, "mine", extension.Channel{
		Provider: "mine_chat", Send: sender.send, Live: answersLive(true, nil),
	})
	r := commsResolver{}
	resolved, _, err := r.ResolveChannel(context.Background(), member, "mine_chat")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}

	receipt, err := resolved.SendMessage(context.Background(), nil, connector.ChannelMessage{
		Recipient:      connector.ChannelIdentity{Provider: "mine_chat", ChannelUserID: "acct-7", Username: "handle"},
		Body:           "the reply",
		ReplyTo:        "upstream-9",
		IdempotencyKey: "delivery-1",
		Attempt:        0,
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got := sender.got
	// WHOSE credential transmits is the whole point of this seam.
	if string(got.Member) != member.String() {
		t.Errorf("Member = %q, want the delivery's own sending member %q — a message must not leave under somebody else's connection", got.Member, member)
	}
	if got.Recipient.ChannelUserID != "acct-7" || got.Recipient.Provider != "mine_chat" {
		t.Errorf("Recipient = %+v, want the provider and account id the dispatcher resolved", got.Recipient)
	}
	// The handle is deliberately NOT carried: it can be released and re-claimed,
	// so nothing may route on it.
	if got.Recipient.DisplayName != "" {
		t.Errorf("the display handle %q crossed the seam; a re-claimable handle must not reach a routing decision", got.Recipient.DisplayName)
	}
	if got.Body != "the reply" || got.ReplyTo != "upstream-9" || got.IdempotencyKey != "delivery-1" {
		t.Errorf("the message was altered in transit: %+v", got)
	}
	// The seam counts retries from 0 and the published surface from 1.
	if got.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1 for a first transmission — a unit logging attempt 0 reads as a repeat", got.Attempt)
	}
	if receipt.ProviderMessageID != "provider-42" {
		t.Errorf("ProviderMessageID = %q, want the unit's own receipt — it is what makes a later reply anchorable", receipt.ProviderMessageID)
	}
	// A channel message carries no RFC822 identity, which is why the seam keys
	// retry safety on the idempotency key instead.
	if receipt.RFC822MessageID != "" {
		t.Errorf("RFC822MessageID = %q; a channel message has none", receipt.RFC822MessageID)
	}
}

// The one refusal class that crosses the seam, and the reason it is the only
// one: the dispatcher records a channel transmission as in flight BEFORE the
// call and then treats every error that is NOT ErrSendOutcomeUnknown as a
// definite answer from the provider — proof that nothing went out — clearing
// the marker and putting the delivery back on the ladder.
//
// A channel has no prior-send lookup, so a POST whose answer was lost and
// reported as an ordinary failure delivers the rep's message twice, with
// nothing able to detect it. Anything else must NOT be translated, or a refusal
// a retry would have recovered from stops the delivery instead.
func TestOnlyAnUnknownOutcomeStopsTheDeliveryRatherThanRetryingIt(t *testing.T) {
	ordinary := errors.New("the provider refused this message")
	for name, tc := range map[string]struct {
		from error
		stop bool
	}{
		"a lost answer stops the delivery":     {from: extension.ErrSendOutcomeUnknown, stop: true},
		"an ordinary refusal rides the ladder": {from: ordinary},
	} {
		t.Run(name, func(t *testing.T) {
			sender := &capturedSend{err: tc.from}
			declaresTransport(t, "mine", extension.Channel{
				Provider: "mine_chat", Send: sender.send, Live: answersLive(true, nil),
			})
			r := commsResolver{}
			resolved, _, err := r.ResolveChannel(context.Background(), ids.New[ids.UserKind](), "mine_chat")
			if err != nil {
				t.Fatalf("ResolveChannel: %v", err)
			}

			_, err = resolved.SendMessage(context.Background(), nil, connector.ChannelMessage{
				Recipient: connector.ChannelIdentity{Provider: "mine_chat", ChannelUserID: "acct-7"},
				Body:      "the reply", IdempotencyKey: "delivery-1",
			})

			if got := errors.Is(err, connector.ErrSendOutcomeUnknown); got != tc.stop {
				t.Errorf("ErrSendOutcomeUnknown = %v, want %v — got %v", got, tc.stop, err)
			}
			// The unit's own cause survives translation either way, so the
			// delivery's log still says what the unit actually answered.
			if !errors.Is(err, tc.from) {
				t.Errorf("the underlying cause %v was dropped from %v", tc.from, err)
			}
		})
	}
}

// A provider no composed unit supplies falls through to the capture registry, or
// a core connector's own transport would stop resolving the moment any unit
// declared any channel.
func TestResolveChannelStillReachesTheCoreRegistryForACoreTransport(t *testing.T) {
	declaresTransport(t, "mine", extension.Channel{
		Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})
	r := commsResolver{channels: stubChannelSenders{sender: stubChannelSender{}}}

	sender, auth, err := r.ResolveChannel(context.Background(), ids.New[ids.UserKind](), "telegram")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if sender == nil || string(auth) != "bot-token" {
		t.Fatalf("ResolveChannel = %v/%q, want the core binding unchanged", sender, auth)
	}
}

// A unit is never handed files it has nowhere to put.
//
// extension.OutboundMessage has no file field, so a message with files reaching
// a unit could only go out as text that lies about its contents. The refusal is
// at the adapter rather than only at the carriage gate because the gate reads a
// capability the unit declares — and a unit declaring carriage the published
// surface cannot yet express is exactly the mistake this catches.
func TestAUnitIsRefusedAMessageCarryingFilesItCannotPutAnywhere(t *testing.T) {
	sender := &capturedSend{receipt: extension.Receipt{ProviderMessageID: "provider-42"}}
	declaresTransport(t, "mine", extension.Channel{
		Provider: "mine_chat", Send: sender.send, Live: answersLive(true, nil),
	})
	resolved, _, err := commsResolver{}.ResolveChannel(context.Background(), ids.New[ids.UserKind](), "mine_chat")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}

	_, err = resolved.SendMessage(context.Background(), nil, connector.ChannelMessage{
		Recipient:      connector.ChannelIdentity{Provider: "mine_chat", ChannelUserID: "acct-7"},
		Body:           "the reply",
		IdempotencyKey: "delivery-1",
		Files:          []connector.OutboundFile{{AttachmentID: "a-1", Filename: "quote.pdf", Body: []byte("bytes")}},
	})
	if !errors.Is(err, connector.ErrFilesNotCarried) {
		t.Fatalf("SendMessage with files → %v, want ErrFilesNotCarried", err)
	}
	if sender.got.Body != "" {
		t.Errorf("the unit was handed %q despite carrying none of the message's files", sender.got.Body)
	}
}

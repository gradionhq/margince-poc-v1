// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The send-side resolve for a channel connection over a real migrated Postgres
// and a real vault. What it proves cannot be proven against a mock: the lookup
// is keyed on the WORKSPACE rather than on a user id, it selects only a live
// connection, and the credential it hands back is the very bot token connect
// sealed — a resolve that returned the wrong bot's token would transmit from a
// bot the customer never opened a chat with, which Telegram refuses after the
// rep has already written the message.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// channelSendConnector is a channel connector that CAN transmit — the seam
// ChannelSenderFor type-asserts for. It answers no messages of its own: the
// resolve is what is under test, not the provider call.
type channelSendConnector struct{ sent []connector.ChannelMessage }

func (c *channelSendConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: capture.ProviderTelegram, Version: "fixture"}
}

func (c *channelSendConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return nil, errors.New("channelSendConnector: a bot binding is not authenticated through the registry")
}

func (c *channelSendConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (c *channelSendConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, nil
}

func (c *channelSendConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

func (c *channelSendConnector) SendMessage(_ context.Context, _ connector.Auth, m connector.ChannelMessage) (connector.SendReceipt, error) {
	c.sent = append(c.sent, m)
	return connector.SendReceipt{ProviderMessageID: "4321"}, nil
}

var _ connector.MessageSender = (*channelSendConnector)(nil)

// captureOnlyChannelConnector registers under the same provider name and
// implements no send seam — the capture-only case a send path must be able to
// name rather than read as an outage. It is spelled out rather than embedding
// the sender above, because embedding would inherit SendMessage and the case
// would silently test the opposite of what it says.
type captureOnlyChannelConnector struct{}

func (captureOnlyChannelConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: capture.ProviderTelegram, Version: "capture-only"}
}

func (captureOnlyChannelConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return nil, errors.New("captureOnlyChannelConnector: a bot binding is not authenticated through the registry")
}

func (captureOnlyChannelConnector) Sync(_ context.Context, _ connector.Auth, cursor connector.Cursor, _ connector.Sink) (connector.Cursor, error) {
	return cursor, nil
}

func (captureOnlyChannelConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, nil
}

func (captureOnlyChannelConnector) HealthCheck(context.Context, connector.Auth) error { return nil }

// channelSendRegistry builds a Registry over the SAME pool and the SAME vault
// the channel fixture connected through, so the token it resolves is the one
// connect actually sealed.
func channelSendRegistry(t *testing.T, f *channelFixture, c connector.Connector) *capture.Registry {
	t.Helper()
	_, pool := setupCaptureDB(t)
	reg := capture.NewRegistry(pool, nil, fixtureAuthority{}, f.vault)
	if c != nil {
		reg.Register(c)
	}
	return reg
}

// connectChannel binds one fresh bot and returns its BotFather token.
func connectChannel(t *testing.T, f *channelFixture, username string) string {
	t.Helper()
	token, _ := f.api.withNewBot(username)
	if _, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return token
}

func TestChannelSenderForResolvesTheWorkspacesBotToken(t *testing.T) {
	f := newChannelFixture(t, nil)
	token := connectChannel(t, f, "sendbot")
	sender := &channelSendConnector{}
	reg := channelSendRegistry(t, f, sender)

	got, auth, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram)
	if err != nil {
		t.Fatalf("ChannelSenderFor: %v", err)
	}
	if got == nil {
		t.Fatal("no MessageSender resolved for a live channel connection")
	}
	if string(auth) != token {
		t.Fatalf("resolved credential = %q, want the sealed bot token %q", string(auth), token)
	}
	// The resolve is keyed on the workspace alone: it names no user, and there
	// is no capture_connection row for this human at all.
	var connections int
	owner, _ := setupCaptureDB(t)
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM capture_connection WHERE workspace_id = $1`, f.ws).Scan(&connections); err != nil {
		t.Fatal(err)
	}
	if connections != 0 {
		t.Fatalf("the fixture holds %d capture_connection rows; this resolve must not be reading one", connections)
	}
}

// A workspace that bound no bot, and one whose binding was withdrawn, read the
// same way: there is nothing live to send through. The withdrawn case matters on
// its own because the row survives — archived, status disconnected — and a resolve
// that read it would transmit through a token the vault no longer holds.
func TestChannelSenderForReportsNoConnectionForAnythingNotLive(t *testing.T) {
	t.Run("no binding at all", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		reg := channelSendRegistry(t, f, &channelSendConnector{})
		if _, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrNoConnection) {
			t.Fatalf("ChannelSenderFor = %v, want ErrNoConnection", err)
		}
	})

	t.Run("a binding the operator withdrew", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		connectChannel(t, f, "withdrawnbot")
		live := f.liveConnections(t)
		if len(live) != 1 {
			t.Fatalf("the fixture holds %d live connections, want exactly 1", len(live))
		}
		if err := f.store.Disconnect(f.ctx, live[0].ID); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
		reg := channelSendRegistry(t, f, &channelSendConnector{})
		if _, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrNoConnection) {
			t.Fatalf("ChannelSenderFor on a withdrawn binding = %v, want ErrNoConnection", err)
		}
	})
}

// The two other deployment facts, each of which must be NAMED rather than read
// as an outage: a caller's response to a fact is to stop trying.
func TestChannelSenderForNamesTheDeploymentFacts(t *testing.T) {
	t.Run("a connector this role never registered", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		connectChannel(t, f, "unregistered")
		reg := channelSendRegistry(t, f, nil)
		if _, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrConnectorNotConfigured) {
			t.Fatalf("ChannelSenderFor = %v, want ErrConnectorNotConfigured", err)
		}
	})

	t.Run("a connector that captures only", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		connectChannel(t, f, "captureonly")
		reg := channelSendRegistry(t, f, captureOnlyChannelConnector{})
		if _, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrConnectorCannotSend) {
			t.Fatalf("ChannelSenderFor = %v, want ErrConnectorCannotSend", err)
		}
	})
}

// The send resolver refuses to guess between two live bindings, and F22 makes that
// state unreachable: uq_channel_connection_ws permits ONE live row per
// (workspace, provider). This is the test of that index rather than of the
// refusal, because the index is what an operator actually meets — a second bot is
// refused at connect, not silently bound and then discovered when a rep's reply
// vanishes.
//
// The refusal in liveChannelBinding stays as the second line: it reads the rows
// rather than trusting a constraint it cannot see, and picking the planner's first
// row would send a customer's reply through a bot they never opened a chat with.
func TestASecondLiveBindingCannotBeCreatedForTheResolveToGuessBetween(t *testing.T) {
	f := newChannelFixture(t, nil)
	connectChannel(t, f, "firstbot")

	// Not merely a refusal from the store: the INDEX is asserted, by writing
	// straight past the application on the owner connection.
	owner, _ := setupCaptureDB(t)
	_, err := owner.Exec(context.Background(), `
		INSERT INTO channel_connection
		  (workspace_id, provider, channel_id, channel_label, credential_ref, status, connected_by)
		SELECT workspace_id, provider, channel_id || '9', channel_label, credential_ref, status, connected_by
		  FROM channel_connection WHERE workspace_id = $1 AND archived_at IS NULL`, f.ws)
	if err == nil {
		t.Fatal("a second live binding was written for this workspace — every reply would then be ambiguous and the resolver would refuse to send at all")
	}
	if constraint, unique := storekit.UniqueViolation(err); !unique || constraint != "uq_channel_connection_ws" {
		t.Fatalf("the second binding failed on %v, want the uq_channel_connection_ws unique index", err)
	}

	// And the one binding still resolves, so the index bounds the state without
	// breaking the lookup.
	reg := channelSendRegistry(t, f, &channelSendConnector{})
	if _, auth, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); err != nil || len(auth) == 0 {
		t.Fatalf("the sole binding resolved (%v, %d bytes of credential)", err, len(auth))
	}
}

// The window between resolving a credential and spending it is real: the
// dispatcher unseals the token first and then walks the seat, consent and pacing
// gates, several database round trips before the provider is called. An admin
// who replaces the bot inside that window has withdrawn the resolved one —
// Telegram deleted its webhook, and a reply transmitted through it lands in a
// chat nothing will ever read an answer from, or is refused outright once the
// token itself was rotated.
//
// The refusal must be TRANSIENT rather than one of the deployment facts: the
// workspace has a live bot, it is simply a different one, and the retry that
// follows resolves it.
func TestASendRefusesAfterItsBotWasReplaced(t *testing.T) {
	f := newChannelFixture(t, nil)
	connectChannel(t, f, "outgoingbot")
	provider := &channelSendConnector{}
	reg := channelSendRegistry(t, f, provider)

	sender, auth, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram)
	if err != nil {
		t.Fatalf("ChannelSenderFor: %v", err)
	}

	// The admin swaps the bot while the delivery is still walking the gates.
	live := f.liveConnections(t)
	if len(live) != 1 {
		t.Fatalf("the fixture holds %d live connections, want exactly 1", len(live))
	}
	incoming, _ := f.api.withNewBot("incomingbot")
	if err := f.store.ReplaceToken(f.ctx, live[0].ID, incoming); err != nil {
		t.Fatalf("ReplaceToken: %v", err)
	}

	_, err = sender.SendMessage(f.ctx, auth, connector.ChannelMessage{
		Recipient:      connector.ChannelIdentity{Provider: capture.ProviderTelegram, ChannelUserID: "778899"},
		Body:           "On its way today.",
		IdempotencyKey: "01997f0e-0000-7000-8000-00000000f00d",
	})
	if !errors.Is(err, capture.ErrChannelBindingReplaced) {
		t.Fatalf("SendMessage through the replaced bot = %v, want ErrChannelBindingReplaced", err)
	}
	if len(provider.sent) != 0 {
		t.Fatalf("the withdrawn bot transmitted %d message(s); the reply went to a chat whose webhook is gone", len(provider.sent))
	}
	for _, fact := range []error{capture.ErrNoConnection, capture.ErrConnectorCannotSend, capture.ErrConnectorNotConfigured} {
		if errors.Is(err, fact) {
			t.Fatalf("a replaced binding was reported as the deployment fact %v; the delivery would park instead of retrying", fact)
		}
	}

	// The proof that the fence tracks the binding rather than refusing forever:
	// resolving again picks up the replacement and transmits through it.
	sender, auth, err = reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram)
	if err != nil {
		t.Fatalf("re-resolving after the replacement: %v", err)
	}
	if string(auth) != incoming {
		t.Fatalf("the re-resolved credential is not the incoming bot's token")
	}
	if _, err := sender.SendMessage(f.ctx, auth, connector.ChannelMessage{
		Recipient:      connector.ChannelIdentity{Provider: capture.ProviderTelegram, ChannelUserID: "778899"},
		Body:           "On its way today.",
		IdempotencyKey: "01997f0e-0000-7000-8000-00000000f00e",
	}); err != nil {
		t.Fatalf("SendMessage through the replacement: %v", err)
	}
	if len(provider.sent) != 1 {
		t.Fatalf("the provider saw %d message(s), want the one sent through the replacement", len(provider.sent))
	}
}

// RLS scopes the resolve: another workspace's live binding is not this
// workspace's sender, whatever the global bot index says.
func TestChannelSenderForDoesNotReachAnotherWorkspacesBinding(t *testing.T) {
	api := newFakeTelegram()
	bound := newChannelFixture(t, api)
	connectChannel(t, bound, "otherworkspacebot")

	unbound := newChannelFixture(t, api)
	reg := channelSendRegistry(t, unbound, &channelSendConnector{})
	if _, _, err := reg.ChannelSenderFor(unbound.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrNoConnection) {
		t.Fatalf("ChannelSenderFor across workspaces = %v, want ErrNoConnection", err)
	}
	// The other workspace still resolves its own, so the isolation is a scope
	// and not a broken lookup.
	boundReg := channelSendRegistry(t, bound, &channelSendConnector{})
	if _, auth, err := boundReg.ChannelSenderFor(bound.ctx, capture.ProviderTelegram); err != nil || len(auth) == 0 {
		t.Fatalf("the owning workspace resolved (%v, %d bytes of credential)", err, len(auth))
	}
}

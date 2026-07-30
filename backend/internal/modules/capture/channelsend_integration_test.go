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

// A workspace that bound no bot, and one whose registration never completed,
// read the same way: there is nothing live to send through. A `pending` row is
// deliberately NOT sendable — its webhook call failed, so the bot is unreachable
// in both directions and an operator has not been told the binding is broken.
func TestChannelSenderForReportsNoConnectionForAnythingNotLive(t *testing.T) {
	t.Run("no binding at all", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		reg := channelSendRegistry(t, f, &channelSendConnector{})
		if _, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrNoConnection) {
			t.Fatalf("ChannelSenderFor = %v, want ErrNoConnection", err)
		}
	})

	t.Run("a registration that never completed", func(t *testing.T) {
		f := newChannelFixture(t, nil)
		token, _ := f.api.withNewBot("halfbot")
		f.api.setWebhookErr = errors.New("telegram unreachable")
		if _, err := f.store.Connect(f.ctx, capture.ConnectRequest{
			Provider: capture.ProviderTelegram, BotToken: token,
		}); err == nil {
			t.Fatal("Connect succeeded despite a failed setWebhook")
		}
		reg := channelSendRegistry(t, f, &channelSendConnector{})
		if _, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram); !errors.Is(err, capture.ErrNoConnection) {
			t.Fatalf("ChannelSenderFor on a pending binding = %v, want ErrNoConnection", err)
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

// Two live bindings for one provider in one workspace is a state the schema
// permits — the unique indexes bind (workspace, provider, bot) and
// (provider, bot), never (workspace, provider). The resolve REFUSES rather than
// picking: replying through a bot the customer never opened a chat with is
// refused by Telegram, so a guess turns into a message that silently never
// arrives.
func TestChannelSenderForRefusesToGuessBetweenTwoLiveBindings(t *testing.T) {
	f := newChannelFixture(t, nil)
	connectChannel(t, f, "firstbot")
	connectChannel(t, f, "secondbot")
	reg := channelSendRegistry(t, f, &channelSendConnector{})

	_, _, err := reg.ChannelSenderFor(f.ctx, capture.ProviderTelegram)
	if !errors.Is(err, capture.ErrChannelConnectionAmbiguous) {
		t.Fatalf("ChannelSenderFor = %v, want ErrChannelConnectionAmbiguous", err)
	}
	// And it is NOT one of the deployment facts: an operator can repair this,
	// and a delivery that parked on it would need re-sending by hand.
	for _, fact := range []error{capture.ErrNoConnection, capture.ErrConnectorCannotSend, capture.ErrConnectorNotConfigured} {
		if errors.Is(err, fact) {
			t.Fatalf("an ambiguous binding was reported as the deployment fact %v", fact)
		}
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

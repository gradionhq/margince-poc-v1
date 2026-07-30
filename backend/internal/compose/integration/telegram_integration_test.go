// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The Telegram channel acceptance suite (telegram-oa design §12, TG-CR-3's
// AC-TG-1…6). It is the last gate before a real bot is pointed at this code:
// nobody can exercise the live channel until this merges, so every claim in
// these four files is a fact read back out of a real migrated Postgres or off
// the real HTTP router — never a fact about a mock's own bookkeeping.
//
// This file holds the two connect-side criteria: what binding a bot writes and
// seals, and what an unauthenticated delivery is told. The shared fixture is in
// telegram_fixture_integration_test.go.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestAC_TG_1_ConnectValidatesSealsAndRecordsAuditOnly is AC-TG-1 whole: the
// bot is validated against the provider BEFORE anything is stored, both
// secrets are sealed in the vault, the minted webhook secret is the one
// Telegram was registered with, and the acting admin is captured for audit and
// nothing else — the connection belongs to the workspace, not to them.
func TestAC_TG_1_ConnectValidatesSealsAndRecordsAuditOnly(t *testing.T) {
	c := setupTelegram(t)

	// Observed from inside getMe: the one moment at which "before anything is
	// stored" is a checkable claim rather than an ordering comment.
	rowsWhenValidated := -1
	c.api.onGetMe = func() { rowsWhenValidated = c.count(t, `SELECT count(*) FROM channel_connection`) }

	c.connectBot(t)

	if rowsWhenValidated != 0 {
		t.Fatalf("%d channel_connection rows existed when the token was validated, want 0 — "+
			"getMe must run before anything is written", rowsWhenValidated)
	}
	if got, want := c.api.callOrder(), []string{"getMe", "getWebhookInfo", "setWebhook"}; !slices.Equal(got, want) {
		t.Fatalf("Bot API call order = %v, want %v", got, want)
	}

	if c.conn.Status != "connected" {
		t.Fatalf("connection status = %q, want connected", c.conn.Status)
	}
	if c.conn.ChannelID != fmt.Sprintf("%d", telegramBotID) || c.conn.ChannelLabel != telegramBotUser {
		t.Fatalf("connection identifies bot %q/%q, want the id and username getMe reported (%d/%s)",
			c.conn.ChannelID, c.conn.ChannelLabel, telegramBotID, telegramBotUser)
	}

	// The webhook Telegram was told about must be the route this installation
	// actually serves, carrying THIS connection's id: registering any other URL
	// leaves the row reading `connected` while every delivery 401s.
	wantURL := telegramWebhookBase + "/webhooks/telegram/" + c.conn.ID.String()
	if c.api.gotWebhookURL != wantURL {
		t.Fatalf("registered webhook URL = %q, want %q", c.api.gotWebhookURL, wantURL)
	}
	if got, want := c.api.gotAllowedUpdates, []string{"message", "my_chat_member"}; !slices.Equal(got, want) {
		t.Fatalf("registered allowed_updates = %v, want %v", got, want)
	}

	c.assertSecretsSealed(t)
	c.assertConnectIsAuditOnly(t)
	c.assertConnectionIsWorkspaceOwned(t)
}

// assertSecretsSealed reads the row's two vault refs and unseals both. The bot
// token must be recoverable (the send path resolves it) and the webhook secret
// must be exactly what Telegram was registered with (the ingress path compares
// against it) — if the minted secret and the registered secret ever diverged,
// every delivery would be refused and nothing else in this suite would notice.
func (c *telegramEnv) assertSecretsSealed(t *testing.T) {
	t.Helper()
	var credentialRef, secretRef string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT credential_ref, webhook_secret_ref FROM channel_connection WHERE id = $1`,
			c.conn.ID).Scan(&credentialRef, &secretRef)
	}); err != nil {
		t.Fatalf("reading the connection's vault refs: %v", err)
	}
	if credentialRef == telegramBotToken || secretRef == c.secret {
		t.Fatal("a vault ref holds the plaintext it is supposed to address")
	}

	ws := ids.From[ids.WorkspaceKind](c.workspaceID(t))
	ctx := context.Background()
	token, err := c.vault.Get(ctx, ws, keyvault.Ref(credentialRef))
	if err != nil {
		t.Fatalf("unsealing the bot token: %v", err)
	}
	if string(token) != telegramBotToken {
		t.Fatalf("sealed bot token = %q, want the token Connect was given", token)
	}
	secret, err := c.vault.Get(ctx, ws, keyvault.Ref(secretRef))
	if err != nil {
		t.Fatalf("unsealing the webhook secret: %v", err)
	}
	if string(secret) != c.secret {
		t.Fatal("the sealed webhook secret is not the one Telegram was registered with — every delivery would be refused")
	}
}

// assertConnectIsAuditOnly holds the write posture the closed event catalog
// forces: a channel connection has an audit trail and no event half, and the
// trail names the acting human without ever re-holding the credentials.
func (c *telegramEnv) assertConnectIsAuditOnly(t *testing.T) {
	t.Helper()
	if n := c.count(t, `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'channel_connection' AND entity_id = $1 AND actor_id = $2`,
		c.conn.ID, "human:"+c.admin); n != 2 {
		t.Errorf("%d audit rows name the acting admin for this connection, want 2 (the pending create and the connected flip)", n)
	}
	if n := c.count(t, `
		SELECT count(*) FROM event_outbox WHERE envelope::text LIKE '%' || $1::text || '%'`,
		c.conn.ID); n != 0 {
		t.Error("the connect emitted an outbox event; the closed event catalog defines no verb for a channel connection, so the write is audit-only")
	}
	// The audit spine must not become a second custodian of the credentials.
	if n := c.count(t, `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'channel_connection' AND entity_id = $1
		   AND (before::text LIKE '%' || $2 || '%' OR after::text LIKE '%' || $2 || '%')`,
		c.conn.ID, telegramBotToken); n != 0 {
		t.Error("the audit trail re-stores the bot token")
	}
}

// assertConnectionIsWorkspaceOwned is the "never as an owner" half of AC-TG-1,
// held two ways. Structurally: the table has no owner column at all, so no
// later read can start scoping these rows to the admin who ran connect.
// Behaviourally: a DIFFERENT human, on a team-bounded row scope and holding
// only read, sees the binding — because a workspace bot serves every seat.
func (c *telegramEnv) assertConnectionIsWorkspaceOwned(t *testing.T) {
	t.Helper()
	if n := c.count(t, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'channel_connection' AND column_name IN ('owner_id', 'user_id')`); n != 0 {
		t.Error("channel_connection carries an owner column; a workspace-level bot binding has no owner (design D2)")
	}

	live, err := c.channelStore().List(c.strangerRepCtx(t,
		map[string]principal.ObjectGrant{"channel_connection": {Read: true}}))
	if err != nil {
		t.Fatalf("a rep listing the workspace's channels: %v", err)
	}
	if len(live) != 1 || live[0].ID != c.conn.ID {
		t.Fatalf("a rep on a team row scope sees %d channel connections, want the workspace's 1", len(live))
	}

	// And the read reaches them over the composed router too: the transport
	// handler has to shadow its generated 501 stub, or the settings screen
	// calls an endpoint that answers "not implemented".
	var listed struct {
		Data []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Provider string `json:"provider"`
		} `json:"data"`
	}
	if status := c.call(t, "GET", "/v1/channel-connections", nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("GET /v1/channel-connections → %d, want 200 (501 means the transport is not wired)", status)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != c.conn.ID.String() ||
		listed.Data[0].Status != "connected" || listed.Data[0].Provider != "telegram" {
		t.Fatalf("the channel list served %+v, want the one connected telegram binding", listed.Data)
	}
}

// TestAC_TG_2_WrongSecretIsRefusedAndCapturesNothing is AC-TG-2: a delivery
// whose secret does not match is refused with no body detail and captures
// nothing. Every refusal shape answers identically — wrong secret, absent
// header, unknown connection id — so an attacker probing this unauthenticated
// edge learns neither which connections exist nor which of their guesses was
// closer.
//
// The accepted delivery at the end is not a bonus case: without it a 401 could
// equally mean the route is not mounted at all, and every refusal above would
// pass against an installation that captures nothing from anybody.
func TestAC_TG_2_WrongSecretIsRefusedAndCapturesNothing(t *testing.T) {
	c := setupTelegramConnected(t)
	refused := telegramUpdate{updateID: 5101, messageID: 11, senderID: 770101, username: "prober", text: "let me in"}

	for _, probe := range []struct {
		name         string
		connectionID string
		secret       string
	}{
		{"a wrong secret", c.conn.ID.String(), "not-the-registered-secret"},
		{"no secret header at all", c.conn.ID.String(), ""},
		{"the right secret against an unknown connection", ids.NewV7().String(), c.secret},
		{"a connection id that is not a uuid", "not-a-uuid", c.secret},
	} {
		status, body := c.post(t, probe.connectionID, probe.secret, refused.body(t))
		if status != http.StatusUnauthorized {
			t.Fatalf("%s → %d, want 401", probe.name, status)
		}
		if body != "" {
			t.Fatalf("%s answered with a body (%q); a refusal must name nothing", probe.name, body)
		}
	}

	if n := c.rawCaptures(t, refused.updateID); n != 0 {
		t.Fatalf("%d raw captures stored behind refused deliveries, want 0", n)
	}
	if n := c.ingestJobs(t); n != 0 {
		t.Fatalf("%d ingest jobs enqueued behind refused deliveries, want 0", n)
	}

	accepted := telegramUpdate{updateID: 5102, messageID: 12, senderID: 770102, username: "buyer", text: "hello"}
	if status, _ := c.deliver(t, accepted); status != http.StatusOK {
		t.Fatalf("a delivery carrying the registered secret → %d, want 200 — "+
			"the refusals above prove nothing if this route refuses everyone", status)
	}
	if n := c.rawCaptures(t, accepted.updateID); n != 1 {
		t.Fatalf("%d raw captures stored for the accepted delivery, want 1", n)
	}
	if n := c.ingestJobs(t); n != 1 {
		t.Fatalf("%d ingest jobs enqueued for the accepted delivery, want 1", n)
	}
}

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
// This file holds the connect-side criterion: what binding a bot writes and
// seals. AC-TG-2's twin — what an unauthenticated delivery is told — has no
// subject any more: ingress polls, so this installation exposes no inbound
// endpoint for anyone to authenticate against. The shared fixture is in
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
// bot is validated against the provider BEFORE anything is stored, its token is
// sealed in the vault, and the acting admin is captured for audit and nothing
// else — the connection belongs to the workspace, not to them.
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
	// getMe first, then the webhook clear that makes the bot pollable at all —
	// and nothing after, because a pull ingress has no registration to make.
	if got, want := c.api.callOrder(), []string{"getMe", "deleteWebhook"}; !slices.Equal(got, want) {
		t.Fatalf("Bot API call order = %v, want %v", got, want)
	}

	if c.conn.Status != "connected" {
		t.Fatalf("connection status = %q, want connected", c.conn.Status)
	}
	if c.conn.ChannelID != fmt.Sprintf("%d", telegramBotID) || c.conn.ChannelLabel != telegramBotUser {
		t.Fatalf("connection identifies bot %q/%q, want the id and username getMe reported (%d/%s)",
			c.conn.ChannelID, c.conn.ChannelLabel, telegramBotID, telegramBotUser)
	}

	c.assertTokenSealed(t)
	c.assertConnectIsAuditOnly(t)
	c.assertConnectionIsWorkspaceOwned(t)
}

// assertTokenSealed reads the row's vault ref and unseals it. The bot token must be
// recoverable, because it is the ONE credential both halves of the channel spend:
// the poll authenticates with it and the send path resolves it. One secret per
// connection is the whole shape now — there is no second value to mint, register,
// rotate or destroy.
func (c *telegramEnv) assertTokenSealed(t *testing.T) {
	t.Helper()
	var credentialRef string
	if err := inWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT credential_ref FROM channel_connection WHERE id = $1`, c.conn.ID).Scan(&credentialRef)
	}); err != nil {
		t.Fatalf("reading the connection's vault ref: %v", err)
	}
	if credentialRef == telegramBotToken {
		t.Fatal("the vault ref holds the plaintext it is supposed to address")
	}
	// Structurally, not merely by count: a column that still existed would invite a
	// second custodian back in.
	if n := c.count(t, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'channel_connection' AND column_name = 'webhook_secret_ref'`); n != 0 {
		t.Error("channel_connection still carries webhook_secret_ref; a polled channel authenticates nothing inbound")
	}

	ws := ids.From[ids.WorkspaceKind](c.workspaceID(t))
	token, err := c.vault.Get(context.Background(), ws, keyvault.Ref(credentialRef))
	if err != nil {
		t.Fatalf("unsealing the bot token: %v", err)
	}
	if string(token) != telegramBotToken {
		t.Fatalf("sealed bot token = %q, want the token Connect was given", token)
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
		c.conn.ID, "human:"+c.admin); n != 1 {
		t.Errorf("%d audit rows name the acting admin for this connection, want 1 — a connect is one atomic step, so there is no second transition to record", n)
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
	if status := c.Call(t, "GET", "/v1/channel-connections", nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("GET /v1/channel-connections → %d, want 200 (501 means the transport is not wired)", status)
	}
	if len(listed.Data) != 1 || listed.Data[0].ID != c.conn.ID.String() ||
		listed.Data[0].Status != "connected" || listed.Data[0].Provider != "telegram" {
		t.Fatalf("the channel list served %+v, want the one connected telegram binding", listed.Data)
	}
}

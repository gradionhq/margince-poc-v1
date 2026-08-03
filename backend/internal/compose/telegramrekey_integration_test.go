// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What a polled update's identity is pinned to, held against the one supported
// operation that moves it: replacing a live connection's bot token. It rides the
// ingress fixture in telegrampollfixture_integration_test.go and lives apart from
// it because the claim is about the CONNECTION lifecycle crossing an already-read
// update, not about the poll's own durability.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// replaceTestTelegramBot runs the REAL ReplaceToken (a supported admin operation)
// to point a live connection at a DIFFERENT bot, over the same fake provider
// connect used. A hand-written UPDATE of channel_id would model the mutation but
// not the operation, and it is the operation this system has to survive.
func replaceTestTelegramBot(t *testing.T, e *integration.Env, vault keyvault.Vault, connID ids.UUID, botID int64, username string) {
	t.Helper()
	api := &telegramPollFakeAPI{bot: telegram.Bot{ID: botID, Username: username}}
	store := capture.NewChannelStore(e.Pool, vault, api, quietTestLogger())
	err := store.ReplaceToken(telegramAdminContext(e.WS, e.Rep1), connID,
		fmt.Sprintf("%d:AAH-fixture-secret-for-%s", botID, username))
	if err != nil {
		t.Fatalf("ReplaceToken: %v", err)
	}
	if api.deleteWebhooks == 0 {
		t.Fatal("ReplaceToken did not clear the incoming bot's webhook — Telegram would refuse to poll it")
	}
}

// telegramActivityKeys reads back every captured Telegram activity's natural
// key and thread key — both are bot-namespaced, and both are what a late bot
// resolution would rewrite.
func telegramActivityKeys(t *testing.T, e *integration.Env) (sourceID, threadKey string) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT source_id, thread_key FROM activity WHERE source_system = 'telegram'`).Scan(&sourceID, &threadKey)
	}); err != nil {
		t.Fatalf("reading back the captured activity's keys: %v", err)
	}
	return sourceID, threadKey
}

// An admin may swap a live connection onto a different bot at any moment, and
// that moment can land between an update being read and its ingest job running.
// The message's identity must not move with it.
//
// Resolved from the connection row at job time, the already-stored update is
// re-keyed onto the NEW bot: it is filed into that bot's conversation thread,
// and — because Telegram's message ids restart per chat per bot — its natural
// key can equal a real message of the new bot's, whereupon the Sink's
// idempotent upsert merges two different customers' messages into one activity.
// Pinned by the poll that read it, the bot that actually received the update
// travels with it and none of that is reachable.
func TestATokenReplacementBetweenPollAndIngestKeepsThePollingBotsKeys(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	const (
		updateID  = int64(5004)
		senderID  = int64(770604)
		messageID = int64(64)
	)
	api := &telegramPollFakeAPI{
		bot:  telegram.Bot{ID: 91000006, Username: "bot_before_swap"},
		held: []json.RawMessage{telegramPrivateUpdate(updateID, senderID, messageID, "before the swap")},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 91000006, "bot_before_swap")

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the arranging poll: %v", err)
	}
	enqueued := telegramEnqueuedRawIDs(t, e, conn.ID.String())
	if len(enqueued) != 1 {
		t.Fatalf("%d ingest jobs after one polled update, want 1", len(enqueued))
	}

	// The swap lands while the job is still queued.
	const swappedBotID = int64(91000007)
	replaceTestTelegramBot(t, e, vault, conn.ID, swappedBotID, "bot_after_swap")
	if live := e.WsCount(t,
		`SELECT count(*) FROM channel_connection WHERE id = $1 AND channel_id = $2`,
		conn.ID, fmt.Sprintf("%d", swappedBotID)); live != 1 {
		t.Fatalf("the connection row does not name the swapped-in bot — the arrange step did not take effect")
	}

	workOneIngestJob(t, e, newTelegramIngestWorker(e.Pool, CaptureConfig{}, quietTestLogger()), enqueued[0])

	sourceID, threadKey := telegramActivityKeys(t, e)
	wantSource := fmt.Sprintf("%s:%d:%d", conn.ChannelID, senderID, messageID)
	wantThread := fmt.Sprintf("telegram:%s:%d", conn.ChannelID, senderID)
	if sourceID != wantSource {
		t.Errorf("activity.source_id = %q, want %q — the message was re-keyed onto whichever bot the row named at job time",
			sourceID, wantSource)
	}
	if threadKey != wantThread {
		t.Errorf("activity.thread_key = %q, want %q — the message was filed into the swapped-in bot's conversation",
			threadKey, wantThread)
	}
}

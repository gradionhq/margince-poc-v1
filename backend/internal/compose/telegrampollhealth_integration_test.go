// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What a poll does with a refusal, against a real database (design v2 §6). One
// concept: which refusals PARK a connection, and the fact that parking it is what
// actually stops the dispatcher scheduling it — there is no separate enable flag,
// so the status and the due-scan cannot disagree.
//
// The three refusals differ in remedy, so each is asserted on its own: a token
// Telegram now refuses (the admin re-pastes it), a conflict a webhook clear
// repairs (nothing to do), and a conflict that survives the clear (the admin finds
// the other consumer). Answering any of them as another either wedges a healthy
// bot or hides a broken one.
//
// The durability half — what a poll COMMITS — is in telegrampoll_integration_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A token Telegram has stopped accepting parks the connection, and PARKING IT IS
// WHAT STOPS THE RETRY: the dispatcher's due-scan selects only live `connected`
// rows, so there is no separate enable flag that could disagree with the status.
//
// The reason is recorded because the row has no column for it. A bare
// `reauth_required` tells an operator that something is wrong and nothing about
// which of the two things it is — a refused token they re-paste, or a rival
// consumer they go and find.
func TestAPollWhoseTokenIsRefusedParksTheConnection(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot:      telegram.Bot{ID: 92000007, Username: "revoked_bot"},
		failWith: fmt.Errorf("telegram: getUpdates: Unauthorized: %w", telegram.ErrTokenRejected),
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000007, "revoked_bot")

	// The connection is due before the refusal, or "no longer due" afterwards
	// would measure nothing.
	if !telegramConnectionIsDue(t, e, conn) {
		t.Fatal("a freshly connected bot is not in the due-scan, so this test could not observe it leaving")
	}

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the refused poll returned %v, want nil — River retrying a token Telegram will never accept is a loop nothing breaks", err)
	}

	if status := telegramConnectionStatus(t, e, conn); status != "reauth_required" {
		t.Fatalf("status = %q after Telegram refused the token, want reauth_required", status)
	}
	if telegramConnectionIsDue(t, e, conn) {
		t.Fatal("the parked connection is still in the due-scan — the dispatcher would poll a token Telegram has already refused, forever")
	}
	if n := e.WsCount(t, `
		SELECT count(*) FROM audit_log
		 WHERE entity_type = 'channel_connection' AND entity_id = $1
		   AND after->>'poll_stopped_because' <> ''`, conn.ID); n != 1 {
		t.Errorf("%d audit rows record WHY polling stopped, want 1 — the row has no column for it, so this is the only place an operator can read it", n)
	}
}

// A 409 that survives having the webhook cleared is a DIFFERENT fact from one a
// clear repairs: something else — another installation polling the same token —
// holds this bot's updates, and no retry fixes that. The connection is parked so
// the dispatcher stops handing River a job that can only ever fail.
//
// The poller establishes which of the two it is rather than inferring it from a
// retry counter: it clears the registration, then re-asks with no long-poll
// interval and reads only the refusal.
func TestAConflictThatSurvivesTheWebhookClearParksTheConnection(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	// Refused unconditionally, so clearing the webhook changes nothing — a rival
	// consumer, not a stale registration.
	api := &telegramPollFakeAPI{
		bot:      telegram.Bot{ID: 92000008, Username: "contested_bot"},
		failWith: fmt.Errorf("telegram: getUpdates: Conflict: %w", telegram.ErrWebhookActive),
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000008, "contested_bot")
	clearsAfterConnect := api.deleteWebhooks

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the contested poll returned %v, want nil — River retrying a bot another consumer holds is a loop nothing breaks", err)
	}

	if api.deleteWebhooks != clearsAfterConnect+1 {
		t.Errorf("the conflict cleared %d webhook(s), want 1 — the clear is what establishes that a registration was not the cause",
			api.deleteWebhooks-clearsAfterConnect)
	}
	if status := telegramConnectionStatus(t, e, conn); status != "error" {
		t.Fatalf("status = %q after the conflict survived the clear, want error", status)
	}
	if telegramConnectionIsDue(t, e, conn) {
		t.Error("the parked connection is still in the due-scan — the dispatcher would go on scheduling a poll that cannot succeed")
	}
}

// A 409 a clear DOES repair must not park anything. Parking on it would retire a
// healthy bot whose only problem was a registration left behind by a previous
// installation — which is the ordinary case when a bot moves from a webhook
// deployment to this one.
func TestAConflictAWebhookClearRepairsLeavesTheConnectionLive(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot:  telegram.Bot{ID: 92000013, Username: "stale_registration_bot"},
		held: []json.RawMessage{telegramPrivateUpdate(7101, 781101, 111, "after the clear")},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000013, "stale_registration_bot")
	// A registration reappears after connect cleared one — a second installation
	// re-registering, or an operator doing it by hand.
	api.webhookRegistered = true

	err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn)
	if err == nil {
		t.Fatal("the conflict was swallowed — a poll that collected nothing must not report success")
	}
	if !errors.Is(err, telegram.ErrWebhookActive) {
		t.Errorf("got %v, want the conflict to survive into the returned error so the retry is diagnosable", err)
	}

	if status := telegramConnectionStatus(t, e, conn); status != "connected" {
		t.Fatalf("status = %q, want connected — a registration this installation can clear is not a reason to retire the bot", status)
	}
	if !telegramConnectionIsDue(t, e, conn) {
		t.Fatal("the connection left the due-scan, so nothing would ever poll it again")
	}
	// And the retry actually works now, which is the proof the clear repaired it.
	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll after the clear: %v", err)
	}
	if n := rawCaptureCount(t, e, conn, 7101); n != 1 {
		t.Errorf("%d raw rows after the retry, want 1 — the clear did not actually make the bot pollable", n)
	}
}

// telegramConnectionStatus reads the connection's status through a
// workspace-bound transaction (channel_connection carries FORCE RLS).
func telegramConnectionStatus(t *testing.T, e *integration.Env, conn capture.ChannelConnection) string {
	t.Helper()
	var status string
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM channel_connection WHERE id = $1`, conn.ID).Scan(&status)
	}); err != nil {
		t.Fatalf("reading the connection status: %v", err)
	}
	return status
}

// telegramConnectionBot reads the bot the connection row currently names.
func telegramConnectionBot(t *testing.T, e *integration.Env, conn capture.ChannelConnection) string {
	t.Helper()
	var bot string
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT channel_id FROM channel_connection WHERE id = $1`, conn.ID).Scan(&bot)
	}); err != nil {
		t.Fatalf("reading the connection's bot: %v", err)
	}
	return bot
}

// telegramConnectionIsDue asks the PRODUCTION due-scan whether the dispatcher
// would still schedule a poll for this connection — never a re-spelling of its
// predicate, which could agree with the test while disagreeing with the scan.
func telegramConnectionIsDue(t *testing.T, e *integration.Env, conn capture.ChannelConnection) bool {
	t.Helper()
	due, err := capture.DueChannelConnections(context.Background(), e.Pool, capture.ProviderTelegram)
	if err != nil {
		t.Fatalf("the due-scan: %v", err)
	}
	for _, d := range due {
		if d.ID == conn.ID {
			return true
		}
	}
	return false
}

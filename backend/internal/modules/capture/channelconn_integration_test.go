// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The channel-connect suite (telegram-oa design v2 §4). Every test here is about
// ORDER or about what a failure leaves behind, which is why it runs on a real
// database: the invariants are "nothing was written", "the cursor restarted",
// "the second workspace loses the global index" — none of which a mocked store
// could be wrong about.
//
// The shape connect has under a PULL ingress is what most of these assert: there
// is no registration, so there is no window between a written row and a live one,
// and every refusal therefore has to leave the table and the vault untouched.

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// unknownToken is never registered with the fake, so Telegram rejects it. Its
// shape is valid on purpose: the point is to exercise the provider's refusal,
// not the local shape check that would short-circuit it.
const unknownToken = "8109999999:AAH-a-token-telegram-does-not-know"

// A refused token must leave the system exactly as it found it: no row for an
// operator to wonder about, and — the half that is easy to miss — nothing sealed
// in the vault. getMe runs first precisely so that both stay true.
//
// Counting what was NOT written is what earns the tag: the assertion is a real
// query returning zero, which no fake can be wrong about.
func TestConnectValidatesTokenBeforePersistingAnything(t *testing.T) {
	f := newChannelFixture(t, nil)

	_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: unknownToken,
	})
	if !errors.Is(err, telegram.ErrTokenRejected) {
		t.Fatalf("Connect with an unknown token: got %v, want ErrTokenRejected", err)
	}

	if n := f.rowCount(t); n != 0 {
		t.Errorf("a refused connect wrote %d channel_connection row(s); a token Telegram rejects must persist nothing", n)
	}
	if puts := f.vault.putCount(); puts != 0 {
		t.Errorf("a refused connect sealed %d secret(s); nothing references them and no sweep collects them", puts)
	}
	if cleared := f.api.clearedWebhooks(); len(cleared) != 0 {
		t.Errorf("deleteWebhook ran %d time(s) on a token getMe already rejected — the order is getMe first", len(cleared))
	}
}

// The transport turns a rejected token into a 400 naming the token, and carries
// none of the provider's own text: the client learns what to fix, not what
// Telegram said.
//
// Tagged because it shares newChannelFixture with the sibling arms that count
// real rows and vault puts. Rebuilding it on a nil pool to win the unit lane
// would turn a future "it persisted something" regression into a panic instead
// of a failure, which is the fixture lying rather than the test passing.
func TestConnectRejectsAnInvalidTokenWith400(t *testing.T) {
	f := newChannelFixture(t, nil)

	rec, req := f.connectRequest(
		`{"provider":"telegram","botToken":"` + unknownToken + `"}`)
	f.handlers.ConnectChannel(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decoding the problem body: %v (%s)", err, rec.Body.String())
	}
	if problem.Code != "channel_token_rejected" {
		t.Errorf("code %q, want channel_token_rejected", problem.Code)
	}
	if problem.Detail == "" {
		t.Error("the 400 carries no detail — the operator is not told what to fix")
	}
	// The token itself must not come back: a problem body lands in logs and
	// error trackers, and the token is a live credential.
	if strings.Contains(rec.Body.String(), unknownToken) {
		t.Errorf("the response echoed the bot token: %s", rec.Body.String())
	}
}

// Clearing the bot's webhook is what makes it pollable at all — Telegram refuses
// getUpdates while one is registered — so a connect that cannot clear it must
// write nothing rather than a row the poller could only ever fail on. The clear
// happens BEFORE the seal, which is what keeps the vault clean too.
func TestConnectWritesNothingWhenTheWebhookCannotBeCleared(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("unclearable_bot")
	api.deleteWebhookErr = telegram.ErrUnreachable
	f := newChannelFixture(t, api)

	_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if !errors.Is(err, telegram.ErrUnreachable) {
		t.Fatalf("Connect with deleteWebhook failing: got %v, want ErrUnreachable", err)
	}
	if n := f.rowCount(t); n != 0 {
		t.Errorf("the refused connect wrote %d row(s); a connection Telegram will not let us poll is not a connection", n)
	}
	if puts := f.vault.putCount(); puts != 0 {
		t.Errorf("the refused connect sealed %d secret(s) — the clear runs before the seal for exactly this reason", puts)
	}
}

// A connect that succeeds is live immediately and starts at cursor 0 — "whatever
// Telegram still holds" — so the messages waiting for a freshly connected bot are
// collected rather than skipped. There is no `pending` state to pass through,
// because nothing follows the insert.
func TestConnectIsLiveOnCommitWithAZeroCursor(t *testing.T) {
	api := newFakeTelegram()
	token, botID := api.withNewBot("live_bot")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.Status != "connected" {
		t.Errorf("status %q straight out of Connect, want connected", conn.Status)
	}
	status, archived, channelID := f.rowState(t, conn.ID)
	if status != "connected" || archived || channelID != strconv.FormatInt(botID, 10) {
		t.Errorf("row state status=%q archived=%v channel_id=%q, want connected and live on bot %d",
			status, archived, channelID, botID)
	}
	if offset := f.pollOffsetOf(t, conn.ID); offset != 0 {
		t.Errorf("poll_offset %d, want 0 — a fresh binding must collect what Telegram is holding, not skip it", offset)
	}
	if cleared := f.api.clearedWebhooks(); len(cleared) != 1 || cleared[0] != token {
		t.Errorf("deleteWebhook calls %v, want exactly one for this bot's token — otherwise getUpdates answers 409", cleared)
	}
	// One secret now, not two: a poll authenticates with the bot token alone.
	if puts := f.vault.putCount(); puts != 1 {
		t.Errorf("a connect sealed %d secret(s), want 1 (the bot token)", puts)
	}
	// Audit-only, and exactly one row: there is no second transition to record.
	if actions := f.auditActions(t, conn.ID); !slices.Equal(actions, []string{"create"}) {
		t.Errorf("audit actions %v, want a single create — a connect is one atomic step", actions)
	}
}

// The bot-scoped unique index is deliberately GLOBAL, not per-workspace: only one
// getUpdates consumer may hold a bot, so a second workspace connecting the same
// bot would not split the traffic, it would race the first for it.
func TestConnectRefusesTheSameBotInASecondWorkspace(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("shared_bot")
	first := newChannelFixture(t, api)
	second := newChannelFixture(t, api)

	if _, err := first.store.Connect(first.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	}); err != nil {
		t.Fatalf("the first workspace's connect must succeed: %v", err)
	}

	_, err := second.store.Connect(second.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("the second workspace's connect: got %v, want ErrConflict", err)
	}
	// And it must NOT read as the workspace rule: the remedy differs — pick
	// another bot, rather than disconnect the one this workspace already has.
	if errors.Is(err, capture.ErrChannelWorkspaceBotAlreadyBound) {
		t.Errorf("got %v, which tells the admin to disconnect a binding this workspace does not have", err)
	}
	if n := second.rowCount(t); n != 0 {
		t.Errorf("the losing workspace kept %d row(s)", n)
	}
	// The loser sealed a token on its way to the insert; it must be destroyed,
	// because a lost race is the one failure that proves no row persisted to name it.
	sealed := second.vault.mintedRefs()
	if len(sealed) != 1 {
		t.Fatalf("the losing connect sealed %d secret(s), want just the bot token", len(sealed))
	}
	if _, err := second.vault.Get(second.ctx, second.workspaceKey(), sealed[0]); err == nil {
		t.Error("the losing connect left a sealed secret behind, referenced by no row and collected by no sweep")
	}
}

// One live bot per WORKSPACE (F22). Two live bindings make every outbound reply
// ambiguous and the send resolver refuses rather than guessing, so a second
// binding would not add a channel — it would take away the ability to reply on
// either. The refusal has to say WHICH rule it lost to: the remedy is to
// disconnect what is already bound, not to try another bot.
func TestConnectRefusesASecondBotInTheSameWorkspace(t *testing.T) {
	api := newFakeTelegram()
	firstToken, _ := api.withNewBot("first_bot")
	secondToken, _ := api.withNewBot("second_bot")
	f := newChannelFixture(t, api)

	if _, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: firstToken,
	}); err != nil {
		t.Fatalf("the first connect must succeed: %v", err)
	}

	_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: secondToken,
	})
	if !errors.Is(err, capture.ErrChannelWorkspaceBotAlreadyBound) {
		t.Fatalf("connecting a second bot: got %v, want ErrChannelWorkspaceBotAlreadyBound", err)
	}
	if n := f.rowCount(t); n != 1 {
		t.Errorf("%d rows after the refused second connect, want just the first binding", n)
	}

	rec, req := f.connectRequest(`{"provider":"telegram","botToken":"` + secondToken + `"}`)
	f.handlers.ConnectChannel(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "channel_workspace_already_bound") {
		t.Errorf("the 409 does not carry the actionable code: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secondToken) {
		t.Errorf("the response echoed the bot token: %s", rec.Body.String())
	}
}

// A replacement clears the INCOMING bot's webhook (Telegram would otherwise refuse
// to poll it) and leaves the connection live throughout: there is no registration
// to be mid-flight, so no state in which the row could claim less than the truth.
//
// It also RESTARTS the cursor, and that is the load-bearing half: update_id is a
// per-bot sequence, so inheriting the outgoing bot's high-water mark would ask the
// incoming bot for updates numbered far beyond anything it has ever sent, and every
// message it received would be skipped silently.
func TestReplaceTokenClearsTheIncomingWebhookAndRestartsTheCursor(t *testing.T) {
	api := newFakeTelegram()
	firstToken, firstID := api.withNewBot("original_bot")
	replacementToken, replacementID := api.withNewBot("replacement_bot")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: firstToken,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	oldCredential := f.credentialRefOf(t, conn.ID)
	// The outgoing bot has been polled for a while.
	f.setPollOffset(t, conn.ID, 900500)

	if err := f.store.ReplaceToken(f.ctx, conn.ID, replacementToken); err != nil {
		t.Fatalf("ReplaceToken: %v", err)
	}

	status, archived, channelID := f.rowState(t, conn.ID)
	if status != "connected" {
		t.Errorf("status %q after a successful rotation, want connected — a replacement never passes through a not-live state", status)
	}
	if archived {
		t.Error("the rotation archived the connection — history and identity bindings hang off this row surviving")
	}
	if channelID != strconv.FormatInt(replacementID, 10) {
		t.Errorf("channel_id %q, want the replacement bot's id %d", channelID, replacementID)
	}
	if offset := f.pollOffsetOf(t, conn.ID); offset != 0 {
		t.Errorf("poll_offset %d after a bot swap, want 0 — update_id is per-bot, so the incoming bot's own messages would be skipped", offset)
	}
	if cleared := f.api.clearedWebhooks(); !slices.Contains(cleared, replacementToken) {
		t.Errorf("deleteWebhook calls %v never named the incoming token — Telegram would refuse to poll it", cleared)
	}
	// create, then one update: the swap is a single transition, not a round trip
	// through a half-live state.
	if actions := f.auditActions(t, conn.ID); !slices.Equal(actions, []string{"create", "update"}) {
		t.Errorf("audit actions %v, want create then one update", actions)
	}
	// The superseded token is unreachable from any row and must be gone.
	if _, err := f.vault.Get(f.ctx, f.workspaceKey(), oldCredential); err == nil {
		t.Error("the superseded bot token survived the rotation, referenced by no row and collected by no sweep")
	}
	if firstID == replacementID {
		t.Fatal("the fixture handed out one bot twice, so this test could not tell a swap from a rotation")
	}
}

// The race the version predicate exists for, on the replacement arm. Replacement A
// reads the row and then blocks at the provider; replacement B repoints the SAME
// row onto its own bot and commits. A must lose rather than overwrite it — and it
// must lose BEFORE it destroys anything, or the winner is left live with a
// credential nothing can unseal.
//
// The cursor is moved off zero first. A replacement resets it, and the trigger that
// maintains `version` exempts a cursor-only write — so run against a never-polled
// connection this case cannot tell a correct exemption from one that also swallows
// the bump on the transition the predicate reads. The steady state is a bot that
// HAS polled.
func TestAStaleReplacementCannotOverwriteTheWinner(t *testing.T) {
	api := newFakeTelegram()
	originalToken, _ := api.withNewBot("original_bot")
	tokenA, botAID := api.withNewBot("bot_a")
	tokenB, botBID := api.withNewBot("bot_b")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: originalToken,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	f.setPollOffset(t, conn.ID, 4242)

	var errB error
	// Fires while replacement A is inside deleteWebhook — after A read the row,
	// before A repoints it, which is the only window in which A can go stale.
	api.onNextDeleteWebhook(func(string) {
		errB = f.store.ReplaceToken(f.ctx, conn.ID, tokenB)
	})

	errA := f.store.ReplaceToken(f.ctx, conn.ID, tokenA)

	if errB != nil {
		t.Fatalf("the replacement that ran inside the window: %v, want it to complete and own the connection", errB)
	}
	if !errors.Is(errA, apperrors.ErrVersionSkew) {
		t.Fatalf("the stale replacement = %v, want ErrVersionSkew — its snapshot no longer describes the row", errA)
	}

	status, _, channelID := f.rowState(t, conn.ID)
	if channelID != strconv.FormatInt(botBID, 10) {
		t.Fatalf("channel_id %q, want bot B's id %d — the newer replacement owns this connection", channelID, botBID)
	}
	if status != "connected" {
		t.Errorf("status %q, want connected — the winner's binding is live", status)
	}
	if channelID == strconv.FormatInt(botAID, 10) {
		t.Error("the stale replacement overwrote the newer one's bot")
	}
	// The winner's credential must still resolve: the loser has to refuse before
	// its own teardown touches state it no longer owns.
	if _, err := f.vault.Get(f.ctx, f.workspaceKey(), f.credentialRefOf(t, conn.ID)); err != nil {
		t.Errorf("the live connection's bot token is gone: %v — the stale replacement destroyed the winner's credential", err)
	}
}

// A disconnect landing inside a replacement's provider window wins, and the
// replacement has to lose CLEANLY. This is the teardown arm of the same race the
// version predicate guards: the replacement decided to repoint from a read taken
// before its round trip at Telegram, and by the time it writes, the operator has
// withdrawn the binding altogether.
//
// The loser's obligation is the part that is easy to get wrong. Its repoint wrote
// nothing — the row it locked is archived — so the token it sealed on the way is
// referenced by no row and collected by no sweep. Returning the refusal without
// destroying it leaves a live bot credential in the vault forever, with nothing
// left that could ever name it.
func TestADisconnectInsideAReplacementsWindowLeavesNoOrphanedCredential(t *testing.T) {
	api := newFakeTelegram()
	originalToken, _ := api.withNewBot("outgoing_bot")
	replacementToken, _ := api.withNewBot("late_bot")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: originalToken,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// A bot that has polled — the steady state, and the one in which the version
	// trigger's cursor exemption could otherwise hide the bump this race needs.
	f.setPollOffset(t, conn.ID, 4242)
	sealedBeforeReplacement := len(f.vault.mintedRefs())

	var errDisconnect error
	// Fires while the replacement's own webhook clear is out at the provider —
	// after its read of the row, before its repoint.
	api.onNextDeleteWebhook(func(string) {
		errDisconnect = f.store.Disconnect(f.ctx, conn.ID)
	})

	errReplace := f.store.ReplaceToken(f.ctx, conn.ID, replacementToken)

	if errDisconnect != nil {
		t.Fatalf("the disconnect that ran inside the window: %v, want it to complete — the operator withdrew the binding they were shown", errDisconnect)
	}
	if !errors.Is(errReplace, apperrors.ErrNotFound) {
		t.Fatalf("the losing replacement = %v, want ErrNotFound — the connection it was repointing no longer exists", errReplace)
	}

	status, archived, _ := f.rowState(t, conn.ID)
	if status != "disconnected" || !archived {
		t.Errorf("row state status=%q archived=%v, want disconnected and archived — the replacement resurrected a withdrawn binding", status, archived)
	}
	// Every ref sealed after the connect belongs to the losing replacement, and
	// none of them may survive it.
	for _, ref := range f.vault.mintedRefs()[sealedBeforeReplacement:] {
		if _, err := f.vault.Get(f.ctx, f.workspaceKey(), ref); err == nil {
			t.Error("the losing replacement left a sealed bot token behind, referenced by no row and collected by no sweep")
		}
	}
}

// Disconnecting stops capture; it does not erase. The sealed token is destroyed
// and the row archived as disconnected — which is what actually ends ingress,
// because the poll dispatcher's due-scan selects only live connected rows — and
// every activity already captured through the channel is still there.
func TestDisconnectArchivesTheBindingAndKeepsActivities(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("disconnect_bot")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	activity := f.seedActivity(t)
	credentialRef := f.credentialRefOf(t, conn.ID)

	if err := f.store.Disconnect(f.ctx, conn.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	status, archived, _ := f.rowState(t, conn.ID)
	if status != "disconnected" || !archived {
		t.Errorf("row state status=%q archived=%v, want disconnected and archived (archival is what both stops the due-scan and frees the unique indexes for a reconnect)", status, archived)
	}
	if _, err := f.vault.Get(f.ctx, f.workspaceKey(), credentialRef); err == nil {
		t.Error("the bot token survived disconnect — a live credential outlives the operator's withdrawal")
	}
	if !f.activityExists(t, activity) {
		t.Error("disconnect removed a captured activity; disconnecting stops capture, it does not erase history")
	}
	// The read surface no longer offers it, and the same bot can be connected
	// again — the property archival buys.
	if conns := f.liveConnections(t); len(conns) != 0 {
		t.Errorf("the disconnected connection is still listed: %+v", conns)
	}
	if _, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	}); err != nil {
		t.Errorf("reconnecting the same bot after a disconnect: %v", err)
	}
}

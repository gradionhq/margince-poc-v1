// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The poller against a real transaction. Every claim here is about what a
// transaction actually committed or rolled back, which is the one class of claim a
// mock cannot be wrong about in the right direction:
//
//   - the cursor moves only WITH the batch, so a crash before commit re-delivers
//     it and the per-bot key folds it to one row per update;
//   - an erased subject's update persists nothing, and the cursor still advances;
//   - a group chat leaves ZERO rows — refusing it downstream would already have
//     written the orphan;
//   - a poison update is dropped and the batch around it survives;
//   - an advancing cursor does not read as a changed binding, a REPLACEMENT still
//     does, and a bot replaced mid-poll does not inherit the outgoing bot's cursor.
//
// What a poll does with a refusal it cannot store its way out of is the other half,
// and it lives next door: telegrampollhealth_integration_test.go.
//
// The single-flight guarantee is asserted in telegrampoll_test.go, on the args
// type that carries it: uniqueness is declared, not observed, and a test that
// raced two jobs would be asserting River's scheduler rather than this code's.

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
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A poll persists what it collected and advances the cursor past it, in one
// transaction. The cursor is the load-bearing assertion: getUpdates(offset =
// highest + 1) is what tells Telegram to forget the batch, so the number stored
// here is the acknowledgement of everything below it.
func TestAPollPersistsTheBatchAndAdvancesTheCursor(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot: telegram.Bot{ID: 92000001, Username: "batch_bot"},
		held: []json.RawMessage{
			telegramPrivateUpdate(6101, 780101, 11, "first"),
			telegramPrivateUpdate(6102, 780102, 12, "second"),
		},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000001, "batch_bot")

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll: %v", err)
	}

	for _, updateID := range []int64{6101, 6102} {
		if n := rawCaptureCount(t, e, conn, updateID); n != 1 {
			t.Errorf("%d raw rows for update %d, want 1", n, updateID)
		}
	}
	if jobs := telegramEnqueuedRawIDs(t, e, conn.ID.String()); len(jobs) != 2 {
		t.Errorf("%d ingest jobs for a two-update batch, want 2", len(jobs))
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 6103 {
		t.Fatalf("poll_offset = %d, want 6103 (the batch's highest id + 1) — the next poll is what acknowledges this batch", offset)
	}
	// The first poll asked from 0 — "whatever Telegram still holds" — which is what
	// lets a freshly connected bot collect the messages waiting for it.
	if polls := api.polls(); len(polls) != 1 || polls[0] != 0 {
		t.Errorf("the poll asked from offsets %v, want a single ask from 0", polls)
	}
}

// The whole correctness story of a pull ingress, held against a real rollback: a
// crash between Telegram answering and the transaction committing must leave the
// cursor exactly where it was, because the batch was never acknowledged. Telegram
// then re-delivers the identical updates, and the per-bot (bot, update_id) natural
// key is what folds them to one row each rather than two.
//
// The failure is injected at the enqueue — INSIDE the transaction the poller
// opened — because that is the only way to prove the raw insert does not survive
// the rollback. An error returned before the transaction would prove nothing.
func TestTheCursorSurvivesACrashBeforeCommit(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	// Telegram goes on holding what was never acknowledged, so the re-poll below
	// gets the identical batch without the fixture scripting a second one.
	api := &telegramPollFakeAPI{
		bot: telegram.Bot{ID: 92000002, Username: "crash_bot"},
		held: []json.RawMessage{
			telegramPrivateUpdate(6201, 780201, 21, "before the crash"),
			telegramPrivateUpdate(6202, 780202, 22, "also before the crash"),
		},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000002, "crash_bot")

	crash := errors.New("injected: the enqueue fails inside the poller's own transaction")
	err := runOnePoll(t, newTestPollWorker(e, vault, api, &fakeInserter{err: crash}), conn)
	if !errors.Is(err, crash) {
		t.Fatalf("the crashing poll returned %v, want the injected failure — a swallowed one would look like a successful poll", err)
	}

	if offset := telegramStoredPollOffset(t, e, conn); offset != 0 {
		t.Fatalf("poll_offset = %d after a rolled-back poll, want 0 — an acknowledged batch Telegram has forgotten is unrecoverable", offset)
	}
	for _, updateID := range []int64{6201, 6202} {
		if n := rawCaptureCount(t, e, conn, updateID); n != 0 {
			t.Fatalf("%d raw rows for update %d survived the rollback, want 0", n, updateID)
		}
	}

	// The re-poll: the identical batch arrives again and folds to one row each.
	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the re-poll: %v", err)
	}
	for _, updateID := range []int64{6201, 6202} {
		if n := rawCaptureCount(t, e, conn, updateID); n != 1 {
			t.Errorf("%d raw rows for re-delivered update %d, want exactly 1", n, updateID)
		}
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 6203 {
		t.Errorf("poll_offset = %d after the re-poll, want 6203", offset)
	}
	if polls := api.polls(); len(polls) != 2 || polls[0] != 0 || polls[1] != 0 {
		t.Errorf("the two polls asked from offsets %v, want both from 0 — a cursor that moved on the failed poll would have skipped the batch", polls)
	}
}

// An erased subject writing again must leave NOTHING behind. The suppression list
// stops the Person being recreated, but a verbatim update persisted anyway holds
// their numeric id, handle, names and message text where no later erasure can
// reach it: both the raw purge and the suppression itself are driven off
// person_channel_identity rows the first erasure deleted and the suppression
// guarantees are never recreated.
//
// The cursor still advances, and that half matters as much: a cursor held back by
// an erased subject's messages would re-fetch them on every poll forever and block
// every later customer's message behind them.
func TestAPollPersistsNothingForAnErasedSubject(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	const erasedAccount = int64(780301)
	api := &telegramPollFakeAPI{
		bot: telegram.Bot{ID: 92000003, Username: "erased_bot"},
		held: []json.RawMessage{
			telegramPrivateUpdate(6301, erasedAccount, 31, "hello again"),
			telegramPrivateUpdate(6302, 780302, 32, "an unrelated customer"),
		},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000003, "erased_bot")
	suppressChannelAccount(t, e, "780301")

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll: %v", err)
	}

	if n := rawCaptureCount(t, e, conn, 6301); n != 0 {
		t.Errorf("%d raw rows for an erased subject's message, want 0 — their id, handle, name and words were stored where no erasure can reach them", n)
	}
	// The unrelated customer in the same batch must be unaffected: one refusal is
	// not a reason to drop everybody's messages.
	if n := rawCaptureCount(t, e, conn, 6302); n != 1 {
		t.Errorf("%d raw rows for the unrelated customer, want 1 — a refusal must not take the batch around it down", n)
	}
	if jobs := telegramEnqueuedRawIDs(t, e, conn.ID.String()); len(jobs) != 1 {
		t.Errorf("%d ingest jobs, want 1 — an update that must not be stored must not be queued for capture either", len(jobs))
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 6303 {
		t.Errorf("poll_offset = %d, want 6303 — a cursor wedged behind an erased subject blocks every later customer", offset)
	}
}

// A group chat leaves ZERO rows. Refusing it in the worker that normalizes the
// payload later is strictly WORSE than not filtering at all: by then the verbatim
// update is stored, holding the sender's id, handle, names and text, and no Person,
// erasure, SAR or retention lane can reach it — every one of them drives off
// person_channel_identity, which only a captured record creates.
//
// The cursor still advances past it, because the alternative is re-fetching an
// update this connector will never store on every poll, forever.
func TestAPollPersistsNothingForAGroupChat(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot:  telegram.Bot{ID: 92000004, Username: "group_bot"},
		held: []json.RawMessage{telegramGroupUpdate(6401, 780401)},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000004, "group_bot")

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll: %v", err)
	}

	// Counted over the whole table, not through the key: the claim is that NO row
	// was written, however the key might be spelled.
	if n := e.WsCount(t, `SELECT count(*) FROM raw_capture WHERE source_system = 'telegram'`); n != 0 {
		t.Errorf("%d raw rows after a group-chat batch, want 0 — a stored group update is an orphan no lifecycle lane can reach", n)
	}
	if jobs := telegramEnqueuedRawIDs(t, e, conn.ID.String()); len(jobs) != 0 {
		t.Errorf("%d ingest jobs for a group chat, want 0", len(jobs))
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 6402 {
		t.Errorf("poll_offset = %d, want 6402 — an update this connector never stores must not be re-fetched forever", offset)
	}
}

// A poison update is dropped, the rest of the batch persists, and the cursor
// advances PAST the poison. That last part is the decision: a poison update that
// blocked the cursor would silently stop all further ingress for that bot, which is
// a far larger loss than the one update dropped here.
func TestAPollDropsAPoisonUpdateAndAdvancesPastIt(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	// The poison is LAST, so the cursor can only reach past it by counting it:
	// with the poison first, advancing to the good update's id would already have
	// cleared it and the test would pass without the decision being made.
	api := &telegramPollFakeAPI{
		bot: telegram.Bot{ID: 92000005, Username: "poison_bot"},
		held: []json.RawMessage{
			telegramPrivateUpdate(6501, 780501, 51, "a real message"),
			json.RawMessage(`{"update_id":6502,"message":{"message_id":52,"chat":"not-an-object"}}`),
		},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000005, "poison_bot")

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll: %v", err)
	}

	if n := rawCaptureCount(t, e, conn, 6501); n != 1 {
		t.Errorf("%d raw rows for the good update, want 1 — one poison update must not take the batch around it down", n)
	}
	if n := rawCaptureCount(t, e, conn, 6502); n != 0 {
		t.Errorf("%d raw rows for the poison update, want 0", n)
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 6503 {
		t.Fatalf("poll_offset = %d, want 6503 — a cursor stuck at the poison update wedges ALL ingress for this bot", offset)
	}
}

// A bot REPLACED while a poll is in flight must not inherit the outgoing bot's
// cursor. This is the window that matters most in practice: a 25s long poll under
// a 30s tick means a poll is in flight almost always, so an admin rotating a token
// lands inside one.
//
// update_id is a per-bot sequence. The finishing poll holds the outgoing bot's
// number, and Telegram reads an offset as confirmation of everything below it — so
// stamping that number onto the incoming bot tells Telegram to forget every message
// it is holding for a bot that has never sent one that high, and the connection
// goes permanently deaf while still reading `connected`. The raw rows the poll
// wrote are keyed on the bot that sent them and stay valid; only the cursor is
// wrong, so only the cursor is refused.
func TestAReplacementDuringAPollDoesNotInheritTheOutgoingBotsCursor(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot:  telegram.Bot{ID: 92000009, Username: "outgoing_poll_bot"},
		held: []json.RawMessage{telegramPrivateUpdate(6901, 780901, 91, "the outgoing bot's last message")},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000009, "outgoing_poll_bot")

	// The admin swaps the bot while the poll is still waiting on Telegram.
	const incomingBotID = int64(92000010)
	api.onGetUpdates = func() {
		replaceTestTelegramBot(t, e, vault, conn.ID, incomingBotID, "incoming_poll_bot")
	}

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll that finished after the swap: %v", err)
	}

	if bot := telegramConnectionBot(t, e, conn); bot != fmt.Sprintf("%d", incomingBotID) {
		t.Fatalf("the row names bot %s, want the swapped-in %d — the arrange step did not take effect", bot, incomingBotID)
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 0 {
		t.Fatalf("poll_offset = %d after the swap, want 0 — the incoming bot inherited the outgoing bot's number and every message Telegram holds for it would be dropped unread",
			offset)
	}
	// The outgoing bot's own message is still captured, under its own bot's key:
	// refusing the cursor must not refuse the batch that was already durable.
	if n := rawCaptureCount(t, e, capture.ChannelConnection{ChannelID: "92000009"}, 6901); n != 1 {
		t.Errorf("%d raw rows for the outgoing bot's message, want 1 — the update was real and the poll had already stored it", n)
	}
}

// An advancing cursor is not a changed BINDING. The send path resolves a
// credential, walks the seat/consent/pacing gates, then re-reads the row's version
// to refuse a token whose bot was replaced under it — so a version bumped by every
// inbound message would fire that fence on a healthy channel, over and over.
func TestAdvancingTheCursorDoesNotLookLikeAReplacedBinding(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot:  telegram.Bot{ID: 92000006, Username: "fence_bot"},
		held: []json.RawMessage{telegramPrivateUpdate(6601, 780601, 61, "just a message")},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000006, "fence_bot")
	before := telegramConnectionVersion(t, e, conn)

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the poll: %v", err)
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 6602 {
		t.Fatalf("the cursor did not move (poll_offset = %d), so this test could not observe what moving it costs", offset)
	}
	if after := telegramConnectionVersion(t, e, conn); after != before {
		t.Fatalf("the connection version moved from %d to %d because its cursor advanced — every send resolved before an inbound message would be refused as a replaced binding",
			before, after)
	}
}

// The cursor's exemption from the version bump must NOT extend to a replacement
// that happens to reset the cursor — which every replacement does. ReplaceToken
// writes `poll_offset = 0` alongside the bot it points at, so a trigger condition
// that read "skip when poll_offset changed" would stop bumping the version on the
// one transition the version exists to detect, on every connection that has ever
// polled (the steady state).
//
// What that would cost: the send path's binding fence never fires after a
// replacement, and the lifecycle writers' optimistic guards stop detecting skew —
// so a stale replacement overwrites the winner and leaks the winner's sealed token.
//
// The cursor is therefore advanced FIRST here. Replacing a never-polled connection
// would pass against the broken condition too.
func TestAReplacementStillBumpsTheVersionOnAPolledConnection(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()

	api := &telegramPollFakeAPI{
		bot:  telegram.Bot{ID: 92000011, Username: "polled_then_swapped"},
		held: []json.RawMessage{telegramPrivateUpdate(7001, 781001, 101, "a message first")},
	}
	conn := connectTestTelegramBot(t, e, vault, api, 92000011, "polled_then_swapped")

	if err := runOnePoll(t, newTestPollWorker(e, vault, api, ambientPollInserter(t, e)), conn); err != nil {
		t.Fatalf("the arranging poll: %v", err)
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset == 0 {
		t.Fatal("the cursor did not move, so this test would pass against a condition that skips the bump whenever the cursor changes")
	}
	beforeSwap := telegramConnectionVersion(t, e, conn)

	replaceTestTelegramBot(t, e, vault, conn.ID, 92000012, "the_replacement")

	if after := telegramConnectionVersion(t, e, conn); after == beforeSwap {
		t.Fatalf("the version stayed at %d across a bot replacement — the send fence and every optimistic guard on this row have stopped seeing the one change they exist for",
			after)
	}
	if offset := telegramStoredPollOffset(t, e, conn); offset != 0 {
		t.Errorf("poll_offset = %d after the swap, want 0 — update_id is per-bot", offset)
	}
}

// suppressChannelAccount arms the erasure suppression entry for one Telegram
// account, through the SAME hash the poller's probe and the eraser both derive —
// a hand-written value here could agree with this test and with nothing else.
func suppressChannelAccount(t *testing.T, e *integration.Env, account string) {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'channel_identity', $1)`,
			storekit.ChannelIdentityHash(telegram.Provider, account))
		return err
	}); err != nil {
		t.Fatalf("arming the erasure suppression for account %s: %v", account, err)
	}
}

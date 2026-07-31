// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The shared arrange half of every Telegram ingress suite: a fake Bot API that
// hands out scripted batches, a connection built by the REAL Connect, and the
// read-backs the claims are made against.
//
// Connect is run rather than a row hand-inserted, so a suite exercises exactly
// what production writes — a fixture row could drift from it silently, and the
// poller reads columns (credential_ref, poll_offset) Connect is the only writer of.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// telegramPollFakeAPI is the provider boundary the ingress suites drive: getMe
// answers one bot, deleteWebhook is recorded, and getUpdates implements the real
// OFFSET CONTRACT — it answers everything it still holds at or above the asked
// offset, and forgets everything below, because asking for offset N+1 IS the
// acknowledgement of N.
//
// Modelling the contract rather than handing out scripted batches is what keeps a
// re-delivery honest: a batch stays held until something acknowledges it, so a
// poll that rolled back gets the identical batch again, and a read that
// acknowledges nothing (the re-ask after a webhook clear) cannot consume one.
//
// The mutex is not decoration: the deleteWebhook counter is read by the test body
// between the polls that write it, and onGetUpdates lets a lifecycle change commit
// from another call frame while a poll is still in flight.
type telegramPollFakeAPI struct {
	mu  sync.Mutex
	bot telegram.Bot
	// held is every update Telegram has not yet been told to forget.
	held           []json.RawMessage
	getUpdatesCall []int64 // the offset each poll asked from, in order
	deleteWebhooks int
	// failWith, when non-nil, is what getUpdates answers instead of a batch —
	// unconditionally, so it models a refusal nothing this installation does can
	// repair.
	failWith error
	// webhookRegistered models the ONE 409 cause a clear does repair: while it is
	// set, getUpdates is refused; deleteWebhook clears it. Modelling the provider's
	// state rather than counting calls is what lets one fixture serve both the
	// repairable conflict and the one that survives a clear.
	webhookRegistered bool
	// onGetUpdates runs inside getUpdates, after the batch is chosen and outside
	// the mutex, so a test can make something else commit WHILE a poll is in
	// flight — the 25s window a lifecycle change actually lands in.
	onGetUpdates func()
}

func (f *telegramPollFakeAPI) GetMe(context.Context, string) (telegram.Bot, error) {
	return f.bot, nil
}

func (f *telegramPollFakeAPI) DeleteWebhook(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteWebhooks++
	f.webhookRegistered = false
	return nil
}

func (f *telegramPollFakeAPI) GetUpdates(_ context.Context, _ string, offset int64, _ int, _ []string) ([]json.RawMessage, int64, error) {
	f.mu.Lock()
	f.getUpdatesCall = append(f.getUpdatesCall, offset)
	failWith := f.failWith
	if failWith == nil && f.webhookRegistered {
		failWith = fmt.Errorf("telegram: getUpdates: Conflict: can't use getUpdates method while webhook is active: %w",
			telegram.ErrWebhookActive)
	}
	hook := f.onGetUpdates
	if failWith == nil {
		f.held = slices.DeleteFunc(f.held, func(raw json.RawMessage) bool {
			id, numbered := telegram.UpdateIDOf(raw)
			return numbered && id < offset
		})
	}
	batch := append([]json.RawMessage(nil), f.held...)
	f.mu.Unlock()

	// Outside the mutex: what the hook drives calls back into this same fake.
	if hook != nil {
		hook()
	}
	if failWith != nil {
		return nil, 0, failWith
	}
	return batch, telegramHighestUpdateID(batch), nil
}

func (f *telegramPollFakeAPI) SendMessage(context.Context, string, telegram.OutboundChannelMessage) (int64, error) {
	panic("telegramPollFakeAPI: the ingress suites never send")
}

// polls reports how many times getUpdates was asked, and from which offsets.
func (f *telegramPollFakeAPI) polls() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.getUpdatesCall...)
}

// telegramHighestUpdateID mirrors what the real client reports: the highest
// number in the batch, 0 for an empty one. It is derived from the fixture's own
// bytes rather than declared alongside them, so a batch cannot be scripted with an
// acknowledgement number its updates do not carry.
func telegramHighestUpdateID(batch []json.RawMessage) int64 {
	var highest int64
	for _, raw := range batch {
		if id, ok := telegram.UpdateIDOf(raw); ok {
			highest = max(highest, id)
		}
	}
	return highest
}

// telegramAdminContext binds the principal Connect needs: a human on a full seat
// holding the channel_connection admin grants. e.Rep1 already exists as an
// app_user in e.WS (integration.Setup seeds it), which the connected_by composite
// FK requires.
func telegramAdminContext(ws, user ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects: map[string]principal.ObjectGrant{
				"channel_connection": {Create: true, Read: true, Update: true, Delete: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// connectTestTelegramBot runs the real Connect flow (seals a real vault ref,
// writes the real row) so every suite exercises exactly what the poller reads.
func connectTestTelegramBot(t *testing.T, e *integration.Env, vault keyvault.Vault, api telegram.API, botID int64, username string) capture.ChannelConnection {
	t.Helper()
	store := capture.NewChannelStore(e.Pool, vault, api, quietTestLogger())
	conn, err := store.Connect(telegramAdminContext(e.WS, e.Rep1), capture.ConnectRequest{
		Provider: capture.ProviderTelegram,
		BotToken: fmt.Sprintf("%d:AAH-fixture-secret-for-%s", botID, username),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.Status != "connected" {
		t.Fatalf("Connect left the row %q, want connected — there is no half-connected state", conn.Status)
	}
	return conn
}

// newTestPollWorker builds the poll worker over the same constructor jobs.go
// composes, so nothing in these suites passes against a worker production does
// not run. inserter is the seam a test fails inside the real transaction.
func newTestPollWorker(e *integration.Env, vault keyvault.Vault, api telegram.API, inserter telegramEnqueuer) *telegramPollWorker {
	return newTelegramPollWorker(e.Pool, vault, api, inserter, quietTestLogger())
}

// ambientPollInserter is the real River insert client the poller enqueues ingest
// jobs through. A poll driven directly (rather than by a running River client) has
// no client on its context, so the ambient enqueuer production uses cannot resolve
// one — this stands in for it and still writes through the caller's transaction,
// which is what the atomicity claims depend on.
func ambientPollInserter(t *testing.T, e *integration.Env) telegramEnqueuer {
	t.Helper()
	inserter, err := jobs.NewInserter(e.Pool, quietTestLogger())
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	return inserter
}

// runOnePoll works one poll job through the real worker, with the args the
// dispatcher would have stamped.
//
// The JobRow is supplied rather than left zero: river.Job EMBEDS
// *rivertype.JobRow, so a job built without one panics the moment the worker
// reads any River-side field, and a fixture that only ever drove the happy path
// would not find out.
func runOnePoll(t *testing.T, worker *telegramPollWorker, conn capture.ChannelConnection) error {
	t.Helper()
	return worker.Work(context.Background(), &river.Job[TelegramPollArgs]{
		JobRow: &rivertype.JobRow{Kind: TelegramPollArgs{}.Kind(), Attempt: 1},
		Args:   TelegramPollArgs{Workspace: conn.WorkspaceID.String(), ConnectionID: conn.ID.String()},
	})
}

// telegramPrivateUpdate renders a full private-chat message update — chat, sender,
// the provider's own send time and text — so the ingest worker can normalize it
// into a real activity.
func telegramPrivateUpdate(updateID, senderID, messageID int64, text string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%d,"type":"private"},`+
			`"from":{"id":%d,"username":"sender%d","first_name":"Sender %d"},`+
			`"date":1785000000,"text":%q}}`,
		updateID, messageID, senderID, senderID, senderID, senderID, text))
}

// telegramGroupUpdate renders a message in a GROUP chat — out of scope by design
// §1, and the case that must leave no row at all.
func telegramGroupUpdate(updateID, senderID int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"update_id":%d,"message":{"message_id":9,"chat":{"id":-100%d,"type":"group"},`+
			`"from":{"id":%d,"username":"grouper"},"date":1785000000,"text":"group chatter"}}`,
		updateID, senderID, senderID))
}

// telegramStoredPollOffset reads the connection's cursor through a
// workspace-bound transaction — channel_connection carries FORCE RLS, so a query
// with no GUC set would answer zero rows regardless of what the table holds.
func telegramStoredPollOffset(t *testing.T, e *integration.Env, conn capture.ChannelConnection) int64 {
	t.Helper()
	var offset int64
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT poll_offset FROM channel_connection WHERE id = $1`, conn.ID).Scan(&offset)
	}); err != nil {
		t.Fatalf("reading the stored poll offset: %v", err)
	}
	return offset
}

// telegramConnectionVersion reads the row's version, which the send path's fence
// compares against to refuse a credential whose bot moved under it.
func telegramConnectionVersion(t *testing.T, e *integration.Env, conn capture.ChannelConnection) int64 {
	t.Helper()
	var version int64
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version FROM channel_connection WHERE id = $1`, conn.ID).Scan(&version)
	}); err != nil {
		t.Fatalf("reading the connection version: %v", err)
	}
	return version
}

// rawCaptureCount counts the raw rows one update left behind, keyed through
// telegramRawSourceID — the production spelling, so an assertion here can never
// pass against a key shape the poller has stopped writing.
func rawCaptureCount(t *testing.T, e *integration.Env, conn capture.ChannelConnection, updateID int64) int {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND source_id = $1`,
		telegramRawSourceID(conn.ChannelID, updateID))
}

// telegramEnqueuedRawIDs is the raw row each of one connection's ingest jobs was
// told to normalize. river_job is not workspace-scoped, so it is read off the pool
// directly.
func telegramEnqueuedRawIDs(t *testing.T, e *integration.Env, connectionID string) []string {
	t.Helper()
	rows, err := e.Pool.Query(context.Background(),
		`SELECT args->>'raw_capture_id' FROM river_job WHERE kind = $1 AND args->>'connection_id' = $2`,
		TelegramIngestArgs{}.Kind(), connectionID)
	if err != nil {
		t.Fatalf("reading the ingest jobs for connection %s: %v", connectionID, err)
	}
	rawIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collecting the ingest jobs for connection %s: %v", connectionID, err)
	}
	return rawIDs
}

// workOneIngestJob runs one enqueued job through the real worker, with the args
// the poll actually stamped — read back off river_job rather than assembled here,
// so the worker is fed exactly what that poll pinned.
func workOneIngestJob(t *testing.T, e *integration.Env, worker *telegramIngestWorker, rawID string) {
	t.Helper()
	var args TelegramIngestArgs
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT args FROM river_job WHERE kind = $1 AND args->>'raw_capture_id' = $2`,
		TelegramIngestArgs{}.Kind(), rawID).Scan(&args); err != nil {
		t.Fatalf("reading the enqueued args for raw row %s: %v", rawID, err)
	}
	if err := worker.Work(context.Background(), &river.Job[TelegramIngestArgs]{Args: args}); err != nil {
		t.Fatalf("ingest Work: %v", err)
	}
}

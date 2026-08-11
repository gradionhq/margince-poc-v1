// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package channels

// The shared fixture the Telegram acceptance suite rides: the composed api
// role with the channel surface live, the ONE faked boundary (the Telegram
// Bot API), the worker role's runner — which is where ingress now lives, because
// updates are POLLED rather than delivered — and the update builder every test
// puts into the provider's hands.
//
// It is its own file because it is what GREW — four suites now share it — and
// because a reader looking for what a criterion asserts should not have to
// walk past two hundred lines of arrange to find it. The criteria live in
// telegram_integration_test.go (connect and admission),
// telegram_ingress_integration_test.go, telegram_identity_integration_test.go
// and telegram_roundtrip_integration_test.go.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// The workspace's bot, as BotFather would have issued it. The numeric id
	// before the colon is the bot id Telegram reports from getMe, and it is
	// also half of every natural key this suite asserts on, so the two must
	// agree — hence one literal each rather than a token assembled inline.
	telegramBotID    = int64(8100)
	telegramBotToken = "8100:AAH-acceptance-fixture-bot-token"
	telegramBotUser  = "acme_support_bot"

	// telegramWorkspaceName / telegramAdminEmail bootstrap the installation.
	telegramWorkspaceName = "Telegram Acceptance"
	telegramWorkspaceSlug = "telegram-acceptance"
	telegramAdminEmail    = "admin@telegram.test"
)

// fakeTelegramAPI is the ONE mocked boundary in this suite: Telegram itself.
// It records the ORDER of the calls it received, because the order is what
// AC-TG-1 is about — a token validated against the provider before anything is
// stored — and an out-of-order connect would still leave a correct-looking row
// behind.
//
// getUpdates implements the real OFFSET CONTRACT rather than handing out scripted
// batches: it answers every update it still holds at or above the asked offset,
// and forgets everything below it, because asking for offset N+1 IS the
// acknowledgement of N. A scripted fake would happily "re-deliver" an update
// Telegram had already been told to forget, which is the one thing that contract
// rules out — and every durability claim in this suite rests on it.
type fakeTelegramAPI struct {
	mu    sync.Mutex
	calls []string

	bot telegram.Bot

	// held is every update Telegram is still holding for this bot, oldest first.
	held []heldUpdate
	// polledOffsets is the offset each getUpdates asked from, in order.
	polledOffsets []int64
	// gotAllowedUpdates is the subscription the last poll narrowed itself to.
	gotAllowedUpdates []string

	// sent is every outbound message the send path transmitted. nextMessageID
	// is the provider message id handed back, incremented per send so a reply
	// threaded under one can be told from a reply threaded under another.
	sent          []telegram.OutboundChannelMessage
	nextMessageID int64

	// onGetMe runs inside GetMe, which is the only moment a test can observe
	// the system's state BEFORE the connect wrote or sealed anything.
	onGetMe func()
}

// heldUpdate is one update Telegram has not yet been told to forget.
type heldUpdate struct {
	id  int64
	raw json.RawMessage
}

// hold puts one update into Telegram's hands. Nothing is delivered by this call:
// the next poll is what collects it, exactly as a real bot's traffic arrives.
func (f *fakeTelegramAPI) hold(raw json.RawMessage) {
	id, ok := telegram.UpdateIDOf(raw)
	if !ok {
		panic("fakeTelegramAPI: an update with no usable update_id could not be acknowledged, so Telegram would never stop sending it")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = append(f.held, heldUpdate{id: id, raw: raw})
}

func (f *fakeTelegramAPI) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

// callOrder is the sequence of Bot API calls this fake saw.
func (f *fakeTelegramAPI) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// sentMessages is every message the send path actually transmitted.
func (f *fakeTelegramAPI) sentMessages() []telegram.OutboundChannelMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]telegram.OutboundChannelMessage(nil), f.sent...)
}

func (f *fakeTelegramAPI) GetMe(context.Context, string) (telegram.Bot, error) {
	f.record("getMe")
	if f.onGetMe != nil {
		f.onGetMe()
	}
	return f.bot, nil
}

func (f *fakeTelegramAPI) DeleteWebhook(context.Context, string) error {
	f.record("deleteWebhook")
	return nil
}

// GetUpdates answers the offset contract: everything still held at or above
// offset, with the batch's highest id, and the acknowledgement of everything
// below applied first.
func (f *fakeTelegramAPI) GetUpdates(_ context.Context, _ string, offset int64, _ int, allowed []string) ([]json.RawMessage, int64, error) {
	f.record("getUpdates")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polledOffsets = append(f.polledOffsets, offset)
	f.gotAllowedUpdates = allowed
	f.held = slices.DeleteFunc(f.held, func(h heldUpdate) bool { return h.id < offset })

	batch := make([]json.RawMessage, 0, len(f.held))
	var highest int64
	for _, h := range f.held {
		batch = append(batch, h.raw)
		highest = max(highest, h.id)
	}
	return batch, highest, nil
}

func (f *fakeTelegramAPI) SendMessage(_ context.Context, _ string, m telegram.OutboundChannelMessage) (int64, error) {
	f.record("sendMessage")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	f.nextMessageID++
	return 900000 + f.nextMessageID, nil
}

// telegramEnv is the composed installation this suite drives: the api role's
// real router with the channel surfaces wired, plus the ids and credentials
// every test needs to address it.
type telegramEnv struct {
	*apptest.AppEnv
	vault keyvault.Vault
	api   *fakeTelegramAPI
	// inserter is an insert-only River client, used to put one poll job on the
	// queue when a test wants ingress to run now rather than on the dispatcher's
	// next tick.
	inserter *jobs.Runner
	log      *slog.Logger

	ws, admin string
	// conn is the live bot binding Connect wrote.
	conn capture.ChannelConnection
}

// setupTelegram boots the api composition with the channel surface live and then
// binds the workspace's bot through the REAL ChannelStore over the fake provider.
// There is no ingress surface on the api role at all any more: updates are polled
// by the worker role (newTelegramWorker).
//
// Connect runs rather than a hand-inserted row on purpose: every later test
// reads what connect wrote (the vault refs the webhook unseals, the bot id the
// natural keys are namespaced on), and a fixture row could agree with the
// tests while disagreeing with production.
func setupTelegram(t *testing.T) *telegramEnv {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The vault and the job inserter both need a pool before apptest.SetupAppWithOptions
	// has opened the harness's own — the separate-connection precedent
	// setupPreflight uses for exactly this reason.
	pool := apptest.EarlyPool(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generating a test root key: %v", err)
	}
	vault, err := keyvault.New(keyvault.Config{RootKey: key, Pool: pool})
	if err != nil {
		t.Fatalf("building the local vault: %v", err)
	}
	inserter, err := jobs.NewInserter(pool, quiet)
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	api := &fakeTelegramAPI{bot: telegram.Bot{ID: telegramBotID, Username: telegramBotUser}}

	// Option ORDER is the contract WithChannelSurface states: the transport is
	// composed first, and WithKeyvault is what hands it the vault it seals with.
	e := apptest.SetupAppWithOptions(t,
		compose.WithChannelSurface(),
		compose.WithKeyvault(vault),
	)
	apptest.BootstrapWorkspaceSession(t, e, telegramWorkspaceName, telegramAdminEmail, "Telegram Admin")
	e.Slug = telegramWorkspaceSlug

	c := &telegramEnv{AppEnv: e, vault: vault, api: api, inserter: inserter, log: quiet}
	c.resolveActors(t)
	return c
}

// resolveActors reads back the bootstrapped workspace and its admin. Both are
// needed as raw ids: the workspace to bind a tenant context the HTTP session
// does not cover, and the admin because channel_connection.connected_by
// carries a real composite foreign key.
func (c *telegramEnv) resolveActors(t *testing.T) {
	t.Helper()
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT workspace_id, id FROM app_user WHERE email = $1`, telegramAdminEmail).Scan(&c.ws, &c.admin)
	}); err != nil {
		t.Fatalf("resolving the acting admin: %v", err)
	}
}

// workspaceID is the bootstrapped workspace as a typed id.
func (c *telegramEnv) workspaceID(t *testing.T) ids.UUID {
	t.Helper()
	id, err := ids.Parse(c.ws)
	if err != nil {
		t.Fatalf("parsing the workspace id: %v", err)
	}
	return id
}

// adminCtx binds the principal Connect requires: a human on a full seat
// holding the channel_connection admin grants, under the bootstrapped
// workspace. The HTTP session cannot serve here — the store is called
// directly, so the tenant and the actor have to be bound explicitly.
func (c *telegramEnv) adminCtx(t *testing.T) context.Context {
	t.Helper()
	user, err := ids.Parse(c.admin)
	if err != nil {
		t.Fatalf("parsing the admin id: %v", err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), c.workspaceID(t))
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + c.admin, UserID: user,
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

// strangerRepCtx binds a human who is NOT the connecting admin and belongs to
// no team, on the tightest row scope a seat can hold. Whatever this principal
// can read is workspace-shared by construction rather than by ownership — which
// is the only way to tell a genuinely shared record from one that merely
// happens to belong to the caller.
func (c *telegramEnv) strangerRepCtx(t *testing.T, objects map[string]principal.ObjectGrant) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), c.workspaceID(t))
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:stranger", UserID: ids.NewV7(),
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, Objects: objects, RowScope: principal.RowScopeTeam,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// channelStore is the REAL store the composed transport is built over, wired
// to the same vault the server holds so a token sealed here is a token the
// poller can unseal.
func (c *telegramEnv) channelStore() *capture.ChannelStore {
	return capture.NewChannelStore(c.Pool, c.vault, c.api, c.log)
}

// connectBot binds the workspace's bot and records the live connection.
func (c *telegramEnv) connectBot(t *testing.T) {
	t.Helper()
	conn, err := c.channelStore().Connect(c.adminCtx(t), capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: telegramBotToken,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.Status != "connected" {
		t.Fatalf("Connect left the binding %q — there is no half-connected state under a pull ingress", conn.Status)
	}
	c.conn = conn
}

// setupTelegramConnected is setupTelegram plus the bound bot: the arrange step
// every test past AC-TG-1 shares.
func setupTelegramConnected(t *testing.T) *telegramEnv {
	t.Helper()
	c := setupTelegram(t)
	c.connectBot(t)
	return c
}

// telegramUpdate is one Telegram private-chat message update, in the shape the
// Bot API actually posts. A private chat's id IS the sender's account id, so
// the fixture derives it rather than taking both.
type telegramUpdate struct {
	updateID  int64
	messageID int64
	senderID  int64
	username  string
	firstName string
	text      string
}

// body renders the update as Telegram's own JSON.
func (u telegramUpdate) body(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"update_id": u.updateID,
		"message": map[string]any{
			"message_id": u.messageID,
			// type is load-bearing, not decoration: only a private chat is in
			// scope, and the connector skips every other kind.
			"chat": map[string]any{"id": u.senderID, "type": "private", "username": u.username},
			"from": map[string]any{
				"id": u.senderID, "username": u.username, "first_name": u.firstName,
			},
			// A fixed instant: occurred_at is the provider's own send time, and
			// a wall-clock value here would make the captured row's timestamp
			// depend on when the test ran.
			"date": int64(1785000000),
			"text": u.text,
		},
	})
	if err != nil {
		t.Fatalf("rendering the update: %v", err)
	}
	return raw
}

// naturalKey is the activity source_id this update must be captured under:
// bot:chat:message, chat-scoped because Telegram's message ids repeat across
// chats.
func (u telegramUpdate) naturalKey() string {
	return fmt.Sprintf("%d:%d:%d", telegramBotID, u.senderID, u.messageID)
}

// account is the sender's Telegram account id as the identity tables hold it.
func (u telegramUpdate) account() string { return fmt.Sprintf("%d", u.senderID) }

// arrive puts one update into Telegram's hands and drives the poll that collects
// it, through the SAME worker-role runner production polls with. It returns once
// the poll has completed, which is when the raw row and its ingest job are
// durable — the ingest itself is a separate await, because it is a separate job.
//
// The poll is enqueued explicitly rather than waited for: the dispatcher would
// pick this connection up on its next tick, and a test that waited out a real tick
// would be a sleep in disguise.
func (c *telegramEnv) arrive(t *testing.T, sub <-chan *river.Event, u telegramUpdate) {
	t.Helper()
	c.api.hold(u.body(t))
	c.pollNow(t, sub, u.updateID+1)
}

// pollNow runs one poll of this connection and waits until its cursor proves
// that the update it was asked to collect committed. A runner also starts the
// leader-elected sweep, whose empty poll can complete after this method enqueues
// its own job; waiting for a completion event by kind alone would mistake that
// unrelated poll for the batch this test just handed to Telegram.
func (c *telegramEnv) pollNow(t *testing.T, sub <-chan *river.Event, wantOffset int64) {
	t.Helper()
	if err := c.inserter.Enqueue(context.Background(), compose.TelegramPollArgs{
		Workspace: c.workspaceID(t), ConnectionID: c.conn.ID.String(),
	}, nil); err != nil {
		t.Fatalf("enqueueing a poll: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for {
		// Checked before blocking: the enqueue above dedupes into any poll already
		// in flight (per-bot uniqueness), and that poll's completion event may have
		// been consumed by an earlier await — the cursor itself is the durable
		// evidence, the event only says when to look again.
		if c.pollCursor(t) >= wantOffset {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for poll cursor to reach %d: %v", wantOffset, ctx.Err())
		case ev := <-sub:
			if ev == nil || ev.Job == nil || ev.Job.Kind != (compose.TelegramPollArgs{}).Kind() {
				continue
			}
		}
	}
}

// pollCursor is the offset the next poll will ask from. Advancing it IS the
// acknowledgement of everything below it, so this is the direct read of whether a
// batch was accepted — and, when nothing was stored, of whether the cursor moved
// anyway.
func (c *telegramEnv) pollCursor(t *testing.T) int64 {
	t.Helper()
	var offset int64
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT poll_offset FROM channel_connection WHERE id = $1`, c.conn.ID).Scan(&offset)
	}); err != nil {
		t.Fatalf("reading the poll cursor: %v", err)
	}
	return offset
}

// rewindPollCursor puts the connection's cursor back, which is exactly the state
// a crash between Telegram answering and the transaction committing leaves behind:
// the batch was never acknowledged, so the next poll asks for it again.
func (c *telegramEnv) rewindPollCursor(t *testing.T, offset int64) {
	t.Helper()
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE channel_connection SET poll_offset = $2 WHERE id = $1`, c.conn.ID, offset)
		return err
	}); err != nil {
		t.Fatalf("rewinding the poll cursor: %v", err)
	}
}

// count runs one scalar count under the bootstrapped workspace's GUC. Every
// table this suite reads carries FORCE RLS, so a query with no GUC set would
// answer zero regardless of what the table holds — the deny-on-unset policy
// applies to a test's own assertions exactly as it does to production code.
func (c *telegramEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := apptest.InWorkspace(c.AppEnv, t, c.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("counting (%s): %v", query, err)
	}
	return n
}

// rawCaptures counts the raw rows stored for one Telegram update_id. Matched
// through the payload's own update_id rather than through source_id: the stored
// key namespaces the update on the bot whose counter it came from (update_id is
// per-bot), and what this suite asks is how many rows one update left behind,
// not how the key spells it.
func (c *telegramEnv) rawCaptures(t *testing.T, updateID int64) int {
	t.Helper()
	return c.count(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND payload->>'update_id' = $1`,
		fmt.Sprintf("%d", updateID))
}

// ingestJobs counts the normalize jobs enqueued against this connection.
// river_job is not workspace-scoped, so it is read off the pool directly.
func (c *telegramEnv) ingestJobs(t *testing.T) int {
	t.Helper()
	var n int
	if err := c.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'connection_id' = $2`,
		compose.TelegramIngestArgs{}.Kind(), c.conn.ID.String()).Scan(&n); err != nil {
		t.Fatalf("counting ingest jobs: %v", err)
	}
	return n
}

// newTelegramWorker builds the WORKER role's runner — the same
// compose.NewJobRunner cmd/worker calls, so the poll dispatcher, the poll worker
// and the ingest worker under test are the registered ones — and its completion
// feed. Nothing is started yet, so a test can arrange before ingress runs.
//
// ChannelAPI is the fake: left nil the poller would compose the real Bot API
// client and this suite would reach api.telegram.org.
func newTelegramWorker(t *testing.T, c *telegramEnv, cfg compose.JobRunnerConfig) (*jobs.Runner, <-chan *river.Event) {
	t.Helper()
	integration.ApplyRiverSchema(t)
	cfg.CloseDateInterval, cfg.ReconcileInterval, cfg.TimeScanInterval = time.Hour, time.Hour, time.Hour
	cfg.ChannelVault, cfg.ChannelAPI = c.vault, c.api
	runner, err := compose.NewJobRunner(c.Pool, c.log, cfg)
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	// Subscribe BEFORE Start so no completion is missed — a job enqueued
	// before the runner booted completes during startup.
	sub, cancelSub := runner.SubscribeCompleted()
	t.Cleanup(cancelSub)
	return runner, sub
}

// startTelegramWorker starts the runner and registers its drain.
func startTelegramWorker(t *testing.T, runner *jobs.Runner) {
	t.Helper()
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("starting the worker: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Stop(stopCtx); err != nil {
			t.Errorf("stopping the worker: %v", err)
		}
	})
}

// awaitJobKind blocks until one job of the given kind reports completion. No
// polling and no sleep: River's completion feed says exactly when an async leg
// is done, which is the only honest way to observe a path whose whole point is
// that it finishes after the request did.
func awaitJobKind(t *testing.T, sub <-chan *river.Event, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	integration.AwaitKindCompleted(ctx, t, sub, kind)
}

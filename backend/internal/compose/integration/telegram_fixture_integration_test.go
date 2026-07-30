// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The shared fixture the Telegram acceptance suite rides: the composed api
// role with both channel surfaces live, the ONE faked boundary (the Telegram
// Bot API), the worker role's runner, and the update builder every test
// delivers through.
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
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose"
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

	// telegramWebhookBase is the installation's externally-reachable origin,
	// the base Connect registers the delivery URL against. It is asserted
	// against the URL the fake provider received, so one literal serves both.
	telegramWebhookBase = "https://telegram.acceptance.test"

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
type fakeTelegramAPI struct {
	mu    sync.Mutex
	calls []string

	bot telegram.Bot

	// What setWebhook was handed. The secret is the ingress credential every
	// later delivery in this suite is authenticated with, so it is read back
	// from here rather than re-derived.
	gotWebhookURL     string
	gotWebhookSecret  string
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

// GetWebhookInfo answers as a fresh bot does: no webhook registered anywhere,
// so the connect preflight has nothing to refuse. The preflight's own conflict
// case is proven in package capture, against a fake that reports another
// installation's URL.
func (f *fakeTelegramAPI) GetWebhookInfo(context.Context, string) (telegram.WebhookInfo, error) {
	f.record("getWebhookInfo")
	return telegram.WebhookInfo{}, nil
}

func (f *fakeTelegramAPI) SetWebhook(_ context.Context, _, url, secret string, allowed []string) error {
	f.record("setWebhook")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotWebhookURL, f.gotWebhookSecret, f.gotAllowedUpdates = url, secret, allowed
	return nil
}

func (f *fakeTelegramAPI) DeleteWebhook(context.Context, string) error {
	f.record("deleteWebhook")
	return nil
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
	*env
	vault keyvault.Vault
	api   *fakeTelegramAPI
	// inserter is the api role's insert-only River client — the same shape
	// cmd/api holds, so the webhook's enqueue is the real one.
	inserter *jobs.Runner
	log      *slog.Logger

	ws, admin string
	// conn is the live bot binding Connect wrote, and secret is the ingress
	// credential it registered with Telegram.
	conn   capture.ChannelConnection
	secret string
}

// setupTelegram boots the api composition with BOTH channel surfaces live —
// the connect/read transport (WithChannelWebhookBase) and the ingress webhook
// (WithTelegramWebhook) — and then binds the workspace's bot through the REAL
// ChannelStore over the fake provider.
//
// Connect runs rather than a hand-inserted row on purpose: every later test
// reads what connect wrote (the vault refs the webhook unseals, the bot id the
// natural keys are namespaced on), and a fixture row could agree with the
// tests while disagreeing with production.
func setupTelegram(t *testing.T) *telegramEnv {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The vault and the job inserter both need a pool before setupWithOptions
	// has opened the harness's own — the separate-connection precedent
	// setupPreflight uses for exactly this reason.
	pool := preflightAppPool(t)
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

	// Option ORDER is the contract both channel options state: the origin and
	// the job inserter must be recorded before WithKeyvault, which is what
	// composes the connect transport and the ingress webhook over them.
	e := setupWithOptions(t,
		compose.WithChannelWebhookBase(telegramWebhookBase),
		compose.WithTelegramWebhook(inserter),
		compose.WithKeyvault(vault),
	)
	bootstrapWorkspaceSession(t, e, telegramWorkspaceName, telegramAdminEmail, "Telegram Admin")
	e.slug = telegramWorkspaceSlug

	c := &telegramEnv{env: e, vault: vault, api: api, inserter: inserter, log: quiet}
	c.resolveActors(t)
	return c
}

// resolveActors reads back the bootstrapped workspace and its admin. Both are
// needed as raw ids: the workspace to bind a tenant context the HTTP session
// does not cover, and the admin because channel_connection.connected_by
// carries a real composite foreign key.
func (c *telegramEnv) resolveActors(t *testing.T) {
	t.Helper()
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
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
// to the same vault the server holds so a secret sealed here is a secret the
// ingress webhook can unseal.
func (c *telegramEnv) channelStore() *capture.ChannelStore {
	return capture.NewChannelStore(c.pool, c.vault, c.api, telegramWebhookBase, c.log)
}

// connectBot binds the workspace's bot and records the live connection plus
// the ingress secret every later delivery authenticates with.
func (c *telegramEnv) connectBot(t *testing.T) {
	t.Helper()
	conn, err := c.channelStore().Connect(c.adminCtx(t), capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: telegramBotToken,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	c.conn, c.secret = conn, c.api.gotWebhookSecret
	if c.secret == "" {
		t.Fatal("Connect returned without registering a webhook secret — the ingress path has no credential")
	}
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
			"chat":       map[string]any{"id": u.senderID},
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

// deliver posts one update to the REAL mounted ingress route with the
// connection's registered secret, and returns the status and the verbatim
// response body — the body matters because a refusal must name nothing.
func (c *telegramEnv) deliver(t *testing.T, u telegramUpdate) (int, string) {
	t.Helper()
	return c.post(t, c.conn.ID.String(), c.secret, u.body(t))
}

// post is deliver's unauthenticated sibling: any connection id, any secret,
// any bytes. AC-TG-2 needs all three to vary.
func (c *telegramEnv) post(t *testing.T, connectionID, secret string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		c.ts.URL+"/webhooks/telegram/"+connectionID, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("building the delivery: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatalf("delivering the update: %v", err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the delivery response: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// count runs one scalar count under the bootstrapped workspace's GUC. Every
// table this suite reads carries FORCE RLS, so a query with no GUC set would
// answer zero regardless of what the table holds — the deny-on-unset policy
// applies to a test's own assertions exactly as it does to production code.
func (c *telegramEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
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
	if err := c.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'connection_id' = $2`,
		compose.TelegramIngestArgs{}.Kind(), c.conn.ID.String()).Scan(&n); err != nil {
		t.Fatalf("counting ingest jobs: %v", err)
	}
	return n
}

// newTelegramWorker builds the WORKER role's runner — the same
// compose.NewJobRunner cmd/worker calls, so the Telegram ingest worker under
// test is the registered one — and its completion feed. Nothing is started:
// several tests must observe the system between the webhook's ack and the
// job's run, which is only possible while the queue is idle.
func newTelegramWorker(t *testing.T, c *telegramEnv, cfg compose.JobRunnerConfig) (*jobs.Runner, <-chan *river.Event) {
	t.Helper()
	ApplyRiverSchema(t)
	cfg.CloseDateInterval, cfg.ReconcileInterval, cfg.TimeScanInterval = time.Hour, time.Hour, time.Hour
	runner, err := compose.NewJobRunner(c.pool, c.log, cfg)
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
	awaitKindCompleted(ctx, t, sub, kind)
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The Telegram ingress webhook end to end (design §6.1, §6.2, the review's
// first Critical finding): a wrong secret is refused before any work; the
// raw row and its enqueue commit atomically or not at all — proved by
// injecting a failure at the real transactional seam, not merely the happy
// path; and a redelivered update_id yields exactly one raw_capture row and
// one river job, never two. None of these are provable against a mock
// pool: the claim in each case is about what a real transaction actually
// committed or rolled back.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// telegramWebhookFakeAPI is the provider boundary the connect step needs —
// the ingress suite never calls GetMe/SetWebhook itself, it only needs a
// live `connected` row to deliver against, so this fixture is deliberately
// thinner than the full connect suite's fakeTelegram.
type telegramWebhookFakeAPI struct {
	bot          telegram.Bot
	sentSecret   string
	sentURL      string
	setWebhookOK bool
}

func (f *telegramWebhookFakeAPI) GetMe(context.Context, string) (telegram.Bot, error) {
	return f.bot, nil
}

func (f *telegramWebhookFakeAPI) GetWebhookInfo(context.Context, string) (telegram.WebhookInfo, error) {
	return telegram.WebhookInfo{}, nil
}

func (f *telegramWebhookFakeAPI) SetWebhook(_ context.Context, _, url, secret string, _ []string) error {
	f.sentURL, f.sentSecret = url, secret
	f.setWebhookOK = true
	return nil
}

func (f *telegramWebhookFakeAPI) DeleteWebhook(context.Context, string) error { return nil }

func (f *telegramWebhookFakeAPI) SendMessage(context.Context, string, telegram.OutboundChannelMessage) (int64, error) {
	panic("telegramWebhookFakeAPI: the ingress suite never sends")
}

// telegramWebhookAdminContext binds the principal Connect needs: a human on
// a full seat holding the channel_connection admin grants. e.Rep1 already
// exists as an app_user in e.WS (integration.Setup seeds it), which the
// connected_by composite FK requires.
func telegramWebhookAdminContext(ws, user ids.UUID) context.Context {
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

// connectTestTelegramBot runs the real Connect flow (seals real vault
// refs, writes the real row) so the ingress suite exercises exactly what
// production ResolveChannelConnection reads — never a hand-inserted row
// that could drift from what Connect actually writes.
func connectTestTelegramBot(t *testing.T, e *integration.Env, vault keyvault.Vault, botID int64, username string) (capture.ChannelConnection, string) {
	t.Helper()
	api := &telegramWebhookFakeAPI{bot: telegram.Bot{ID: botID, Username: username}}
	store := capture.NewChannelStore(e.Pool, vault, api, "https://telegram-webhook-test.example", nil)
	ctx := telegramWebhookAdminContext(e.WS, e.Rep1)
	conn, err := store.Connect(ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram,
		BotToken: fmt.Sprintf("%d:AAH-fixture-secret-for-%s", botID, username),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !api.setWebhookOK {
		t.Fatal("Connect did not reach SetWebhook — the row is not live")
	}
	return conn, api.sentSecret
}

// telegramUpdateBody renders a minimal, decodable Telegram update carrying
// just the field the handler actually reads.
func telegramUpdateBody(updateID int64) []byte {
	return []byte(fmt.Sprintf(`{"update_id":%d,"message":{"message_id":1,"text":"hi"}}`, updateID))
}

func postTelegramWebhook(t *testing.T, srv *httptest.Server, connID ids.UUID, secret string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhooks/telegram/"+connID.String(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set(telegramSecretHeader, secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func newTelegramTestMux(pool *pgxpool.Pool, vault keyvault.Vault, enqueuer telegramEnqueuer) http.Handler {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle("/webhooks/telegram/{connection_id}", Webhook(telegramWebhookSpec(pool, vault, enqueuer, quiet), quiet))
	return mux
}

// rawCaptureCount reads through a workspace-bound transaction, never the
// raw pool: raw_capture carries FORCE RLS, so a query with no GUC set
// would silently return zero regardless of what the table holds — the
// deny-on-unset policy applies to the test's own assertions exactly as it
// does to production code.
//
// Keyed through telegramRawSourceID, the production spelling, so an assertion
// here can never pass against a key shape the handler has stopped writing.
func rawCaptureCount(t *testing.T, e *integration.Env, conn capture.ChannelConnection, updateID int64) int {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND source_id = $1`,
		telegramRawSourceID(conn.ChannelID, updateID))
}

// assertRawRowHoldsItsOwnBotsMessage reads back the row one bot's update was
// stored under and insists it still carries the bytes THAT bot delivered,
// returning the row id so the caller can also prove the two bots' deliveries
// did not converge onto one row.
func assertRawRowHoldsItsOwnBotsMessage(t *testing.T, e *integration.Env, conn capture.ChannelConnection, updateID, senderID int64, text string) ids.UUID {
	t.Helper()
	var id ids.UUID
	var gotText, gotSender string
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, payload->'message'->>'text', payload->'message'->'from'->>'id'
			  FROM raw_capture WHERE source_system = 'telegram' AND source_id = $1`,
			telegramRawSourceID(conn.ChannelID, updateID)).Scan(&id, &gotText, &gotSender)
	}); err != nil {
		t.Fatalf("reading the raw capture for bot %s update %d: %v", conn.ChannelID, updateID, err)
	}
	if gotText != text || gotSender != fmt.Sprintf("%d", senderID) {
		t.Errorf("bot %s's raw row holds %q from sender %s, want %q from %d",
			conn.ChannelID, gotText, gotSender, text, senderID)
	}
	return id
}

// telegramEnqueuedRawIDs is the raw row each of one connection's ingest jobs
// was told to normalize. river_job is not workspace-scoped, so it is read off
// the pool directly.
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

// telegramChatUpdateBody renders a full private-chat message update — chat,
// sender, the provider's own send time and text — so the ingest worker can
// normalize it into a real activity. telegramUpdateBody is deliberately
// thinner: the webhook itself reads only update_id.
func telegramChatUpdateBody(updateID, senderID, messageID int64, text string) []byte {
	return []byte(fmt.Sprintf(
		`{"update_id":%d,"message":{"message_id":%d,"chat":{"id":%d},`+
			`"from":{"id":%d,"username":"sender%d","first_name":"Sender %d"},`+
			`"date":1785000000,"text":%q}}`,
		updateID, messageID, senderID, senderID, senderID, senderID, text))
}

// deliverAccepted posts one update as the named bot and insists the webhook
// accepted it — the arrange step of any test whose claims are about what an
// acknowledged delivery left behind.
func deliverAccepted(t *testing.T, srv *httptest.Server, conn capture.ChannelConnection, secret string, body []byte) {
	t.Helper()
	resp := postTelegramWebhook(t, srv, conn.ID, secret, body)
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing bot %s's response body: %v", conn.ChannelID, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bot %s's delivery status = %d, want 200", conn.ChannelID, resp.StatusCode)
	}
}

// workOneIngestJob runs one enqueued job through the real worker, with the args
// the webhook actually stamped rather than args assembled by the caller.
func workOneIngestJob(t *testing.T, e *integration.Env, worker *telegramIngestWorker, conn capture.ChannelConnection, rawID string) {
	t.Helper()
	if err := worker.Work(context.Background(), &river.Job[TelegramIngestArgs]{
		Args: TelegramIngestArgs{
			Workspace: e.WS.String(), ConnectionID: conn.ID.String(), RawCaptureID: rawID,
		},
	}); err != nil {
		t.Fatalf("Work for bot %s: %v", conn.ChannelID, err)
	}
}

// riverJobCount counts the jobs enqueued for one connection. Scoping to the
// connection under test is what makes the assertion a statement about THIS
// delivery rather than about the existence of some telegram_ingest row: both
// deliveries carry the same path-derived connection id, so a duplicate can
// only raise the count, and an args shape that drifted would drop it to zero.
func riverJobCount(t *testing.T, e *integration.Env, kind, connectionID string) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'connection_id' = $2`,
		kind, connectionID).Scan(&n); err != nil {
		t.Fatalf("counting river_job rows: %v", err)
	}
	return n
}

// A wrong (or absent) secret must be refused before any work — no raw row,
// no enqueue — and the response must be a bare 401 naming nothing, so an
// attacker probing this unauthenticated edge learns nothing about which
// connection ids exist.
func TestWrongSecretIsRefusedAndCapturesNothing(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	conn, _ := connectTestTelegramBot(t, e, vault, 91000001, "wrong_secret_bot")

	srv := httptest.NewServer(newTelegramTestMux(e.Pool, vault, &fakeInserter{}))
	defer srv.Close()

	const updateID = 5001
	resp := postTelegramWebhook(t, srv, conn.ID, "not-the-secret", telegramUpdateBody(updateID))
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if n := rawCaptureCount(t, e, conn, updateID); n != 0 {
		t.Fatalf("raw_capture rows after a wrong secret = %d, want 0", n)
	}
}

// The raw insert and the job enqueue must commit together or not at all.
// EnqueueTx is stubbed to fail INSIDE the real transaction the handler
// opens — the property under test is that InsertRawCaptureTx's write does
// not survive that failure, which only a real rollback can prove.
func TestRawCaptureAndJobCommitAtomically(t *testing.T) {
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	conn, secret := connectTestTelegramBot(t, e, vault, 91000002, "atomic_bot")

	failing := &fakeInserter{err: errors.New("injected: enqueue fails inside the real transaction")}
	srv := httptest.NewServer(newTelegramTestMux(e.Pool, vault, failing))
	defer srv.Close()

	const updateID = 5002
	resp := postTelegramWebhook(t, srv, conn.ID, secret, telegramUpdateBody(updateID))
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (Transient — redelivery is the recovery path)", resp.StatusCode)
	}
	if n := rawCaptureCount(t, e, conn, updateID); n != 0 {
		t.Fatalf("raw_capture rows after a failed enqueue = %d, want 0 — the raw insert must not survive the enqueue's rollback", n)
	}
}

// A redelivered update_id must land exactly once: raw_capture's ON
// CONFLICT refreshes the existing row (never a second one), and river's
// UniqueOpts{ByArgs:true} — proved here against the REAL river schema, not
// a fake enqueuer — dedupes the second enqueue rather than queueing it
// again.
func TestRedeliveredUpdateYieldsOneRawRowAndOneJob(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()
	conn, secret := connectTestTelegramBot(t, e, vault, 91000003, "redelivery_bot")

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inserter, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	srv := httptest.NewServer(newTelegramTestMux(e.Pool, vault, inserter))
	defer srv.Close()

	const updateID = 5003
	body := telegramUpdateBody(updateID)

	first := postTelegramWebhook(t, srv, conn.ID, secret, body)
	if err := first.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first delivery status = %d, want 200", first.StatusCode)
	}

	second := postTelegramWebhook(t, srv, conn.ID, secret, body)
	if err := second.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
	if second.StatusCode != http.StatusOK {
		t.Fatalf("redelivered status = %d, want 200 (a redelivery is still accepted)", second.StatusCode)
	}

	if n := rawCaptureCount(t, e, conn, updateID); n != 1 {
		t.Fatalf("raw_capture rows for a redelivered update_id = %d, want 1", n)
	}
	if n := riverJobCount(t, e, "telegram_ingest", conn.ID.String()); n != 1 {
		t.Fatalf("river_job rows for the redelivered update = %d, want 1 — 2 means river's ByArgs dedupe did not skip the second insert; 0 means the handler never enqueued, or stamped a connection_id the args do not carry", n)
	}
}

// Telegram's update_id is a PER-BOT counter, and uq_channel_connection_ws
// permits one workspace to hold several live bots, so two bots' counters can
// reach the same value. Each delivery must keep its OWN raw row with its OWN
// payload, and become its OWN activity under its own bot's natural key.
//
// Keyed on a bare update_id this is not a collision but a data-loss
// primitive: InsertRawCaptureTx's ON CONFLICT overwrites the payload, so the
// second bot's delivery destroys the first bot's only copy of a message the
// webhook had already answered 200 for — unrecoverable, because Telegram has
// no history API to re-fetch it from. Both jobs then name the one surviving
// row, so the first bot's job also mints an activity for the other bot's
// private-chat message, stamped with the wrong bot.
//
// The raw-row count is asserted through the payload's own update_id rather
// than through the stored key, so it measures how many rows the two
// deliveries actually left behind however the key is spelled.
func TestTwoBotsSharingAnUpdateIDKeepSeparateRawRowsAndActivities(t *testing.T) {
	e := integration.Setup(t)
	integration.ApplyRiverSchema(t)
	vault := keyvault.NewMemory()
	connA, secretA := connectTestTelegramBot(t, e, vault, 91000004, "collide_bot_a")
	connB, secretB := connectTestTelegramBot(t, e, vault, 91000005, "collide_bot_b")

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	inserter, err := jobs.NewInserter(e.Pool, quiet)
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	srv := httptest.NewServer(newTelegramTestMux(e.Pool, vault, inserter))
	defer srv.Close()

	// The one update_id both bots' independent counters happen to have reached.
	const updateID = 7777
	const (
		senderA, messageA = int64(770601), int64(61)
		senderB, messageB = int64(770602), int64(62)
	)
	textA, textB := "bot A's customer wrote this", "bot B's customer wrote this"

	deliverAccepted(t, srv, connA, secretA, telegramChatUpdateBody(updateID, senderA, messageA, textA))
	deliverAccepted(t, srv, connB, secretB, telegramChatUpdateBody(updateID, senderB, messageB, textB))

	if n := e.WsCount(t, `
		SELECT count(*) FROM raw_capture
		 WHERE source_system = 'telegram' AND payload->>'update_id' = $1`,
		fmt.Sprintf("%d", updateID)); n != 2 {
		t.Fatalf("%d raw rows for update_id %d across two bots, want 2 — one bot's delivery "+
			"overwrote the other's, and the overwritten message was already acknowledged", n, updateID)
	}

	rawA := assertRawRowHoldsItsOwnBotsMessage(t, e, connA, updateID, senderA, textA)
	rawB := assertRawRowHoldsItsOwnBotsMessage(t, e, connB, updateID, senderB, textB)
	if rawA == rawB {
		t.Fatalf("both bots' updates resolved to raw_capture row %s — one payload cannot hold two messages", rawA)
	}

	// Each job must point at its OWN bot's row. River's ByArgs dedupe cannot
	// merge these two — connection_id differs — so a shared raw id would have
	// both jobs normalizing the same surviving payload.
	jobsA, jobsB := telegramEnqueuedRawIDs(t, e, connA.ID.String()), telegramEnqueuedRawIDs(t, e, connB.ID.String())
	if len(jobsA) != 1 || len(jobsB) != 1 {
		t.Fatalf("ingest jobs = %d for bot A and %d for bot B, want 1 each", len(jobsA), len(jobsB))
	}
	if jobsA[0] != rawA.String() || jobsB[0] != rawB.String() {
		t.Fatalf("bot A's job names raw row %s and bot B's names %s, want %s and %s — a job pointed at the other bot's payload",
			jobsA[0], jobsB[0], rawA, rawB)
	}

	// And the activities the two jobs produce belong to different channels: with
	// one surviving raw row, bot A's job would normalize bot B's message and
	// stamp it under bot A's id.
	worker := newTelegramIngestWorker(e.Pool, CaptureConfig{}, quiet)
	workOneIngestJob(t, e, worker, connA, jobsA[0])
	workOneIngestJob(t, e, worker, connB, jobsB[0])

	for _, want := range []string{
		fmt.Sprintf("%s:%d:%d", connA.ChannelID, senderA, messageA),
		fmt.Sprintf("%s:%d:%d", connB.ChannelID, senderB, messageB),
	} {
		if n := e.WsCount(t,
			`SELECT count(*) FROM activity WHERE source_system = 'telegram' AND source_id = $1`, want); n != 1 {
			t.Errorf("%d activities under natural key %s, want 1 — each bot's message belongs to its own channel", n, want)
		}
	}
	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE source_system = 'telegram'`); n != 2 {
		t.Errorf("%d telegram activities, want exactly 2 — one per bot's message", n)
	}
}

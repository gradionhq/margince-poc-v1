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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
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
func rawCaptureCount(t *testing.T, e *integration.Env, sourceID string) int {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM raw_capture WHERE source_system = 'telegram' AND source_id = $1`, sourceID)
}

func riverJobCount(t *testing.T, e *integration.Env, kind string) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`, kind).Scan(&n); err != nil {
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
	if n := rawCaptureCount(t, e, fmt.Sprintf("%d", updateID)); n != 0 {
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
	if n := rawCaptureCount(t, e, fmt.Sprintf("%d", updateID)); n != 0 {
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
	applyRiverSchema(t)
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

	if n := rawCaptureCount(t, e, fmt.Sprintf("%d", updateID)); n != 1 {
		t.Fatalf("raw_capture rows for a redelivered update_id = %d, want 1", n)
	}
	if n := riverJobCount(t, e, "telegram_ingest"); n != 1 {
		t.Fatalf("river_job rows for the redelivered update = %d, want 1 — river's ByArgs dedupe must have skipped the second insert", n)
	}
}

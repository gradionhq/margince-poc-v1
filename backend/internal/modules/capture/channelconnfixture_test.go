// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The fixture the channel-connection suite runs on: a real migrated Postgres
// (the connect ordering, the two partial unique indexes and the audit write are
// all SQL facts a mock could only pretend to have), a real in-memory vault
// wrapped so a test can assert that NOTHING was sealed, and a fake Telegram API
// — the one true boundary here, and the only thing that must never be reached
// over the network from a test.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fakeTelegram is the provider boundary. Each field is what the next call
// answers, so a test states the provider's behaviour as data rather than as
// control flow, and the recorded calls let a test assert on what was asked of
// the provider — which is the whole point of the connect ordering.
type fakeTelegram struct {
	mu sync.Mutex

	// bots maps a token to the bot it identifies. A token absent from the map
	// is one Telegram rejects. Two tokens may name the SAME bot, which is what a
	// BotFather rotation produces.
	bots map[string]telegram.Bot
	// deleteWebhookErr, when non-nil, is what DeleteWebhook answers — the one
	// provider refusal that can stop a connect after getMe has already succeeded.
	deleteWebhookErr error
	// deleteWebhookHook runs once, before the next DeleteWebhook takes the lock, so
	// a test can drive a second concurrent lifecycle operation while this one is
	// mid-flight at the provider. It fires outside the mutex because what it drives
	// calls back into this same fake.
	deleteWebhookHook func(token string)

	getMeTokens         []string
	deleteWebhookTokens []string
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{bots: map[string]telegram.Bot{}}
}

// nextBotID hands out a bot id unique within the test binary. It has to be
// unique because uq_channel_connection_bot is GLOBAL by design: two tests
// reusing one bot id would collide across their workspaces exactly as two
// installations sharing a bot would, and the second would fail for a reason
// that has nothing to do with what it is testing.
var nextBotID atomic.Int64

// withNewBot registers a fresh bot Telegram accepts and returns its token and
// id. The token's shape is the one BotFather issues, because ValidateToken
// refuses anything else before a call is spent.
func (f *fakeTelegram) withNewBot(username string) (token string, id int64) {
	id = 8100000000 + nextBotID.Add(1)
	token = fmt.Sprintf("%d:AAH-fixture-secret-for-%s", id, username)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bots[token] = telegram.Bot{ID: id, Username: username}
	return token, id
}

// onNextDeleteWebhook arms the one-shot hook: fn runs when the next DeleteWebhook
// call begins, and is disarmed as it fires so what fn itself drives cannot
// re-enter it. DeleteWebhook is the only provider call a connect or a replacement
// makes after reading the row, so it is the only window in which a snapshot can go
// stale.
func (f *fakeTelegram) onNextDeleteWebhook(fn func(token string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteWebhookHook = fn
}

// takeDeleteWebhookHook disarms and returns the hook, under the lock, so it fires
// at most once however many goroutines are in DeleteWebhook.
func (f *fakeTelegram) takeDeleteWebhookHook() func(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hook := f.deleteWebhookHook
	f.deleteWebhookHook = nil
	return hook
}

// clearedWebhooks is every token deleteWebhook was called with, in order.
func (f *fakeTelegram) clearedWebhooks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleteWebhookTokens...)
}

func (f *fakeTelegram) GetMe(_ context.Context, token string) (telegram.Bot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getMeTokens = append(f.getMeTokens, token)
	bot, ok := f.bots[token]
	if !ok {
		return telegram.Bot{}, telegram.ErrTokenRejected
	}
	return bot, nil
}

func (f *fakeTelegram) GetUpdates(context.Context, string, int64, int, []string) ([]json.RawMessage, int64, error) {
	// The connect suite is about the binding lifecycle, never about ingress; a
	// test that polls here is asking the wrong fixture and must be told so.
	panic("fakeTelegram: the channel-connect suite must not poll for updates")
}

func (f *fakeTelegram) DeleteWebhook(_ context.Context, token string) error {
	if hook := f.takeDeleteWebhookHook(); hook != nil {
		hook(token)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteWebhookTokens = append(f.deleteWebhookTokens, token)
	return f.deleteWebhookErr
}

func (f *fakeTelegram) SendMessage(context.Context, string, telegram.OutboundChannelMessage) (int64, error) {
	// The send path is a later work package; a test reaching it is asking the
	// wrong fixture a question, and must be told so rather than get a 0.
	panic("fakeTelegram: the channel-connect suite must not send messages")
}

// countingVault wraps a real vault and records every ref it mints, so a test
// can assert "nothing was persisted" about the vault as well as about the row —
// half the claim would leave a stranded secret invisible — and can find the refs
// of a write that never produced a row to read them off.
type countingVault struct {
	keyvault.Vault
	mu     sync.Mutex
	minted []keyvault.Ref
}

func (v *countingVault) Put(ctx context.Context, ws ids.WorkspaceID, secret []byte) (keyvault.Ref, error) {
	ref, err := v.Vault.Put(ctx, ws, secret)
	if err != nil {
		return "", err
	}
	v.mu.Lock()
	v.minted = append(v.minted, ref)
	v.mu.Unlock()
	return ref, nil
}

// mintedRefs is every ref this vault ever handed out, in order.
func (v *countingVault) mintedRefs() []keyvault.Ref {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]keyvault.Ref(nil), v.minted...)
}

func (v *countingVault) putCount() int { return len(v.mintedRefs()) }

// channelFixture is one workspace's worth of channel-connect machinery.
type channelFixture struct {
	ctx      context.Context
	store    *capture.ChannelStore
	handlers capture.ChannelHandlers
	api      *fakeTelegram
	vault    *countingVault
	ws       ids.UUID
	user     ids.UUID
}

// newChannelFixture bootstraps a fresh workspace + admin human and the store
// under test. api is shared when a test needs two workspaces to talk to the
// SAME provider (the global-unique-bot case); pass nil for a private one.
func newChannelFixture(t *testing.T, api *fakeTelegram) *channelFixture {
	t.Helper()
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()

	wsUUID := ids.NewV7()
	userUUID := ids.NewV7()
	// The full uuid, not a prefix: a v7 id's leading digits are a timestamp,
	// so two workspaces minted in the same millisecond would collide on the
	// slug's unique index.
	slug := "capture-channel-" + wsUUID.String()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Capture Channel', $2, 'USD')`,
		wsUUID, slug); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	if _, err := owner.Exec(ctx,
		`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Channel Admin')`,
		userUUID, wsUUID, "admin-"+userUUID.String()+"@"+slug+".test"); err != nil {
		t.Fatalf("seeding app_user: %v", err)
	}

	if api == nil {
		api = newFakeTelegram()
	}
	vault := &countingVault{Vault: keyvault.NewMemory()}
	store := capture.NewChannelStore(pool, vault, api, nil)

	return &channelFixture{
		ctx:      adminChannelContext(ctx, wsUUID, userUUID),
		store:    store,
		handlers: capture.NewChannelHandlers(store),
		api:      api,
		vault:    vault,
		ws:       wsUUID,
		user:     userUUID,
	}
}

// withoutVault re-points the fixture at a store composed with no credential
// custodian — a deployment that never configured a keyvault. It keeps the same
// pool, so rows an earlier connect wrote are still there for the lifecycle paths
// to reach.
func (f *channelFixture) withoutVault(t *testing.T) {
	t.Helper()
	_, pool := setupCaptureDB(t)
	f.store = capture.NewChannelStore(pool, nil, f.api, nil)
	f.handlers = capture.NewChannelHandlers(f.store)
}

// adminChannelContext binds the principal the HTTP middleware would: a human on
// a full seat holding the admin grants for channel_connection.
func adminChannelContext(ctx context.Context, ws, user ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type:     principal.PrincipalHuman,
		ID:       "human:" + user.String(),
		UserID:   user,
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

// connectRequest builds an in-context POST to the connect endpoint carrying the
// same principal the store paths get, so the transport tests exercise the real
// handler rather than a hand-rolled stand-in.
//
// Connect is the only operation reachable this way: the surface's others take
// their id from the router, which these tests do not mount.
func (f *channelFixture) connectRequest(body string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodPost, "/v1/channel-connections", strings.NewReader(body)).WithContext(f.ctx)
	req.Header.Set("Content-Type", "application/json")
	return httptest.NewRecorder(), req
}

// workspaceKey is the typed workspace id the vault scopes a ref to.
func (f *channelFixture) workspaceKey() ids.WorkspaceID {
	return ids.From[ids.WorkspaceKind](f.ws)
}

// liveConnections is the gated read surface — what the transport would list.
func (f *channelFixture) liveConnections(t *testing.T) []capture.ChannelConnection {
	t.Helper()
	conns, err := f.store.List(f.ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return conns
}

// rowState reads one connection's status/archival/bot straight off the owner
// connection, deliberately bypassing RLS: the assertion is about what the table
// actually holds, not about what one workspace's policy exposes.
func (f *channelFixture) rowState(t *testing.T, id ids.UUID) (status string, archived bool, channelID string) {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	var archivedAt *string
	if err := owner.QueryRow(context.Background(),
		`SELECT status, archived_at::text, channel_id FROM channel_connection WHERE id = $1`, id).
		Scan(&status, &archivedAt, &channelID); err != nil {
		t.Fatalf("reading channel_connection %s: %v", id, err)
	}
	return status, archivedAt != nil, channelID
}

// credentialRefOf reads a connection's sealed-token ref off the owner connection —
// the teardown assertions need it after the row is gone from the read surface.
func (f *channelFixture) credentialRefOf(t *testing.T, id ids.UUID) keyvault.Ref {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	var credentialRef string
	if err := owner.QueryRow(context.Background(),
		`SELECT credential_ref FROM channel_connection WHERE id = $1`, id).Scan(&credentialRef); err != nil {
		t.Fatalf("reading channel_connection credential ref %s: %v", id, err)
	}
	return keyvault.Ref(credentialRef)
}

// pollOffsetOf reads a connection's getUpdates cursor.
func (f *channelFixture) pollOffsetOf(t *testing.T, id ids.UUID) int64 {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	var offset int64
	if err := owner.QueryRow(context.Background(),
		`SELECT poll_offset FROM channel_connection WHERE id = $1`, id).Scan(&offset); err != nil {
		t.Fatalf("reading channel_connection poll offset %s: %v", id, err)
	}
	return offset
}

// setPollOffset moves a connection's cursor, so a test can prove a lifecycle
// change resets it rather than inheriting another bot's sequence.
func (f *channelFixture) setPollOffset(t *testing.T, id ids.UUID, offset int64) {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	if _, err := owner.Exec(context.Background(),
		`UPDATE channel_connection SET poll_offset = $2 WHERE id = $1`, id, offset); err != nil {
		t.Fatalf("setting channel_connection poll offset %s: %v", id, err)
	}
}

// rowCount counts this workspace's connection rows, for the assertions about
// what a refused connect must NOT have written.
func (f *channelFixture) rowCount(t *testing.T) int {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	var n int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM channel_connection WHERE workspace_id = $1`, f.ws).Scan(&n); err != nil {
		t.Fatalf("counting channel_connection rows: %v", err)
	}
	return n
}

// auditActions lists the audit actions recorded against one connection, in
// order — the write shape's audit half, which the store commits inside the same
// transaction as the row.
func (f *channelFixture) auditActions(t *testing.T, id ids.UUID) []string {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	rows, err := owner.Query(context.Background(),
		`SELECT action FROM audit_log WHERE entity_type = 'channel_connection' AND entity_id = $1
		  ORDER BY occurred_at, id`, id)
	if err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scanning audit_log: %v", err)
		}
		out = append(out, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading audit_log: %v", err)
	}
	return out
}

// seedActivity writes one captured activity for this workspace, so a test can
// prove disconnect keeps history rather than erasing it.
func (f *channelFixture) seedActivity(t *testing.T) ids.UUID {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	id := ids.NewV7()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO activity (id, workspace_id, kind, subject, body, direction, source_system, source_id, source, captured_by)
		VALUES ($1, $2, 'note', 'telegram message', 'hello', 'inbound', 'telegram', $3, 'telegram:seed', 'connector:telegram')`,
		id, f.ws, id.String()); err != nil {
		t.Fatalf("seeding activity: %v", err)
	}
	return id
}

// activityExists reports whether a seeded activity survived.
func (f *channelFixture) activityExists(t *testing.T, id ids.UUID) bool {
	t.Helper()
	owner, _ := setupCaptureDB(t)
	var exists bool
	if err := owner.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM activity WHERE id = $1 AND archived_at IS NULL)`, id).Scan(&exists); err != nil {
		t.Fatalf("reading activity: %v", err)
	}
	return exists
}

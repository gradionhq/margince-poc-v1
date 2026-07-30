// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The channel-connect suite (telegram-oa design §5, §9.2). Every test here is
// about ORDER or about what a failure leaves behind, which is why it runs on a
// real database: the invariants are "nothing was written", "a pending row
// survives", "the second workspace loses the global index" — none of which a
// mocked store could be wrong about.

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
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// unknownToken is never registered with the fake, so Telegram rejects it. Its
// shape is valid on purpose: the point is to exercise the provider's refusal,
// not the local shape check that would short-circuit it.
const unknownToken = "8109999999:AAH-a-token-telegram-does-not-know"

// A refused token must leave the system exactly as it found it: no row for an
// operator to wonder about, and — the half that is easy to miss — nothing sealed
// in the vault. getMe runs first precisely so that both stay true.
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
	if len(f.api.getWebhookInfoTokens) != 0 {
		t.Errorf("the preflight ran %d time(s) on a token getMe already rejected — the order is getMe first", len(f.api.getWebhookInfoTokens))
	}
	if len(f.api.setWebhookCalls) != 0 {
		t.Error("a refused connect registered a webhook")
	}
}

// The transport turns a rejected token into a 400 naming the token, and carries
// none of the provider's own text: the client learns what to fix, not what
// Telegram said.
func TestConnectRejectsAnInvalidTokenWith400(t *testing.T) {
	f := newChannelFixture(t, nil)

	rec, req := f.request(http.MethodPost, "/v1/channel-connections",
		`{"provider":"telegram","botToken":"`+unknownToken+`"}`)
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

// Telegram allows exactly ONE webhook per bot, so a bot already delivering to
// another installation cannot be connected here without silently stealing that
// installation's traffic. No local constraint can see this — only the preflight
// can — and the refusal must not name the other installation's URL.
func TestConnectRefusesABotWhoseWebhookPointsElsewhere(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("elsewhere_bot")
	api.pointWebhookElsewhere(token)
	f := newChannelFixture(t, api)

	_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if !errors.Is(err, capture.ErrChannelWebhookOwnedElsewhere) {
		t.Fatalf("Connect on a bot registered elsewhere: got %v, want ErrChannelWebhookOwnedElsewhere", err)
	}
	if n := f.rowCount(t); n != 0 {
		t.Errorf("a conflicting connect wrote %d row(s); the preflight runs before anything is written", n)
	}
	if puts := f.vault.putCount(); puts != 0 {
		t.Errorf("a conflicting connect sealed %d secret(s)", puts)
	}

	rec, req := f.request(http.MethodPost, "/v1/channel-connections",
		`{"provider":"telegram","botToken":"`+token+`"}`)
	f.handlers.ConnectChannel(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "staging.internal.example") {
		t.Errorf("the 409 leaked the other installation's host: %s", rec.Body.String())
	}
}

// setWebhook failing is the case the whole ordering exists for: the row is
// written first, so the failure leaves a `pending` connection an operator can
// see and retry against the id already in the registered URL — not a silent
// divergence between us and Telegram.
func TestConnectLeavesAPendingRowWhenSetWebhookFails(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("pending_bot")
	api.setWebhookErr = telegram.ErrUnreachable
	f := newChannelFixture(t, api)

	_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if !errors.Is(err, telegram.ErrUnreachable) {
		t.Fatalf("Connect with setWebhook failing: got %v, want ErrUnreachable", err)
	}

	// The row must exist and read pending — never connected, and never absent.
	conns := f.liveConnections(t)
	if len(conns) != 1 {
		t.Fatalf("found %d connection(s); a failed setWebhook must leave exactly the one pending row", len(conns))
	}
	if conns[0].Status != "pending" {
		t.Errorf("status %q, want pending — a half-registration that reads connected is indistinguishable from a healthy quiet channel", conns[0].Status)
	}
	status, archived, _ := f.rowState(t, conns[0].ID)
	if status != "pending" || archived {
		t.Errorf("row state status=%q archived=%v, want pending and live", status, archived)
	}
	// The URL Telegram was asked to register carries this row's id — which is
	// exactly why the row had to exist first.
	if len(f.api.setWebhookCalls) != 1 {
		t.Fatalf("setWebhook called %d time(s), want once", len(f.api.setWebhookCalls))
	}
	wantURL := channelWebhookBase + "/webhooks/telegram/" + conns[0].ID.String()
	if got := f.api.setWebhookCalls[0].url; got != wantURL {
		t.Errorf("registered URL %q, want %q", got, wantURL)
	}
	if secret := f.api.setWebhookCalls[0].secret; secret == "" {
		t.Error("setWebhook was called with an empty secret — the ingress path would have nothing to authenticate")
	}
}

// The bot-scoped unique index is deliberately GLOBAL, not per-workspace: one
// bot has one webhook, so a second workspace connecting the same bot would
// silently redirect every delivery away from the first, which would go on
// reading `connected` and simply fall quiet.
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
	if n := second.rowCount(t); n != 0 {
		t.Errorf("the losing workspace kept %d row(s)", n)
	}
	// The loser sealed two secrets on its way to the insert; both must be
	// destroyed, because a lost race is the one failure that proves no row
	// persisted to name them.
	sealed := second.vault.mintedRefs()
	if len(sealed) != 2 {
		t.Fatalf("the losing connect sealed %d secret(s), want the token and the webhook secret", len(sealed))
	}
	for _, ref := range sealed {
		if _, err := second.vault.Get(second.ctx, second.workspaceKey(), ref); err == nil {
			t.Error("the losing connect left a sealed secret behind, referenced by no row and collected by no sweep")
		}
	}
	// And the winner is untouched: it still holds the registration.
	if got := api.registeredWebhook(token); !strings.HasPrefix(got, channelWebhookBase) {
		t.Errorf("the first workspace's registration was disturbed: %q", got)
	}
}

// A token rotation must re-run the preflight (a replacement bot can be wired
// elsewhere just as a first one can) and must pass back through `pending` on
// its way to `connected`, so the row never claims to be live while its
// registration is mid-flight.
func TestReplaceTokenRerunsThePreflightAndReturnsToConnected(t *testing.T) {
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
	oldCredential, oldSecret := f.vaultRefs(t, conn.ID)

	t.Run("a replacement bot registered elsewhere is refused", func(t *testing.T) {
		api.pointWebhookElsewhere(replacementToken)
		defer api.clearWebhook(replacementToken)

		if err := f.store.ReplaceToken(f.ctx, conn.ID, replacementToken); !errors.Is(err, capture.ErrChannelWebhookOwnedElsewhere) {
			t.Fatalf("ReplaceToken onto a bot registered elsewhere: got %v, want ErrChannelWebhookOwnedElsewhere", err)
		}
		status, _, channelID := f.rowState(t, conn.ID)
		if status != "connected" || channelID != strconv.FormatInt(firstID, 10) {
			t.Errorf("the refused rotation moved the row: status=%q channel_id=%q, want connected on the original bot", status, channelID)
		}
	})

	preflightsBefore := len(api.getWebhookInfoTokens)
	if err := f.store.ReplaceToken(f.ctx, conn.ID, replacementToken); err != nil {
		t.Fatalf("ReplaceToken: %v", err)
	}

	if got := api.getWebhookInfoTokens[len(api.getWebhookInfoTokens)-1]; got != replacementToken {
		t.Errorf("the last preflight ran against %q, want the replacement token", got)
	}
	if len(api.getWebhookInfoTokens) <= preflightsBefore {
		t.Error("ReplaceToken did not re-run the preflight")
	}

	status, archived, channelID := f.rowState(t, conn.ID)
	if status != "connected" {
		t.Errorf("status %q after a successful rotation, want connected", status)
	}
	if archived {
		t.Error("the rotation archived the connection — history and identity bindings hang off this row surviving")
	}
	if channelID != strconv.FormatInt(replacementID, 10) {
		t.Errorf("channel_id %q, want the replacement bot's id %d", channelID, replacementID)
	}
	// The audit trail records the pass through pending as its own update, so
	// the sequence is legible: create(pending) → connected → pending → connected.
	if actions := f.auditActions(t, conn.ID); len(actions) != 4 || actions[0] != "create" {
		t.Errorf("audit actions %v, want create followed by three updates", actions)
	}
	// The superseded pair is unreachable from any row and must be gone.
	for name, ref := range map[string]keyvault.Ref{"bot token": oldCredential, "webhook secret": oldSecret} {
		if _, err := f.vault.Get(f.ctx, f.workspaceKey(), ref); err == nil {
			t.Errorf("the superseded %s survived the rotation", name)
		}
	}
	if len(api.setWebhookCalls) < 2 || api.setWebhookCalls[len(api.setWebhookCalls)-1].token != replacementToken {
		t.Error("the replacement token was never registered with the provider")
	}
}

// Swapping in a DIFFERENT bot has to end the outgoing bot's registration, because
// nothing else ever will. Left standing, the old bot keeps delivering to this
// installation's URL carrying a secret the row no longer holds: every one of those
// messages is refused and silently uncaptured, and it is invisible because the
// connection reads `connected` for the new bot.
func TestReplacingWithADifferentBotRevokesTheOutgoingBotsWebhook(t *testing.T) {
	api := newFakeTelegram()
	outgoingToken, _ := api.withNewBot("outgoing_bot")
	incomingToken, _ := api.withNewBot("incoming_bot")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: outgoingToken,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := api.registeredWebhook(outgoingToken); got == "" {
		t.Fatal("the fixture never registered the outgoing bot, so this test could not observe a revocation")
	}

	if err := f.store.ReplaceToken(f.ctx, conn.ID, incomingToken); err != nil {
		t.Fatalf("ReplaceToken: %v", err)
	}

	if got := api.registeredWebhook(outgoingToken); got != "" {
		t.Errorf("the outgoing bot still delivers to %q; its messages would be refused for a secret this connection no longer holds", got)
	}
	if !slices.Contains(api.deleteWebhookTokens, outgoingToken) {
		t.Error("deleteWebhook was never called for the outgoing bot")
	}
	if got := api.registeredWebhook(incomingToken); got != channelWebhookBase+"/webhooks/telegram/"+conn.ID.String() {
		t.Errorf("the incoming bot is registered at %q, want this connection's URL — revoking the outgoing bot must not disturb it", got)
	}
}

// A rotation of the SAME bot must delete nothing. Telegram keeps one webhook per
// bot, so setWebhook replaces the registration in place; deleting it first would
// take a working channel down for the length of a round trip, and leave it down
// altogether if the re-registration failed.
func TestRotatingTheSameBotsTokenRevokesNothing(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("rotating_bot")
	f := newChannelFixture(t, api)

	conn, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rotated := api.rotateToken(token)

	if err := f.store.ReplaceToken(f.ctx, conn.ID, rotated); err != nil {
		t.Fatalf("ReplaceToken: %v", err)
	}

	if len(api.deleteWebhookTokens) != 0 {
		t.Errorf("a same-bot rotation called deleteWebhook %v — it would drop the registration it is about to replace", api.deleteWebhookTokens)
	}
	if got := api.registeredWebhook(rotated); got != channelWebhookBase+"/webhooks/telegram/"+conn.ID.String() {
		t.Errorf("the rotated token is registered at %q, want this connection's URL", got)
	}
	status, _, _ := f.rowState(t, conn.ID)
	if status != "connected" {
		t.Errorf("status %q after a rotation, want connected", status)
	}
}

// The race the version predicate exists for. Replacement A repoints the row and
// then blocks at the provider; replacement B repoints the SAME row onto its own
// bot and its own setWebhook fails, leaving the row `pending` for a bot Telegram
// was never told about. A's registration then succeeds and A goes to flip the row
// live — updating by id alone, it would advertise bot B as connected on the
// strength of a registration made for bot A.
func TestAStaleReplacementCannotMarkAnUnregisteredBotConnected(t *testing.T) {
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

	registrationRefused := errors.New("telegram refused bot B's registration")
	api.failSetWebhookFor(tokenB, registrationRefused)
	var errB error
	// Fires while replacement A is inside setWebhook — after A's repoint has
	// committed, which is the only window in which A's snapshot can go stale.
	api.onNextSetWebhook(func(string) {
		errB = f.store.ReplaceToken(f.ctx, conn.ID, tokenB)
	})

	errA := f.store.ReplaceToken(f.ctx, conn.ID, tokenA)

	if !errors.Is(errB, registrationRefused) {
		t.Fatalf("the second replacement = %v, want the provider refusal that leaves its row pending", errB)
	}
	if !errors.Is(errA, apperrors.ErrVersionSkew) {
		t.Fatalf("the stale replacement = %v, want ErrVersionSkew — its snapshot no longer describes the row", errA)
	}

	status, _, channelID := f.rowState(t, conn.ID)
	if channelID != strconv.FormatInt(botBID, 10) {
		t.Fatalf("channel_id %q, want bot B's id %d — the newer replacement owns this connection", channelID, botBID)
	}
	if status != "pending" {
		t.Errorf("status %q: the row advertises bot B although Telegram refused its registration", status)
	}
	if got := api.registeredWebhook(tokenB); got != "" {
		t.Errorf("bot B is registered at %q, but its setWebhook was refused", got)
	}
	// Bot A's own registration is beside the point: the row does not name bot A,
	// so ingress verifies its deliveries against a secret it does not carry.
	if channelID == strconv.FormatInt(botAID, 10) {
		t.Error("the stale replacement overwrote the newer one's bot")
	}
}

// Disconnecting stops capture; it does not erase. The webhook is revoked, both
// sealed secrets are destroyed, the row is archived as disconnected — and every
// activity already captured through the channel is still there.
func TestDisconnectRevokesTheWebhookAndKeepsActivities(t *testing.T) {
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
	credentialRef, secretRef := f.vaultRefs(t, conn.ID)

	if err := f.store.Disconnect(f.ctx, conn.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if len(api.deleteWebhookTokens) != 1 || api.deleteWebhookTokens[0] != token {
		t.Errorf("deleteWebhook was called %d time(s), want exactly once with the connected bot's token", len(api.deleteWebhookTokens))
	}
	if still := api.registeredWebhook(token); still != "" {
		t.Errorf("Telegram still holds a webhook for a disconnected channel: %q", still)
	}
	status, archived, _ := f.rowState(t, conn.ID)
	if status != "disconnected" || !archived {
		t.Errorf("row state status=%q archived=%v, want disconnected and archived (archival is what frees the unique indexes for a reconnect)", status, archived)
	}
	for name, ref := range map[string]keyvault.Ref{"bot token": credentialRef, "webhook secret": secretRef} {
		if _, err := f.vault.Get(f.ctx, f.workspaceKey(), ref); err == nil {
			t.Errorf("the %s survived disconnect — a live credential outlives the operator's withdrawal", name)
		}
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

// A deployment that does not know its own public address must refuse to connect
// rather than derive one from the request, and must say which knob to set. This
// is the failure the whole path is shaped around: a bot registered against an
// unreachable URL reads `connected` and then falls silent.
func TestConnectRefusesWhenTheInstallationHasNoPublicAddress(t *testing.T) {
	api := newFakeTelegram()
	token, _ := api.withNewBot("homeless_bot")
	f := newChannelFixtureWithoutPublicAddress(t, api)

	_, err := f.store.Connect(f.ctx, capture.ConnectRequest{
		Provider: capture.ProviderTelegram, BotToken: token,
	})
	if !errors.Is(err, capture.ErrChannelWebhookBaseUnset) {
		t.Fatalf("Connect with no public address: got %v, want ErrChannelWebhookBaseUnset", err)
	}
	if !strings.Contains(err.Error(), "--public-base-url") {
		t.Errorf("the refusal does not name the knob to set: %v", err)
	}
	if len(api.getMeTokens) != 0 {
		t.Error("the token was spent on a provider call the deployment could never complete")
	}
	if n := f.rowCount(t); n != 0 {
		t.Errorf("the refused connect wrote %d row(s)", n)
	}

	rec, req := f.request(http.MethodPost, "/v1/channel-connections",
		`{"provider":"telegram","botToken":"`+token+`"}`)
	f.handlers.ConnectChannel(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "channel_public_base_url_unset") {
		t.Errorf("the 503 does not carry the actionable code: %s", rec.Body.String())
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package telegram

// The client's own obligations, against a local test server — never the real
// host. What is under test is the sentinel a caller gets, because the connect
// ordering branches on exactly that: a token to fix, a provider to wait for, or
// a refusal to read.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recorder is a Bot API stand-in: it answers every method with one canned
// status and body, and records what was asked of it. The mutex is not
// decoration — httptest serves each request on its own goroutine, so the test
// body reading these fields races the handler writing them without it.
type recorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func (rec *recorder) record(path, body string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.paths = append(rec.paths, path)
	rec.bodies = append(rec.bodies, body)
}

// lastPath and lastBody report the most recent request, failing the test when
// nothing reached the server at all.
func (rec *recorder) lastPath(t *testing.T) string {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.paths) == 0 {
		t.Fatal("no request reached the provider stand-in")
	}
	return rec.paths[len(rec.paths)-1]
}

func (rec *recorder) lastBody(t *testing.T) string {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) == 0 {
		t.Fatal("no request reached the provider stand-in")
	}
	return rec.bodies[len(rec.bodies)-1]
}

// serve stands up the stand-in and returns a client pointed at it.
func serve(t *testing.T, status int, body string) (API, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
			return
		}
		rec.record(r.URL.Path, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing the fixture response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return NewAPI(srv.Client(), srv.URL), rec
}

func TestGetMeReportsTheBotBehindTheToken(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":{"id":424242,"username":"acme_crm_bot"}}`)

	bot, err := api.GetMe(context.Background(), "424242:secret")
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if bot.ID != 424242 || bot.Username != "acme_crm_bot" {
		t.Errorf("bot %+v, want id 424242 / acme_crm_bot", bot)
	}
	// The token rides the path — Telegram's scheme — so a caller can confirm
	// the request was addressed to the bot it named.
	if got := rec.lastPath(t); !strings.HasPrefix(got, "/bot424242:secret/") {
		t.Errorf("request path %q does not carry the token", got)
	}
}

// A 2xx getMe with no bot id is not something a connection can be keyed on, so
// it must not read as success — the row would end up keyed on "0".
func TestGetMeRefusesAResultWithoutABotID(t *testing.T) {
	api, _ := serve(t, http.StatusOK, `{"ok":true,"result":{"username":"nameless"}}`)

	if _, err := api.GetMe(context.Background(), "1:x"); !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("GetMe on an id-less result: got %v, want ErrRequestRejected", err)
	}
}

// The status verdict is what the connect path branches on, so each class has to
// land on the sentinel whose remedy actually matches it.
func TestEveryFailureClassLandsOnItsOwnSentinel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized token", http.StatusUnauthorized, `{"ok":false,"description":"Unauthorized"}`, ErrTokenRejected},
		{"forbidden token", http.StatusForbidden, `{"ok":false,"description":"Forbidden"}`, ErrTokenRejected},
		// 404 is a token failure, not a server fault: the token is part of the
		// path, so a token naming no bot cannot be routed.
		{"token names no bot", http.StatusNotFound, `{"ok":false,"description":"Not Found"}`, ErrTokenRejected},
		{"provider outage", http.StatusBadGateway, `{"ok":false,"description":"Bad Gateway"}`, ErrUnreachable},
		{"bad request", http.StatusBadRequest, `{"ok":false,"description":"Bad Request: bad webhook"}`, ErrRequestRejected},
		{"rate limited", http.StatusTooManyRequests, `{"ok":false,"description":"Too Many Requests"}`, ErrRequestRejected},
		{"ok=false under a 200", http.StatusOK, `{"ok":false,"description":"refused"}`, ErrRequestRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := serve(t, tc.status, tc.body)
			err := api.SetWebhook(context.Background(), "1:x", "https://crm.test/webhooks/telegram/x", "s", nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// An unreachable host is a reachability failure, not a decoding one: the
// distinction is what tells an operator to check the provider rather than this
// code.
func TestAnUnreachableHostIsReportedAsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := srv.Client()
	url := srv.URL
	srv.Close()

	api := NewAPI(client, url)
	if _, err := api.GetWebhookInfo(context.Background(), "1:x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("GetWebhookInfo against a closed host: got %v, want ErrUnreachable", err)
	}
}

// The response read is bounded so a compromised or misconfigured host cannot
// exhaust memory. Past the cap the body is truncated, which fails the decode —
// reported as a reachability failure, never as success on a partial document.
func TestAnOversizedResponseIsRefusedRatherThanRead(t *testing.T) {
	oversized := `{"ok":true,"result":{"url":"` + strings.Repeat("a", maxResponseBytes+1) + `"}}`
	api, _ := serve(t, http.StatusOK, oversized)

	if _, err := api.GetWebhookInfo(context.Background(), "1:x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("GetWebhookInfo on an oversized body: got %v, want ErrUnreachable", err)
	}
}

func TestSetWebhookSendsTheSecretAndTheAllowedUpdates(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":true}`)

	if err := api.SetWebhook(context.Background(), "1:x",
		"https://crm.test/webhooks/telegram/abc", "the-secret", []string{"message", "my_chat_member"}); err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	body := rec.lastBody(t)
	for _, want := range []string{"https://crm.test/webhooks/telegram/abc", "the-secret", "my_chat_member"} {
		if !strings.Contains(body, want) {
			t.Errorf("the setWebhook request omitted %q: %s", want, body)
		}
	}
}

// Threading is carried by the parent message id, so a send that cannot report
// its own id has nothing a later reply could thread under and must not be
// reported as delivered.
//
// It is a REACHABILITY failure and not a refusal, which is the load-bearing
// half: ok=true means Telegram accepted the message, so it may be on its way,
// and the send path reads this class as an outcome it can never learn. Reported
// as a refusal it would look like a message that did not go, and the retry that
// followed would deliver a second copy.
func TestSendMessageRefusesAResultWithoutAMessageID(t *testing.T) {
	api, _ := serve(t, http.StatusOK, `{"ok":true,"result":{}}`)

	_, err := api.SendMessage(context.Background(), "1:x", OutboundChannelMessage{ChatID: 7, Text: "hi"})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("SendMessage on an id-less result: got %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrRequestRejected) {
		t.Fatalf("got %v, which also reads as a refusal — a retry on that class would send the message twice", err)
	}
}

func TestSendMessageReturnsTheProviderMessageID(t *testing.T) {
	api, _ := serve(t, http.StatusOK, `{"ok":true,"result":{"message_id":9911}}`)

	id, err := api.SendMessage(context.Background(), "1:x", OutboundChannelMessage{ChatID: 7, Text: "hi", ReplyToMessageID: 12})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id != 9911 {
		t.Errorf("message id %d, want 9911", id)
	}
}

// A provider description explains a refusal to an operator reading logs; it
// must never be mistaken for something safe to show a client, so it stays in
// the error the platform mapper logs and nowhere else.
func TestTheProviderDescriptionRidesTheErrorForTheServerLog(t *testing.T) {
	api, _ := serve(t, http.StatusBadRequest, `{"ok":false,"description":"Bad Request: bad webhook: HTTPS url must be provided"}`)

	err := api.SetWebhook(context.Background(), "1:x", "http://insecure.test/hook", "s", nil)
	if err == nil {
		t.Fatal("SetWebhook accepted a request Telegram refused")
	}
	if !strings.Contains(err.Error(), "HTTPS url must be provided") {
		t.Errorf("the error dropped the provider's reason, leaving nothing to diagnose from: %v", err)
	}
}

func TestValidateTokenRefusesWhatCannotBeABotToken(t *testing.T) {
	for name, token := range map[string]string{
		"empty":            "",
		"no colon":         "acme_crm_bot",
		"no bot id":        ":secret",
		"no secret":        "424242:",
		"non-numeric id":   "acme:secret",
		"a pasted webhook": "https://crm.test/webhooks/telegram/abc",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateToken(token); !errors.Is(err, ErrTokenRejected) {
				t.Fatalf("ValidateToken(%q): got %v, want ErrTokenRejected", token, err)
			}
		})
	}
	if err := ValidateToken("  424242:AAH-a-real-looking-secret  "); err != nil {
		t.Errorf("ValidateToken refused a well-formed token (surrounding whitespace is a paste artefact, not an error): %v", err)
	}
}

// The webhook secret authenticates every inbound delivery, so two mints must
// never collide and the encoding must be one Telegram accepts in secret_token.
func TestMintWebhookSecretIsUnguessableAndWireSafe(t *testing.T) {
	const mints = 64
	seen := make(map[string]struct{}, mints)
	for i := range mints {
		secret, err := MintWebhookSecret()
		if err != nil {
			t.Fatalf("MintWebhookSecret: %v", err)
		}
		if _, dup := seen[secret]; dup {
			t.Fatalf("mint %d repeated a secret — the ingress credential is predictable", i)
		}
		seen[secret] = struct{}{}
		if len(secret) < 32 || len(secret) > 256 {
			t.Fatalf("secret length %d is outside Telegram's 1..256 bound (and below a credible entropy floor)", len(secret))
		}
		if bad := strings.TrimLeft(secret, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"); bad != "" {
			t.Fatalf("secret %q carries characters Telegram does not accept in secret_token", secret)
		}
	}
	// Guard the constant itself: 32 bytes is what makes the secret unguessable.
	if webhookSecretBytes < 32 {
		t.Fatalf("webhookSecretBytes is %d — below the 256-bit floor an authentication credential needs", webhookSecretBytes)
	}
}

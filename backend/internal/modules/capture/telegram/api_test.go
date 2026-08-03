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
	"time"
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

// The status verdict is what every caller branches on, so each class has to land
// on the sentinel whose remedy actually matches it. The bodies are Telegram's
// REAL ones, verbatim: a fixture that answers a bare "Forbidden" exercises a
// response the Bot API does not send, and the class that mattered would go
// untested behind a row that looks like it covers it.
func TestEveryFailureClassLandsOnItsOwnSentinel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
		// never is a sentinel this class must NOT also answer, for the pairs whose
		// remedies point an operator in opposite directions.
		never error
	}{
		{"unauthorized token", http.StatusUnauthorized, `{"ok":false,"description":"Unauthorized"}`, ErrTokenRejected, ErrRecipientUnreachable},
		// The commonest send failure a channel has, and the one this split exists
		// for: a customer blocked the bot. The token is live — reported as a
		// credential fault it would send an operator to rotate a working token
		// while the customer stays unreachable regardless.
		{
			"the customer blocked the bot", http.StatusForbidden,
			`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`,
			ErrRecipientUnreachable, ErrTokenRejected,
		},
		{
			"the customer's account is gone", http.StatusForbidden,
			`{"ok":false,"error_code":403,"description":"Forbidden: user is deactivated"}`,
			ErrRecipientUnreachable, ErrTokenRejected,
		},
		// 404 is a token failure, not a server fault: the token is part of the
		// path, so a token naming no bot cannot be routed.
		{"token names no bot", http.StatusNotFound, `{"ok":false,"description":"Not Found"}`, ErrTokenRejected, ErrRecipientUnreachable},
		{"provider outage", http.StatusBadGateway, `{"ok":false,"description":"Bad Gateway"}`, ErrUnreachable, nil},
		{"bad request", http.StatusBadRequest, `{"ok":false,"description":"Bad Request: invalid offset"}`, ErrRequestRejected, nil},
		{"rate limited", http.StatusTooManyRequests, `{"ok":false,"description":"Too Many Requests"}`, ErrRequestRejected, nil},
		{"ok=false under a 200", http.StatusOK, `{"ok":false,"description":"refused"}`, ErrRequestRejected, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := serve(t, tc.status, tc.body)
			_, _, err := api.GetUpdates(context.Background(), "1:x", 0, 25, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.never != nil && errors.Is(err, tc.never) {
				t.Fatalf("got %v, which ALSO reads as %v — the two remedies contradict each other", err, tc.never)
			}
		})
	}
}

// A 403 keeps answering the connect transport's question as well as the send
// path's. The two readers want different things from it — "which recipient" and
// "was my request refused" — and the connect surface has no recipient to speak
// of, so it must go on classifying a 403 as a refusal rather than fall through to
// an unmapped fault.
func TestAForbiddenAlsoReadsAsARefusalForTheConnectTransport(t *testing.T) {
	api, _ := serve(t, http.StatusForbidden,
		`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)

	_, _, err := api.GetUpdates(context.Background(), "1:x", 0, 25, nil)
	if !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("got %v, want a 403 to also answer ErrRequestRejected", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Errorf("got %v, which reads as an outage — a 403 is a definite answer FROM Telegram", err)
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
	if err := api.DeleteWebhook(context.Background(), "1:x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("DeleteWebhook against a closed host: got %v, want ErrUnreachable", err)
	}
}

// The response read is bounded so a compromised or misconfigured host cannot
// exhaust memory. Past the cap the body is truncated, which fails the decode —
// reported as a reachability failure, never as success on a partial document.
func TestAnOversizedResponseIsRefusedRatherThanRead(t *testing.T) {
	oversized := `{"ok":true,"result":{"username":"` + strings.Repeat("a", maxResponseBytes+1) + `"}}`
	api, _ := serve(t, http.StatusOK, oversized)

	if _, err := api.GetMe(context.Background(), "1:x"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("GetMe on an oversized body: got %v, want ErrUnreachable", err)
	}
}

// deleteWebhook must not ask Telegram to drop the updates it is holding: those
// are the customer's messages, and the first poll after a connect is what collects
// them. The default for drop_pending_updates is false, so the guarantee is that
// the parameter is absent rather than set.
func TestDeleteWebhookKeepsThePendingUpdates(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":true}`)

	if err := api.DeleteWebhook(context.Background(), "1:x"); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if body := rec.lastBody(t); strings.Contains(body, "drop_pending_updates") {
		t.Errorf("the deleteWebhook request names drop_pending_updates (%s) — messages waiting for this bot would be discarded", body)
	}
	if got := rec.lastPath(t); !strings.HasSuffix(got, "/deleteWebhook") {
		t.Errorf("request path %q did not reach deleteWebhook", got)
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
	api, _ := serve(t, http.StatusBadRequest, `{"ok":false,"description":"Bad Request: terminated by other getUpdates request"}`)

	err := api.DeleteWebhook(context.Background(), "1:x")
	if err == nil {
		t.Fatal("DeleteWebhook accepted a request Telegram refused")
	}
	if !strings.Contains(err.Error(), "terminated by other getUpdates request") {
		t.Errorf("the error dropped the provider's reason, leaving nothing to diagnose from: %v", err)
	}
}

// The poller reads raw envelopes because both passes downstream of it —
// InScopeSubjects and Normalize — consume raw bytes, and the highest update_id
// is what the next poll acknowledges the batch with.
func TestGetUpdatesReturnsRawEnvelopesAndTheHighestUpdateID(t *testing.T) {
	api, rec := serve(t, http.StatusOK, `{"ok":true,"result":[
		{"update_id":7,"message":{"message_id":1,"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"one"}},
		{"update_id":9,"message":{"message_id":2,"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"two"}}]}`)

	batch, highest, err := api.GetUpdates(context.Background(), "1:x", 4, 25, []string{"message"})
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("got %d envelopes, want 2", len(batch))
	}
	if !strings.Contains(string(batch[0]), `"text":"one"`) {
		t.Errorf("the first envelope is not the raw update Telegram sent: %s", batch[0])
	}
	if highest != 9 {
		t.Errorf("highest update_id %d, want 9", highest)
	}
	body := rec.lastBody(t)
	for _, want := range []string{`"offset":4`, `"timeout":25`, `"message"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the getUpdates request omitted %q: %s", want, body)
		}
	}
}

// An empty batch is the ordinary outcome of a long poll that timed out with
// nothing to report, and it must leave the offset exactly where it was: 0 is
// not "start over", it is "nothing arrived".
func TestGetUpdatesReportsNoHighestIDForAnEmptyBatch(t *testing.T) {
	api, _ := serve(t, http.StatusOK, `{"ok":true,"result":[]}`)

	batch, highest, err := api.GetUpdates(context.Background(), "1:x", 4, 25, nil)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(batch) != 0 || highest != 0 {
		t.Fatalf("got %d envelopes / highest %d, want 0 / 0", len(batch), highest)
	}
}

// getUpdates and setWebhook are mutually exclusive per bot, so this 409 is a
// configuration fact the caller can act on — clear the registration and poll
// again — and it has to be tellable apart from every other refusal.
func TestGetUpdatesSurfacesAConflictWhenAWebhookIsActive(t *testing.T) {
	api, _ := serve(t, http.StatusConflict,
		`{"ok":false,"error_code":409,"description":"Conflict: can't use getUpdates method while webhook is active"}`)

	_, _, err := api.GetUpdates(context.Background(), "1:x", 0, 25, nil)
	if !errors.Is(err, ErrWebhookActive) {
		t.Fatalf("GetUpdates against a webhook-registered bot: got %v, want ErrWebhookActive", err)
	}
	if errors.Is(err, ErrTokenRejected) {
		t.Errorf("got %v, which reads as a credential fault — it would send an operator to rotate a working token", err)
	}
}

// A long poll asks Telegram to HOLD the connection, so the request budget must
// outlast the poll. The shared client timeout bounds an ordinary call and would
// otherwise truncate the poll before Telegram ever answered — and a poll that
// never completes never advances its offset, so the connection would retry
// forever without progress.
func TestALongPollOutlastsTheSharedRequestTimeout(t *testing.T) {
	shared := &http.Client{Timeout: httpTimeout}
	poll := &httpAPI{client: shared, base: "https://example.invalid"}

	budget := LongPollBudget(25)
	if budget <= 25*time.Second {
		t.Fatalf("a 25s poll gets a %s budget — Telegram answers AT the interval, so the answer needs room to travel", budget)
	}
	widened := poll.longPollClient(budget)
	if widened.Timeout < budget {
		t.Fatalf("a %s long poll runs under a %s client timeout — Telegram's answer would be cut off", budget, widened.Timeout)
	}
	if shared.Timeout != httpTimeout {
		t.Errorf("the shared client's timeout moved to %s; every other Bot API call now waits longer", shared.Timeout)
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

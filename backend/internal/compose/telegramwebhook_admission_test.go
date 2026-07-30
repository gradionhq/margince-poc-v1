// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The two cheap brakes on the ONE unauthenticated edge that spends database
// work to decide admission: a request with no secret header is answered before
// the pool is touched at all, and a flood is refused before the chassis reads
// anything. Both are provable without a database — indeed the first is proved
// BY the absence of one.

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
)

// quietLogger is a logger whose output nothing reads — these cases assert on
// status codes and on what the handler under test did, never on log lines.
func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// telegramSecretRequest builds one delivery to a syntactically valid
// connection id, optionally carrying a secret header.
func telegramSecretRequest(connectionID, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/telegram/"+connectionID, strings.NewReader("{}"))
	r.SetPathValue("connection_id", connectionID)
	if secret != "" {
		r.Header.Set(telegramSecretHeader, secret)
	}
	return r
}

// A delivery with no secret header must be refused WITHOUT resolving the
// connection — that resolve is a pool query plus a probe under every live
// workspace's GUC, spent on a request that cannot be admitted whatever it
// finds. The proof is the nil pool: a secret function that reached the
// database here would panic instead of answering.
//
// setWebhook always registers a secret, so no genuine delivery takes this
// branch.
func TestTheSecretFunctionAnswersAnAbsentHeaderWithoutTouchingTheDatabase(t *testing.T) {
	secret := telegramSecretFunc(nil, nil, quietLogger())

	want, got := secret(telegramSecretRequest("0198f0a0-0000-7000-8000-000000000001", ""))
	if got != "" {
		t.Errorf("got = %q, want the empty header value back verbatim", got)
	}
	if want == "" || want == got {
		t.Errorf("want = %q — a headerless delivery must compare against an unmatchable value, never against itself", want)
	}
}

// countingHandler records how many requests got past the throttle. A throttle
// that refused nothing and one that refused everything both look like "the
// status was right" without this.
type countingHandler struct{ served int }

func (h *countingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.served++
	w.WriteHeader(http.StatusOK)
}

// The per-connection budget refuses the over-budget delivery before the
// chassis sees it, and answers 429 — not a 2xx, because a throttled delivery
// was never inspected: Telegram has no history API, so it must be asked to
// redeliver rather than told the update was handled.
func TestTheWebhookThrottleRefusesAnOverBudgetConnectionBeforeTheHandler(t *testing.T) {
	inner := &countingHandler{}
	limits := telegramWebhookLimiters{
		perIP:         ratelimit.New(100, time.Minute),
		perConnection: ratelimit.New(2, time.Minute),
	}
	mux := http.NewServeMux()
	mux.Handle("/webhooks/telegram/{connection_id}", throttleTelegramWebhook(limits, inner))

	const connection = "0198f0a0-0000-7000-8000-000000000002"
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, telegramSecretRequest(connection, "s"))
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d → %d, want 200 — a within-budget delivery must reach the handler", attempt, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, telegramSecretRequest(connection, "s"))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("the over-budget delivery → %d, want 429", rec.Code)
	}
	if inner.served != 2 {
		t.Errorf("%d deliveries reached the handler, want 2 — the refused one still did the work the throttle exists to prevent", inner.served)
	}
}

// The budget is keyed on the connection in the path, so one connection under a
// flood cannot starve another's deliveries.
func TestTheWebhookThrottleBudgetsEachConnectionSeparately(t *testing.T) {
	inner := &countingHandler{}
	limits := telegramWebhookLimiters{
		perIP:         ratelimit.New(100, time.Minute),
		perConnection: ratelimit.New(1, time.Minute),
	}
	mux := http.NewServeMux()
	mux.Handle("/webhooks/telegram/{connection_id}", throttleTelegramWebhook(limits, inner))

	const flooded = "0198f0a0-0000-7000-8000-000000000003"
	const quiet = "0198f0a0-0000-7000-8000-000000000004"
	for range 2 {
		mux.ServeHTTP(httptest.NewRecorder(), telegramSecretRequest(flooded, "s"))
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, telegramSecretRequest(quiet, "s"))
	if rec.Code != http.StatusOK {
		t.Errorf("the second connection's first delivery → %d, want 200 — the budget is shared, not per connection", rec.Code)
	}
}

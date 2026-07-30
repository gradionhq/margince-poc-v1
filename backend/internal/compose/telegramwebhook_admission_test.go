// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The two cheap brakes on the ONE unauthenticated edge that spends database
// work to decide admission: a request with no secret header is answered before
// the pool is touched at all, and a caller who keeps failing admission is
// refused before the chassis reads anything. Both are provable without a
// database — indeed the first is proved BY the absence of one.
//
// What the throttle cases are really about is the other half of that brake:
// what it must NOT refuse. A refused delivery on this edge is unrecoverable —
// Telegram has no history API — so a budget an admitted delivery could spend
// would end with the brake denying exactly the traffic it exists to protect.

import (
	"bytes"
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

// admissionStub stands in for the chassis: it answers whatever admission
// outcome a case needs and counts how many requests actually reached it. A
// throttle that refused nothing and one that refused everything both look like
// "the status was right" without that count.
type admissionStub struct {
	status int
	served int
}

func (h *admissionStub) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.served++
	w.WriteHeader(h.status)
}

// throttledMux mounts the throttle under the real route pattern, so
// r.PathValue("connection_id") resolves exactly as it does in production.
func throttledMux(limits telegramWebhookLimiters, next http.Handler, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/webhooks/telegram/{connection_id}", throttleTelegramWebhook(limits, next, log))
	return mux
}

// fakeThrottleClock makes window expiry a value the test sets, where sleeping
// against a real window is a race.
type fakeThrottleClock struct{ now time.Time }

func (c *fakeThrottleClock) Now() time.Time { return c.now }

// A genuine delivery must never spend the anonymous budget. Behind an ingress
// proxy every caller — Telegram and attacker alike — resolves to the one peer
// address the proxy connects from, so a budget that counted admitted
// deliveries would have a busy bot throttle itself, and its own retries of the
// resulting 429 would keep the window full.
func TestAdmittedDeliveriesNeverSpendTheFailedAdmissionBudget(t *testing.T) {
	chassis := &admissionStub{status: http.StatusOK}
	limits := telegramWebhookLimiters{
		perIP:         ratelimit.New(2, time.Minute),
		perConnection: ratelimit.New(2, time.Minute),
	}
	mux := throttledMux(limits, chassis, quietLogger())

	const connection = "0198f0a0-0000-7000-8000-000000000002"
	const deliveries = 5 // well past both budgets
	for attempt := 1; attempt <= deliveries; attempt++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, telegramSecretRequest(connection, "s"))
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d → %d, want 200 — an admitted delivery is unrecoverable if refused", attempt, rec.Code)
		}
	}
	if chassis.served != deliveries {
		t.Errorf("%d of %d deliveries reached the chassis — the budget counted traffic that had cleared admission",
			chassis.served, deliveries)
	}
}

// The budget that does exist: a caller who fails admission spends it, and once
// it is spent the next request is refused before the chassis (and so before the
// secret function's pool work) sees it. 429, not a 4xx that ends the delivery —
// a throttled delivery was never inspected, so the update is not poison.
func TestFailedAdmissionsSpendTheBudgetAndRefuseTheNextRequest(t *testing.T) {
	chassis := &admissionStub{status: http.StatusUnauthorized}
	limits := telegramWebhookLimiters{
		perIP:         ratelimit.New(2, time.Minute),
		perConnection: ratelimit.New(100, time.Minute),
	}
	var logged bytes.Buffer
	mux := throttledMux(limits, chassis, slog.New(slog.NewTextHandler(&logged, nil)))

	const connection = "0198f0a0-0000-7000-8000-000000000003"
	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, telegramSecretRequest(connection, "wrong"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed admission %d → %d, want 401", attempt, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, telegramSecretRequest(connection, "wrong"))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("the over-budget request → %d, want 429", rec.Code)
	}
	if chassis.served != 2 {
		t.Errorf("%d requests reached the chassis, want 2 — the refused one still did the work the throttle exists to prevent",
			chassis.served)
	}
	// A channel gone dark under this brake must be able to tell itself apart
	// from a channel nobody is writing to.
	if !strings.Contains(logged.String(), "failed-admission budget") {
		t.Errorf("the refusal logged nothing: %q", logged.String())
	}
}

// A 429 is not itself a failed admission, and counting it as one would spread
// the refusal: Telegram treats any non-2xx as "try again later", so a delivery
// refused on the IP its ingress proxy shares with an attacker would go on to
// spend its OWN connection's budget with every retry, taking that channel down
// for reasons that never had anything to do with it.
//
// The two budgets are given different window lengths so the claim is
// observable: the shared IP window expires while the connection's is still
// running, and only a refusal that was counted against the connection can
// still be refusing it then.
func TestARefusedDeliveryDoesNotSpendItsOwnConnectionsBudget(t *testing.T) {
	chassis := &admissionStub{status: http.StatusUnauthorized}
	clock := &fakeThrottleClock{now: time.Unix(1_700_000_000, 0)}
	limits := telegramWebhookLimiters{
		perIP:         ratelimit.NewWithClock(1, time.Minute, clock.Now),
		perConnection: ratelimit.NewWithClock(1, time.Hour, clock.Now),
	}
	mux := throttledMux(limits, chassis, quietLogger())

	// The attacker, on their own invented connection id, spends the shared
	// per-IP budget.
	const flooded = "0198f0a0-0000-7000-8000-000000000004"
	const genuine = "0198f0a0-0000-7000-8000-000000000007"
	mux.ServeHTTP(httptest.NewRecorder(), telegramSecretRequest(flooded, "wrong"))

	// A genuine delivery now arrives from behind the same proxy and is refused
	// for someone else's failures. Its retries are refused the same way.
	chassis.status = http.StatusOK
	for range 3 {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, telegramSecretRequest(genuine, "s"))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("a delivery sharing the flooded peer → %d, want 429", rec.Code)
		}
	}

	clock.now = clock.now.Add(time.Minute + time.Second)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, telegramSecretRequest(genuine, "s"))
	if rec.Code != http.StatusOK {
		t.Errorf("the delivery after the shared window expired → %d, want 200 — its own refusals were counted against its connection", rec.Code)
	}
}

// The budget is keyed on the connection in the path, so one connection under a
// flood of failed admissions cannot starve another's deliveries.
func TestTheWebhookThrottleBudgetsEachConnectionSeparately(t *testing.T) {
	chassis := &admissionStub{status: http.StatusUnauthorized}
	limits := telegramWebhookLimiters{
		perIP:         ratelimit.New(100, time.Minute),
		perConnection: ratelimit.New(1, time.Minute),
	}
	mux := throttledMux(limits, chassis, quietLogger())

	const flooded = "0198f0a0-0000-7000-8000-000000000005"
	const quiet = "0198f0a0-0000-7000-8000-000000000006"
	mux.ServeHTTP(httptest.NewRecorder(), telegramSecretRequest(flooded, "wrong"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, telegramSecretRequest(flooded, "wrong"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the flooded connection → %d, want 429 — its own budget is spent", rec.Code)
	}

	chassis.status = http.StatusOK
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, telegramSecretRequest(quiet, "s"))
	if rec.Code != http.StatusOK {
		t.Errorf("the second connection's first delivery → %d, want 200 — one connection's flood must not spend another's budget", rec.Code)
	}
}

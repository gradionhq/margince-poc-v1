// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The brake on the Telegram ingress edge, and the one rule that makes it safe
// to put a brake there at all: it meters FAILED admissions, never deliveries.
//
// This is the one unauthenticated edge in this installation that does DATABASE
// work to decide admission — verifying the secret means resolving the
// connection first, which is a pool query plus a probe under every live
// workspace's GUC. Gmail's side of the same chassis compares an in-memory
// constant and touches nothing, so it needs no brake; this one does, for the
// same reason the two other public edges have theirs (publicbooking.go,
// publicpreferences.go): an anonymous caller must not be able to spend the pool.
//
// It is also the one edge where a refusal is UNRECOVERABLE. Telegram has no
// history API, so an update this brake turns away exists nowhere else once the
// provider gives up redelivering it. A counter that spends its budget on
// admitted deliveries would therefore end in the brake denying exactly the
// traffic it was added to protect — and it would do so first behind an ingress
// proxy, where publicClientIP resolves every caller, Telegram and attacker
// alike, to the one peer address the proxy connects from.

package compose

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
)

// telegramWebhookLimiters hold the failed-admission budget for this edge.
//
// Per-IP is the brake that actually holds: a flood comes from one or a few
// hosts. Per-connection covers a distributed flood aimed at one connection id —
// and an attacker who does not know a live connection id spends that budget on
// the id they invented, never on a real bot's.
//
// Both are set far above any real bot's traffic, but that is no longer what
// keeps a legitimate delivery out of them: a delivery carrying the connection's
// registered secret is never counted at all.
type telegramWebhookLimiters struct {
	perIP         *ratelimit.Limiter
	perConnection *ratelimit.Limiter
}

func newTelegramWebhookLimiters() telegramWebhookLimiters {
	return telegramWebhookLimiters{
		perIP:         ratelimit.New(600, time.Minute),
		perConnection: ratelimit.New(300, time.Minute),
	}
}

// throttleTelegramWebhook refuses a caller who has already spent the
// failed-admission budget, before the chassis reads a body or the secret
// function touches the pool, and counts one failure for a caller who reaches
// the chassis and does not clear admission.
//
// Blocked/Record rather than Allow is the whole point (see the ratelimit
// package doc): Blocked peeks without spending, so a delivery that clears the
// constant-time secret compare leaves the budget exactly as it found it. Two
// consequences follow, and both are the finding this shape answers. Telegram's
// own retry of a 429 — it treats any non-2xx as "try again later" — cannot
// deepen the refusal that caused it, because a request refused HERE never
// reaches the chassis and so is never counted. And a genuine delivery can only
// be refused while an attacker sharing its network path still has an unexpired
// window of failures, rather than for as long as it keeps arriving.
//
// 429 rather than a 4xx that ends the delivery: a throttled delivery was never
// inspected, so the update is not poison and must not be dropped. The response
// carries no body, like every other refusal on this edge, and the log line is
// what makes a channel gone dark under this brake tell itself apart from a
// channel nobody is writing to.
func throttleTelegramWebhook(limits telegramWebhookLimiters, next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, connection := publicClientIP(r), r.PathValue("connection_id")
		if limits.perIP.Blocked(ip) || limits.perConnection.Blocked(connection) {
			log.WarnContext(r.Context(), "telegram webhook: refused a delivery over the failed-admission budget",
				"peer", ip, "connection", connection)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		admission := &telegramAdmissionRecorder{ResponseWriter: w}
		next.ServeHTTP(admission, r)
		if admission.refused() {
			// Reached only by a request that passed the Blocked gate above, so
			// the per-IP count cannot exceed its own limit within a window —
			// which is also what bounds the per-connection key space against a
			// caller inventing a fresh connection id per request.
			limits.perIP.Record(ip)
			limits.perConnection.Record(connection)
		}
	})
}

// telegramAdmissionRecorder remembers the status the chassis wrote, which is
// the only report of whether a request got past admission — the chassis owns
// that decision and answers it in one place, so re-deriving it out here would
// fork the rule.
type telegramAdmissionRecorder struct {
	http.ResponseWriter
	status int
}

func (a *telegramAdmissionRecorder) WriteHeader(status int) {
	a.status = status
	a.ResponseWriter.WriteHeader(status)
}

// refused reports whether this request failed ADMISSION — a wrong or absent
// secret, a failed second factor, or a method this edge does not serve. Every
// other outcome, poison payloads and transient faults included, belongs to a
// caller that proved it holds the connection's secret and is not what this
// budget is defending against.
//
// A handler that wrote no status at all served 200 and therefore admitted.
func (a *telegramAdmissionRecorder) refused() bool {
	return a.status == http.StatusUnauthorized || a.status == http.StatusMethodNotAllowed
}

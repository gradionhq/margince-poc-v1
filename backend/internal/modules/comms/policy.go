// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
)

// SendPolicy decides whether a delivery may transmit NOW. It cannot refuse a
// send — that is a gate's job, and the two are different facts: a gate says
// never, a policy says not yet.
//
// The dispatcher holds an ordered chain and takes the first non-zero wait, so a
// new policy is a registration rather than a change to the dispatcher.
type SendPolicy interface {
	// Name identifies the policy in the delivery's reason and in logs, so an
	// operator seeing a deferred message knows which rule deferred it.
	Name() string

	// Wait reports how long this delivery must wait. Zero permits it now.
	Wait(ctx context.Context, d Delivery) time.Duration
}

// MailboxRatePolicy paces one mailbox's sends. Providers enforce their own
// per-user quotas and throttle an account that bursts past them; pacing
// ourselves keeps a legitimate run of sends from costing the user their
// mailbox's standing.
type MailboxRatePolicy struct {
	limiter *ratelimit.Limiter
	window  time.Duration
}

// NewMailboxRatePolicy allows limit sends per mailbox per window.
func NewMailboxRatePolicy(limit int, window time.Duration, now func() time.Time) *MailboxRatePolicy {
	if now == nil {
		now = time.Now
	}
	return &MailboxRatePolicy{limiter: ratelimit.NewWithClock(limit, window, now), window: window}
}

// Name identifies this policy on a deferred delivery.
func (p *MailboxRatePolicy) Name() string { return "mailbox_rate" }

// Wait keys on the MAILBOX, not the message: a per-message key would give every
// send its own window and pace nothing. It peeks the limiter with Blocked
// rather than Allow: a slot stands for a message that actually reached the
// provider, so merely asking whether we may send must not spend one — a
// delivery that is asked and then deferred (by an earlier policy in the
// chain, or by a retry that ends in another deferral) must still have its
// slot when it is asked again.
//
// The wait returned is always a full window, not the time remaining in the
// caller's current one: the limiter has no way to report that remainder, so
// a delivery that hits the limit near the end of its window waits longer
// than strictly necessary. That is a latency cost, not a correctness one —
// the next attempt, a full window later, is always past the boundary.
// Combined with the limiter being in-process, the effective ceiling is
// per-replica: a multi-worker deployment paces each worker's view of the
// mailbox independently, not the mailbox as a whole.
func (p *MailboxRatePolicy) Wait(_ context.Context, d Delivery) time.Duration {
	if !p.limiter.Blocked(d.UserID.String()) {
		return 0
	}
	return p.window
}

// SendRecorder is the optional seam a policy implements when the resource it
// meters is only consumed by an actual transmission, not by being asked
// about. Type-asserted by the dispatcher (mirroring how the connector
// package type-asserts Watcher and Backfiller), so a policy that meters
// nothing simply does not implement it and SendPolicy stays two methods.
type SendRecorder interface {
	Recorded(d Delivery)
}

// Recorded counts one actual send against the mailbox's quota. The
// dispatcher calls this once transmission succeeds — not on every Wait
// check — so the limit tracks messages the provider received, the thing it
// actually throttles on.
func (p *MailboxRatePolicy) Recorded(d Delivery) {
	p.limiter.Record(d.UserID.String())
}

var (
	_ SendPolicy   = (*MailboxRatePolicy)(nil)
	_ SendRecorder = (*MailboxRatePolicy)(nil)
)

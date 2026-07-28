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
// send its own window and pace nothing.
func (p *MailboxRatePolicy) Wait(_ context.Context, d Delivery) time.Duration {
	if p.limiter.Allow(d.UserID.String()) {
		return 0
	}
	return p.window
}

var _ SendPolicy = (*MailboxRatePolicy)(nil)

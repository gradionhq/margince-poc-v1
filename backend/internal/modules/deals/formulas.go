// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The stalled-deal rule (formulas-and-rules §8): a deterministic,
// fixed-clock-stable boolean over last_activity_at with the "customer
// asked us to wait" suppression. Two spellings exist by necessity —
// the Go predicate stamps the wire flag, the SQL clause filters lists
// server-side — and the agreement test in the integration lane keeps
// them from drifting.

import "time"

// StalledThresholdDays is the §8 tunable: open deals idle longer than
// this are stalled unless a wait suppresses it.
const StalledThresholdDays = 60

// IsStalled evaluates §8.1 at one instant. Idle is an absolute-duration
// comparison on UTC instants, never a calendar-day count — stable under
// a fixed test clock, identical across zones.
func IsStalled(status string, createdAt time.Time, lastActivityAt, waitUntil *time.Time, now time.Time) bool {
	if DealStatus(status) != DealOpen {
		return false // closed deals never stall
	}
	base := createdAt
	if lastActivityAt != nil {
		base = *lastActivityAt
	}
	if now.Sub(base) <= StalledThresholdDays*24*time.Hour {
		return false
	}
	if waitUntil != nil && now.Before(*waitUntil) {
		return false // respecting an explicit deferral
	}
	return true
}

// stalledSQL is the list-filter spelling of IsStalled (true branch);
// callers negate it for stalled=false.
const stalledSQL = `(status = 'open'
	AND coalesce(last_activity_at, created_at) < now() - interval '60 days'
	AND (wait_until IS NULL OR wait_until <= now()))`

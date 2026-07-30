// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The stalled-deal rule (formulas-and-rules §8): a deterministic,
// fixed-clock-stable boolean over last_activity_at with the "customer
// asked us to wait" suppression. Two spellings exist by necessity —
// the Go predicate stamps the wire flag, the SQL clause filters lists
// server-side — and the agreement test in the integration lane keeps
// them from drifting.

import (
	"fmt"
	"time"
)

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

// stalledSQL is the list-filter spelling of IsStalled (true branch), against
// the database clock; callers negate it for stalled=false.
var stalledSQL = StalledClause("", "now()")

// StalledClause is the SQL spelling of IsStalled at a caller-named instant.
//
// It exists so a caller with an INJECTED clock — the 360's composite read pins
// one, so a stall window cannot flake between seeding and reading — can count
// and filter stalled deals by the same rule the wire flag is stamped from,
// rather than re-spelling §8.1 next to its own query. stalledSQL is built from
// it too, so there is one spelling of the clause and one of the predicate, and
// the integration lane's agreement test keeps those two from drifting.
//
// alias is the table alias to qualify the columns with (empty for none), and
// nowExpr is the SQL expression carrying the instant — a bind placeholder like
// "$3" for an injected clock, or "now()" for the database's own.
func StalledClause(alias, nowExpr string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return fmt.Sprintf(`(%[1]sstatus = 'open'
	AND coalesce(%[1]slast_activity_at, %[1]screated_at) < %[2]s - interval '%[3]d days'
	AND (%[1]swait_until IS NULL OR %[1]swait_until <= %[2]s))`,
		prefix, nowExpr, StalledThresholdDays)
}

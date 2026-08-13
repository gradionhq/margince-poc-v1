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

// StalledSQL is the list-filter spelling of IsStalled (true branch);
// callers negate it for stalled=false. Takes the query's table alias —
// deal_read.go's own list query reads the unaliased `deal` table (alias
// ""), but a caller that JOINs a table sharing a column name this
// expression touches (compose/report.go's deals-by-stage joins `stage`,
// which also has created_at) MUST qualify every column or the reference
// is ambiguous SQL, not merely wrong. One spelling, parameterized, rather
// than a second copy that only agrees by accident.
func StalledSQL(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return fmt.Sprintf(`(%[1]sstatus = 'open'
		AND coalesce(%[1]slast_activity_at, %[1]screated_at) < now() - interval '%[2]d days'
		AND (%[1]swait_until IS NULL OR %[1]swait_until <= now()))`, prefix, StalledThresholdDays)
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The sync schedule is written by ONE clock, derived from this package's source
// rather than remembered. The due-scan gates on
// `COALESCE(s.next_sync_at, now()) <= now()` (registry_connections.go), so the
// column is only ever compared against the clock inside Postgres and every
// write of it has to start there too.
//
// The direction that bit here is the one with no margin on the SHORT side: a
// worker whose clock ran BEHIND the database's by X turned every delay into
// delay − X, and the transient ladder's first rung is ~96s at the low end of
// its jitter — so a skew of that size defeated the ADR-0063 backoff outright
// and re-synced a refusing provider on every tick. A rate-limit was worse
// still, since the same subtraction under-honours a Retry-After the provider
// asked for.
//
// Scope is this package because capture_sync_state is owned here, pinned by
// tableownership_test.go. The check lives in gatekit, shared with overlay's
// next_sweep_at, which owes the identical thing.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

func TestEverySyncScheduleWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "next_sync_at"}.Require(t)
}

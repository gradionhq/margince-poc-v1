// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// An approval's window is written by ONE clock, the database's, derived from
// this package's source rather than remembered.
//
// Every reader compares expires_at against now() INSIDE Postgres —
// effectiveStatus, the join-a-pending-proposal probe, the decide path, the
// expiry sweep — so a deadline bound from the app process makes each of those a
// cross-clock comparison. Unlike a sync schedule, the cost is not throughput: a
// staged action outliving its stated window is decidable by a human who should
// no longer be able to decide it, and one predeceasing it refuses a decision
// that is still in time. Both are authorization outcomes.
//
// Scope is this package because approval is owned here, pinned by
// tableownership_test.go.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

func TestEveryApprovalExpiryWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "expires_at"}.Require(t)
}

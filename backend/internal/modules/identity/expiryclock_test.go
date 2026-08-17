// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Every deadline this package writes is written by ONE clock, the database's,
// derived from this package's source rather than remembered.
//
// identity holds more of these than any other package — a session's idle and
// absolute windows, a passport's, a refresh token's, an authorization code's, a
// password-reset token's, a client document's cache window — and every one of
// them is read as `expires_at > now()` INSIDE Postgres. What each decides is
// whether a credential is still good, so a deadline bound from the app process
// is not a pacing error: a process running ahead refuses a session that is
// still valid, and one running behind honours a token past its stated life.
//
// Scope is this package because it owns every table named above, pinned by
// tableownership_test.go.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// callerChosenExpiry ratifies the one deadline here that is nobody's clock of
// ours. A share's expiry arrives in the request body: the human granting access
// names the day it ends, and rewriting that as an offset from now() would move
// the date they chose.
var callerChosenExpiry = gatekit.Waive(map[string]string{
	"grants.go": "a record share's expiry is the granting human's own choice, arriving as an absolute instant in the request body — deriving it from now() would silently move the date they picked, and the value was never this system's clock to correct",
})

func TestEveryCredentialExpiryWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "expires_at", Exempt: callerChosenExpiry}.Require(t)
	callerChosenExpiry.AssertAllMatched(t)
}

// The session's idle window, which is clamped by the absolute one above.
func TestEverySessionIdleExpiryWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "idle_expires_at"}.Require(t)
}

// A client metadata document's cache window. Freshness is read as
// `metadata_expires_at > now()`, so serving a stale redirect list past the
// window its publisher was promised is the cost of getting this one wrong.
func TestEveryClientMetadataExpiryWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "metadata_expires_at"}.Require(t)
}

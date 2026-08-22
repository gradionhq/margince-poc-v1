// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The room session: what a buyer holds after exchanging an invitation, and the
// only authority the public edge ever reasons from.
//
// A session names ONE participant in ONE room. Everything the buyer reads or
// writes goes through store methods that take this struct and put its room and
// participant into the WHERE clause — never through the seller's store methods,
// which gate on a seat the buyer does not hold. platform/auth refuses the buyer
// principal outright, so a public handler that reached a seller method would
// get a 403, not a leak; the rule here is the second line of defence, held by
// TestPublicHandlersReachOnlyTheSessionScopedStore.

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Session is a resolved buyer session. The zero value admits nobody.
type Session struct {
	ID            ids.UUID
	ParticipantID ids.DealRoomParticipantID
	RoomID        ids.DealRoomID
	// Capability is the seller's decision about this person: `view` reads
	// only, `comment` and `reviewer` may also work the list. Read at every
	// write, so a capability lowered after sign-in binds on the next request.
	Capability string
	// Preview marks a seller looking at their own room as a buyer. Reads
	// everything a buyer would; every write refuses it.
	Preview bool
}

// The session token is minted like the credential it was exchanged from: 256
// bits from crypto/rand, a recognizable prefix, only the digest stored.
const sessionPrefix = "mdrs_"

// sessionTTL is absolute. There is no idle extension: a buyer who is still
// reading a week later exchanges a fresh link, which is cheap, rather than
// holding a token that never lapses.
const sessionTTL = 7 * 24 * time.Hour

// lastSeenGranularity bounds how often a request touches last_seen_at. The
// column answers "when were they last here?" for the seller's roster, a
// question a minute's precision serves; a write per request would not.
const lastSeenGranularity = time.Minute

type sessionKey struct{}

// WithSession binds a resolved session for the handlers downstream.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFrom returns the bound session. A handler on the public edge that
// finds none is misrouted, and the store refuses the zero value anyway.
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(Session)
	return s, ok && s.ID != ids.Nil
}

// BuyerPrincipal is the actor a buyer's write is attributed to. The id is the
// participant's, which is the one durable name a buyer has; the kind is what
// makes every platform/auth gate refuse it. Exported for the compose edge,
// which binds it for every session-bearing request; the exchange binds it
// itself once the credential has named the participant.
func BuyerPrincipal(participantID ids.DealRoomParticipantID) principal.Principal {
	return principal.Principal{
		Type: principal.PrincipalBuyer,
		ID:   "buyer:" + participantID.String(),
	}
}

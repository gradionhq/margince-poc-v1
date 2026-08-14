// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// How long a staging stays approvable, and the kinds for which that question
// does not apply.
//
// Split from staging.go because it is one rule read from two ends — the write
// path stamps expires_at (ttlFor) and the read path decides what to do with it
// (effectiveStatus, via ExpiresNever) — and a policy whose halves live apart is
// one that can drift into stamping a row non-expiring and reading it expired.

import "time"

// stagingTTL bounds how long an unactioned staging stays approvable; a
// week-old agent intention should be re-proposed against fresh state.
const stagingTTL = 24 * time.Hour

// ExpiresNever reports whether a kind's stagings are exempt from expiry.
//
// The exemption is a property of the SUBJECT, not a longer clock: a held
// scheduled message is not reaped by anything — no expiry on the row, no sweeper
// — so it waits until a human answers, however long that is. A card that expired
// would leave the message waiting with nothing asking about it, which is the
// silent stop the card exists to prevent, and any finite TTL only chooses when
// that happens.
//
// Exported because the read path enforces it (effectiveStatus) and the write
// path records it (ttlFor below): one predicate, two halves, no chance of a card
// that is stamped non-expiring and read as expired.
func ExpiresNever(kind string) bool { return noExpiryKinds[kind] }

// ttlFor is how long ONE kind's staging stays approvable.
//
// The default above is right for a PROPOSAL: an agent's intention goes stale
// against the state it was formed on, and re-proposing is how it gets fresh.
//
// It is wrong for a staging whose subject is already WAITING. A held scheduled
// message does not go stale and nothing reaps it — there is no expiry on the row
// and no sweeper — so it waits until a human answers, however long that is. Any
// finite TTL therefore only moves the cliff: past it the card expires and the
// message is still held, which is the silent stop the card exists to prevent.
//
// So those kinds do not expire. The horizon is the subject's own: the card is
// withdrawn when the message is answered (compose's ResolveHeldInTx) or decided
// on directly, and until then the question is still live and still worth asking.
func ttlFor(kind string) time.Duration {
	if ExpiresNever(kind) {
		// The column is NOT NULL and every other reader compares against it, so
		// a non-expiring row still needs a value. It is never CONSULTED for
		// these kinds — effectiveStatus skips the comparison — so this is a
		// placeholder rather than a deadline, and moving it changes nothing.
		return unusedExpiryPlaceholder
	}
	return stagingTTL
}

// noExpiryKinds are the stagings whose subject waits indefinitely, so the card
// must too. Add a kind here only when nothing reaps its subject: for anything
// that ages out on its own, the default TTL is the right answer.
var noExpiryKinds = map[string]bool{KindScheduledSendHeld: true}

// unusedExpiryPlaceholder fills expires_at for a kind whose expiry is never
// read. NOT a TTL: ExpiresNever is what makes these rows non-expiring, and this
// value exists only because the column cannot be null.
const unusedExpiryPlaceholder = 100 * 365 * 24 * time.Hour

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"testing"
	"time"
)

// A card whose subject waits indefinitely does not expire — asserted as
// BEHAVIOUR, by putting a row far past its recorded expiry and reading it back.
//
// The earlier version of this test compared TTL numbers, and passed a card that
// expired after a century: a bigger number is a later cliff, not the absence of
// one. Nothing reaps a held scheduled message, so the card asking about it must
// still be there whenever the human gets to it.
func TestACardForAnUnreapedSubjectNeverReadsAsExpired(t *testing.T) {
	stamped := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	// Long enough that no TTL, however generous, would still be running.
	// AddDate rather than a Duration: five centuries overflows int64 nanoseconds,
	// which is itself a hint about how wrong "a big number" was as the answer.
	farFuture := stamped.AddDate(500, 0, 0)

	held := row{Kind: KindScheduledSendHeld, Status: statusPending, ExpiresAt: stamped}
	if got := held.effectiveStatus(farFuture); got != statusPending {
		t.Fatalf("a held message's card reads %q five centuries past its stamped expiry, want %q — the message is still held and nothing else asks about it",
			got, statusPending)
	}

	// The exemption must not widen. A proposal DOES go stale, and reading an old
	// one as expired is why the clock exists.
	proposal := row{Kind: "deepread", Status: statusPending, ExpiresAt: stamped}
	if got := proposal.effectiveStatus(farFuture); got != StatusExpired {
		t.Fatalf("an ordinary proposal reads %q past its expiry, want %q", got, StatusExpired)
	}
	// …and an unexpired one is untouched by any of this.
	if got := proposal.effectiveStatus(stamped.Add(-time.Hour)); got != statusPending {
		t.Fatalf("a live proposal reads %q before its expiry, want %q", got, statusPending)
	}
}

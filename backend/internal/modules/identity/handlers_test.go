// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// ResetRateLimits clears the four in-process auth lockout buckets — the
// state a non-production data reset cannot otherwise reach.

import "testing"

func TestResetRateLimitsReopensASpentBucket(t *testing.T) {
	h := NewHandlers(&Service{})
	for range 3 {
		h.resetPerEmail.Allow("a@b.test|127.0.0.1")
	}
	if h.resetPerEmail.Allow("a@b.test|127.0.0.1") {
		t.Fatal("the 4th attempt within the 3/hour ceiling must be refused; the bucket is spent")
	}

	h.ResetRateLimits()

	if !h.resetPerEmail.Allow("a@b.test|127.0.0.1") {
		t.Error("resetPerEmail still refuses after ResetRateLimits; the bucket was not cleared")
	}
}

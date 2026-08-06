// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

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

// TestResetRateLimitsOnAHandlerSetWithoutBucketsIsANoOp: the caller is the
// non-production data reset, and a nil-limiter panic there would surface to the
// operator as an opaque 500 on a wipe that had already finished. A handler set
// with no buckets has nothing to clear and must say so by returning quietly.
func TestResetRateLimitsOnAHandlerSetWithoutBucketsIsANoOp(t *testing.T) {
	var h Handlers
	h.ResetRateLimits()
	if h.loginFailures != nil || h.resetPerEmail != nil {
		t.Fatal("a zero-value handler set grew limiters; this case exists precisely because it has none")
	}
}

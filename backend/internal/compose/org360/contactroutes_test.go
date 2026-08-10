// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/relstrength"
)

// The producer's vocabulary and this contract's enum are different lists, and
// the translation between them is easy to leave behind: a direct cast compiles
// and ships `weak` on a wire that has never admitted it. So the claim is
// checked against the producer's OWN bucket set rather than a list restated
// here — a bucket added to relstrength fails this until it is mapped.
func TestEveryRelationshipBucketMapsToADeclaredRouteBand(t *testing.T) {
	for _, bucket := range []string{
		relstrength.BucketNone,
		relstrength.BucketWeak,
		relstrength.BucketModerate,
		relstrength.BucketStrong,
	} {
		got := routeBucket(bucket)
		if !got.Valid() {
			t.Errorf("relstrength bucket %q maps to %q, which the Organization360Route enum does not declare", bucket, got)
		}
	}
	// The bands must stay distinguishable: collapsing everything to cold would
	// satisfy the check above and tell a reader nothing.
	if routeBucket(relstrength.BucketStrong) == routeBucket(relstrength.BucketModerate) {
		t.Error("strong and moderate routes render as the same band, so the reader cannot tell a warm route from a developing one")
	}
}

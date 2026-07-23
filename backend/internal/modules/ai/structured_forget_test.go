// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// A validator-rejected completion must never be replayed from the result
// cache: a retried build with an unchanged corpus would otherwise
// deterministically replay its own failure until the TTL expired.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestValidationFailureEvictsTheCachedAnswer(t *testing.T) {
	cheap := NewFakeClient().Script("not json", "still not json", `{"ok":true}`)
	premium := NewFakeClient().Script("also not json")
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap, TierPremium: premium},
		&memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)
	ctx := wsContext(t)

	// The first logical call burns retry and escalation on invalid output.
	if _, _, err := r.CompleteStructured(ctx, TaskColdStart, structuredReq(), jsonObjectValidator); err == nil {
		t.Fatal("three invalid completions must fail the logical call")
	}

	// The IDENTICAL second call must reach the model again instead of
	// replaying the cached invalid answer - and now the model answers validly.
	resp, _, err := r.CompleteStructured(ctx, TaskColdStart, structuredReq(), jsonObjectValidator)
	if err != nil {
		t.Fatalf("retry after eviction: %v", err)
	}
	if resp.Text != `{"ok":true}` {
		t.Fatalf("resp = %q - the cached invalid answer was replayed", resp.Text)
	}
	if calls := len(cheap.Calls()); calls != 3 {
		t.Fatalf("cheap served %d calls, want 3 - the second logical call must miss the cache", calls)
	}
}

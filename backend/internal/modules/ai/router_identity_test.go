// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

// TestRouterEmbedIdentity proves the composite identity string names the
// SAME embed binding routeMeta[TierEmbedLane] and the configured width
// already carry — offline, no live provider (routeMeta/embedDims are the
// literal fields NewRouter derives from a RoutingConfig; see bindRouter in
// routing_bind_test.go for the same construction shape).
func TestRouterEmbedIdentity(t *testing.T) {
	r := &Router{
		routeMeta: map[Tier]routeMeta{
			TierEmbedLane: {provider: "gemini", model: "gemini-embedding-001"},
		},
		embedDims: 768,
	}
	id, dims := r.EmbedIdentity()
	if id != "gemini/gemini-embedding-001@768" || dims != 768 {
		t.Fatalf("got %q,%d", id, dims)
	}
}

// TestRouterEmbedIdentityUnbound proves the embed lane being absent from
// routeMeta (--ai-fake with no embeddings binding, or any boot that never
// configured one) reports a stable empty identity rather than panicking on
// the missing map entry.
func TestRouterEmbedIdentityUnbound(t *testing.T) {
	r := &Router{routeMeta: map[Tier]routeMeta{}}
	id, dims := r.EmbedIdentity()
	if id != "" || dims != 0 {
		t.Fatalf("unbound embed lane must report (\"\", 0), got %q,%d", id, dims)
	}
}

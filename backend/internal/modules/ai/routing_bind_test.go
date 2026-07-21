// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import "testing"

// bindRouter builds a Router carrying only the tier→model bindings the pure
// resolvers read — the default classify/enrich ladder with premium left
// unbound, plus the embed lane.
func bindRouter() *Router {
	return &Router{routeMeta: map[Tier]routeMeta{
		TierLocalSmall: {provider: "ollama", model: "gemma3"},
		TierCheapCloud: {provider: "gemini", model: "gemini-3.1-flash-lite"},
		TierEmbedLane:  {provider: "gemini", model: "gemini-embedding-001"},
		// TierPremium intentionally absent — an unbound tier.
	}}
}

func TestBoundLadder(t *testing.T) {
	r := bindRouter()
	if got := r.BoundLadder(TaskCaptureClassify); len(got) != 2 ||
		got[0] != (ModelRef{"ollama", "gemma3"}) ||
		got[1] != (ModelRef{"gemini", "gemini-3.1-flash-lite"}) {
		t.Fatalf("BoundLadder(capture_classify) = %+v", got)
	}
	// Embeddings resolve via the embed lane, which is not in taskLadders.
	if got := r.BoundLadder(TaskEmbeddings); len(got) != 1 ||
		got[0] != (ModelRef{"gemini", "gemini-embedding-001"}) {
		t.Fatalf("BoundLadder(embeddings) = %+v", got)
	}
	// A ladder whose rungs are all unbound → empty slice, never a nil index at
	// the call site.
	empty := &Router{routeMeta: map[Tier]routeMeta{}}
	if got := empty.BoundLadder(TaskCaptureClassify); len(got) != 0 {
		t.Fatalf("all-unbound ladder must yield empty, got %+v", got)
	}
}

func TestCurrentModelForTier(t *testing.T) {
	r := bindRouter()
	if m, ok := r.CurrentModelForTier(TierCheapCloud); !ok ||
		m != (ModelRef{"gemini", "gemini-3.1-flash-lite"}) {
		t.Fatalf("CurrentModelForTier(cheap_cloud) = %+v ok=%v", m, ok)
	}
	if _, ok := r.CurrentModelForTier(TierPremium); ok {
		t.Fatalf("premium is unbound; want ok=false")
	}
	// An entry present but empty-modelled is still unbound.
	blank := &Router{routeMeta: map[Tier]routeMeta{TierCheapCloud: {provider: "gemini"}}}
	if _, ok := blank.CurrentModelForTier(TierCheapCloud); ok {
		t.Fatalf("empty model must read as unbound; want ok=false")
	}
}

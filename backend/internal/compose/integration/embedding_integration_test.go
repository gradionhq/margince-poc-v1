// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The retrieval vector substrate (B-EP05.16/.17/.18): content-hash
// reuse (unchanged text costs no model call), similarity ranking under
// object RBAC + row scope, RRF fusion where lane agreement beats a solo
// favorite, and the embed-gen consumer maintaining rows off entity
// events. Driven by the deterministic fake embedder — same text, same
// vector — so ranking assertions are exact, not probabilistic.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// fakeEmbedder wires fake through the router (compose.NewLocalModelPath),
// the same seam production's Embedder lane rides, and hands back the
// wrapped search.Embedder — fake itself stays available for .Calls()
// assertions since it is the exact instance serving every call.
func fakeEmbedder(t *testing.T, fake *ai.FakeClient) search.Embedder {
	t.Helper()
	modelPath, err := compose.NewLocalModelPath(ai.FakeRoutingConfig(), ai.WithFakeClient(fake), ai.WithoutResultCache())
	if err != nil {
		t.Fatalf("NewLocalModelPath: %v", err)
	}
	return modelPath.Embedder
}

func TestEmbeddingUpsertReusesUnchangedText(t *testing.T) {
	e := setupSearch(t)
	fake := ai.NewFakeClient()
	embedder := fakeEmbedder(t, fake)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Vector Person', 'manual', 'human:x')`)

	fresh, err := e.store.UpsertEmbedding(e.Admin(), "person", personID, "Vector Person", embedder)
	if err != nil || !fresh {
		t.Fatalf("first upsert fresh=%v err=%v", fresh, err)
	}
	fresh, err = e.store.UpsertEmbedding(e.Admin(), "person", personID, "Vector Person", embedder)
	if err != nil || fresh {
		t.Fatalf("unchanged text recomputed: fresh=%v err=%v", fresh, err)
	}
	if calls := len(fake.Calls()); calls != 1 {
		t.Fatalf("embedder called %d times for unchanged text, want 1", calls)
	}
	fresh, err = e.store.UpsertEmbedding(e.Admin(), "person", personID, "Vector Person renamed", embedder)
	if err != nil || !fresh {
		t.Fatalf("changed text not re-embedded: fresh=%v err=%v", fresh, err)
	}
}

func TestSimilarityRankingAndRowScope(t *testing.T) {
	e := setupSearch(t)
	fake := ai.NewFakeClient()
	embedder := fakeEmbedder(t, fake)

	shared := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Anke Schulz', 'manual', 'human:x')`)
	foreign := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Bernd Kruse', $3, 'manual', 'human:x')`, e.Rep3)
	for id, text := range map[ids.UUID]string{shared: "Anke Schulz", foreign: "Bernd Kruse"} {
		if _, err := e.store.UpsertEmbedding(e.Admin(), "person", id, text, embedder); err != nil {
			t.Fatal(err)
		}
	}

	// The fake embeds identical text to the identical vector, so the
	// exact-text query must rank its entity first with similarity ~1. This
	// is the test's own oracle for "what vector would identical text
	// produce" — not the AI call under test (that's the embedder above) —
	// so it stays a direct, offline call to the fake's deterministic hash.
	queryVec, err := fake.Embed(context.Background(), model.EmbedRequest{Inputs: []string{"Anke Schulz"}})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := e.store.SimilarEntities(e.Admin(), queryVec.Vectors[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != shared || hits[0].Similarity < 0.999 {
		t.Fatalf("similarity ranking wrong: %+v", hits)
	}

	// rep1 (team1) cannot see rep3's row through the vector lane either.
	hits, err = e.store.SimilarEntities(e.asTeamRep(e.Rep1, e.Team1), queryVec.Vectors[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.ID == foreign {
			t.Fatalf("row scope leaked through the vector lane: %+v", hits)
		}
	}
}

func TestHybridRRFAgreementWins(t *testing.T) {
	e := setupSearch(t)
	fake := ai.NewFakeClient()
	embedder := fakeEmbedder(t, fake)

	agree := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Solar Grid', 'manual', 'human:x')`)
	lexOnly := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Solar Panels', 'manual', 'human:x')`)
	vecOnly := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Photovoltaik Cluster', 'manual', 'human:x')`)

	// Embeddings: the agreeing row and the vector-only row both embed
	// the QUERY text (identical vector); the lexical-only row embeds
	// something unrelated.
	for id, text := range map[ids.UUID]string{
		agree:   "solar grid",
		vecOnly: "solar grid",
		lexOnly: "completely different topic",
	} {
		if _, err := e.store.UpsertEmbedding(e.Admin(), "person", id, text, embedder); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := e.store.HybridSearch(e.Admin(), "solar grid", embedder, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 3 {
		t.Fatalf("expected all three lanes' rows fused, got %+v", hits)
	}
	if hits[0].ID != agree {
		t.Fatalf("lane agreement must win RRF: %+v", hits)
	}
	found := map[ids.UUID]bool{}
	for _, h := range hits {
		found[h.ID] = true
	}
	if !found[lexOnly] || !found[vecOnly] {
		t.Fatalf("single-lane hits missing from fusion: %+v", hits)
	}
}

func TestEmbedGenMaintainsRowsFromEvents(t *testing.T) {
	e := setupSearch(t)
	fake := ai.NewFakeClient()
	gen := search.NewEmbedGen(e.store, fakeEmbedder(t, fake))

	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Event Driven', 'manual', 'human:x')`)
	env := kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        "person.created",
		WorkspaceID: e.WS,
		Entity:      kevents.EntityRef{Type: "person", ID: personID},
	}
	if err := gen.HandleEvent(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	// Redelivery (at-least-once bus) costs no second model call.
	env.EventID = ids.NewV7()
	if err := gen.HandleEvent(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	embedCalls := 0
	for _, c := range fake.Calls() {
		if c.Op == "embed" {
			embedCalls++
		}
	}
	if embedCalls != 1 {
		t.Fatalf("embed-gen called the model %d times for one unchanged entity, want 1", embedCalls)
	}

	// A non-entity event is not ours.
	if err := gen.HandleEvent(context.Background(), kevents.Envelope{Type: "approval.decided", WorkspaceID: e.WS, Entity: kevents.EntityRef{Type: "approval", ID: ids.NewV7()}}); err != nil {
		t.Fatalf("foreign event must be a no-op, got %v", err)
	}
}

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
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// fakeEmbedDims is the width every named test binding below uses unless a
// test deliberately wants a mismatch — matching FakeClient's own default
// so nothing here depends on a magic number rediscovered elsewhere.
const fakeEmbedDims = 1024

// fakeEmbedder wires fake through the router (compose.NewLocalModelPath)
// under a fixed named binding ("embed-fake"), the same seam production's
// Embedder lane rides, and hands back the wrapped search.Embedder — fake
// itself stays available for .Calls() assertions since it is the exact
// instance serving every call. Naming the binding (rather than leaving
// ai.FakeRoutingConfig's Embeddings.Model empty) matters now that
// UpsertEmbedding stamps and gates on EmbedIdentity(): an unnamed binding
// reports the legitimate-but-unhelpful "unbound lane" identity ("", 0),
// which would fail every upsert's width guard once dims stop being a
// fixed package constant.
func fakeEmbedder(t *testing.T, fake *ai.FakeClient) search.Embedder {
	t.Helper()
	return fakeEmbedderNamed(t, fake, "embed-fake")
}

// fakeEmbedderNamed is fakeEmbedder with an explicit binding name, so a
// test can stand up two distinct EmbedIdentity() values (a binding swap)
// riding the SAME underlying fake client and dimension — the two "still
// 1×1024, just a different model" scenarios TestUpsertReembeds... and
// TestUpsertSkipsUnchanged... need. Width stays fakeEmbedDims throughout:
// varying it is not what these tests are proving (the guard tests below
// use stubEmbedder for that instead).
func fakeEmbedderNamed(t *testing.T, fake *ai.FakeClient, modelName string) search.Embedder {
	t.Helper()
	cfg := ai.FakeRoutingConfig()
	cfg.Embeddings = ai.EmbeddingsConfig{
		ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake, Model: modelName},
		Dimensions:     fakeEmbedDims,
	}
	modelPath, err := compose.NewLocalModelPath(cfg, ai.WithFakeClient(fake), ai.WithoutResultCache())
	if err != nil {
		t.Fatalf("NewLocalModelPath: %v", err)
	}
	return modelPath.Embedder
}

// stubEmbedder is a minimal hand-rolled search.Embedder for the two guard
// tests below: proving the width and zero-vector guards fire requires an
// Embed call whose result deliberately disagrees with its own declared
// identity — something the router+fake path can't produce (the fake
// always honors the requested Dimensions), so these bypass the router
// entirely and answer exactly what each test scripts.
type stubEmbedder struct {
	identity string
	dims     int
	vectors  [][]float32
	resDims  int
}

func (s stubEmbedder) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	return model.Embeddings{Vectors: s.vectors, Dims: s.resDims}, nil
}

func (s stubEmbedder) EmbedIdentity() (string, int) { return s.identity, s.dims }

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
	identity, _ := embedder.EmbedIdentity()
	hits, err := e.store.SimilarEntities(e.Admin(), queryVec.Vectors[0], identity, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != shared || hits[0].Similarity < 0.999 {
		t.Fatalf("similarity ranking wrong: %+v", hits)
	}

	// rep1 (team1) cannot see rep3's row through the vector lane either.
	hits, err = e.store.SimilarEntities(e.asTeamRep(e.Rep1, e.Team1), queryVec.Vectors[0], identity, 10)
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

// storedEmbeddingModel reads a person's chunk-0 row's model column
// directly (the owner connection bypasses RLS, same as seed) — the
// assertion surface for "which binding actually produced this row" the
// tests below need. Every current caller checks a person row; narrow to
// that instead of carrying an entityType parameter no test varies.
func (e *searchEnv) storedEmbeddingModel(t *testing.T, entityID ids.UUID) string {
	t.Helper()
	var model string
	err := e.owner.QueryRow(context.Background(),
		`SELECT model FROM embedding WHERE workspace_id = $1 AND entity_type = 'person' AND entity_id = $2 AND chunk_ix = 0`,
		e.WS, entityID).Scan(&model)
	if err != nil {
		t.Fatalf("reading stored embedding: %v", err)
	}
	return model
}

// TestUpsertReembedsOnIdentityChange proves a binding swap (same text,
// different EmbedIdentity — the width-migration / model-swap scenario)
// re-derives the row rather than trusting a hash match alone: skipping
// on hash-only would leave every existing row stamped with a model that
// no longer serves the workspace, indistinguishable from a live one.
func TestUpsertReembedsOnIdentityChange(t *testing.T) {
	e := setupSearch(t)
	fake := ai.NewFakeClient()
	embedderA := fakeEmbedderNamed(t, fake, "model-a")
	embedderB := fakeEmbedderNamed(t, fake, "model-b")
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Identity Person', 'manual', 'human:x')`)

	fresh, err := e.store.UpsertEmbedding(e.Admin(), "person", personID, "Same Text", embedderA)
	if err != nil || !fresh {
		t.Fatalf("first upsert fresh=%v err=%v", fresh, err)
	}
	wantIdentityA, _ := embedderA.EmbedIdentity()
	if gotModel := e.storedEmbeddingModel(t, personID); gotModel != wantIdentityA {
		t.Fatalf("stored model = %q, want %q", gotModel, wantIdentityA)
	}

	fresh, err = e.store.UpsertEmbedding(e.Admin(), "person", personID, "Same Text", embedderB)
	if err != nil || !fresh {
		t.Fatalf("identity change did not re-embed unchanged text: fresh=%v err=%v", fresh, err)
	}
	wantIdentityB, _ := embedderB.EmbedIdentity()
	if wantIdentityA == wantIdentityB {
		t.Fatalf("test setup produced identical identities %q — no swap exercised", wantIdentityA)
	}
	if gotModel := e.storedEmbeddingModel(t, personID); gotModel != wantIdentityB {
		t.Fatalf("stored model after swap = %q, want %q", gotModel, wantIdentityB)
	}
}

// TestUpsertSkipsUnchangedUnderSameIdentity proves the skip path costs no
// model call when BOTH the hash and the identity are unchanged — the
// counterpart to the re-embed test above, so a same-binding no-op stays
// free even after this task adds the identity comparison.
func TestUpsertSkipsUnchangedUnderSameIdentity(t *testing.T) {
	e := setupSearch(t)
	fake := ai.NewFakeClient()
	embedder := fakeEmbedderNamed(t, fake, "model-c")
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Stable Person', 'manual', 'human:x')`)

	fresh, err := e.store.UpsertEmbedding(e.Admin(), "person", personID, "Stable Text", embedder)
	if err != nil || !fresh {
		t.Fatalf("first upsert fresh=%v err=%v", fresh, err)
	}
	fresh, err = e.store.UpsertEmbedding(e.Admin(), "person", personID, "Stable Text", embedder)
	if err != nil || fresh {
		t.Fatalf("unchanged text+identity recomputed: fresh=%v err=%v", fresh, err)
	}
	if calls := len(fake.Calls()); calls != 1 {
		t.Fatalf("embedder called %d times for unchanged text+identity, want 1 (no model spend on the skip)", calls)
	}
}

// TestUpsertRejectsWidthMismatch proves a corrupt/misconfigured embedder
// — one whose actual response width disagrees with its own declared
// EmbedIdentity dims — fails loudly instead of writing an unrankable
// vector into a column other rows expect at a fixed width.
func TestUpsertRejectsWidthMismatch(t *testing.T) {
	e := setupSearch(t)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Width Mismatch', 'manual', 'human:x')`)
	// dims (999) deliberately differs from resDims (1024): the declared
	// identity and the actual response disagree, which the width guard
	// must catch by comparing against the IDENTITY's width, not a fixed
	// package constant that would happen to equal resDims here.
	stub := stubEmbedder{
		identity: "fake/mismatch@999", dims: 999,
		vectors: [][]float32{make([]float32, 1024)}, resDims: 1024,
	}

	fresh, err := e.store.UpsertEmbedding(e.Admin(), "person", personID, "Width Mismatch Text", stub)
	if err == nil {
		t.Fatal("width mismatch must be a hard error")
	}
	if fresh {
		t.Fatalf("width mismatch must not report fresh=true, got %v", fresh)
	}
}

// TestUpsertRejectsZeroVector proves an all-zero vector — cosine
// similarity against it is 0/0 = NaN, which a naive ORDER BY sim DESC
// sorts FIRST, silently outranking every real match — is rejected before
// it ever reaches storage.
func TestUpsertRejectsZeroVector(t *testing.T) {
	e := setupSearch(t)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Zero Vector', 'manual', 'human:x')`)
	// Width matches (1024/1024) so only the zero-vector guard can be what
	// rejects this — a decoy width mismatch would leave the test proving
	// the wrong guard fired.
	stub := stubEmbedder{
		identity: "fake/zero@1024", dims: 1024,
		vectors: [][]float32{make([]float32, 1024)}, resDims: 1024,
	}

	fresh, err := e.store.UpsertEmbedding(e.Admin(), "person", personID, "Zero Vector Text", stub)
	if err == nil {
		t.Fatal("an all-zero vector must be a hard error")
	}
	if fresh {
		t.Fatalf("zero vector must not report fresh=true, got %v", fresh)
	}
}

// TestSimilarEntitiesFiltersIdentityAndDoesNotCrossDimCrash proves the
// e.model = $identity predicate is BOTH a correctness fix and a
// crash-safety pin (design §5.6/§5.8): the embedding column is unbounded
// after migration 0113, so a store holding rows at two different widths
// under two different bindings must (a) rank only the caller's own
// identity's rows without erroring, and (b) the SAME comparison run
// WITHOUT the identity filter — the shape SimilarEntities would run if
// the predicate were ever removed — must itself error with a dimension
// mismatch, proving the filter is load-bearing, not decoration.
func TestSimilarEntitiesFiltersIdentityAndDoesNotCrossDimCrash(t *testing.T) {
	e := setupSearch(t)
	ctx := context.Background()

	threeDim := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Three Dim Person', 'manual', 'human:x')`)
	twoDim := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Two Dim Person', 'manual', 'human:x')`)

	// Seed the unbounded embedding column directly at two different
	// widths under two different identities — the mixed-width store
	// Task 6's migration made possible, and Task 7's read-side filter
	// must survive.
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'hash-three', 'm@3', '[1,2,3]'::vector)`,
		e.WS, threeDim); err != nil {
		t.Fatalf("seeding the 3-dim row: %v", err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO embedding (workspace_id, entity_type, entity_id, chunk_ix, chunk_hash, model, embedding)
		VALUES ($1, 'person', $2, 0, 'hash-two', 'm@2', '[1,2]'::vector)`,
		e.WS, twoDim); err != nil {
		t.Fatalf("seeding the 2-dim row: %v", err)
	}

	// (a) Filtered to "m@3": returns only the m@3 hit and does not crash
	// against the differently-sized m@2 sibling sharing the same column.
	hits, err := e.store.SimilarEntities(e.Admin(), []float32{1, 2, 3}, "m@3", 10)
	if err != nil {
		t.Fatalf("SimilarEntities must not cross-dim crash when identity-filtered: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != threeDim {
		t.Fatalf("expected only the m@3 hit, got %+v", hits)
	}

	// (b) Pin WHY the filter is load-bearing: the unfiltered shape — no
	// model predicate — over the SAME mixed-width store must error with a
	// dimension mismatch. This is what SimilarEntities would do to every
	// caller the moment the e.model = $identity predicate is removed.
	err = database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT 1 - (embedding <=> $1::vector) FROM embedding`, "[1,2,3]")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sim float64
			if err := rows.Scan(&sim); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err == nil {
		t.Fatal("the unfiltered cross-dim comparison must error, got nil")
	}
	if !strings.Contains(err.Error(), "different vector dimensions") {
		t.Fatalf("expected a dimension-mismatch error, got: %v", err)
	}
}

// embedIdentityNeverCalled is a search.Embedder stub reporting the
// legitimate unbound ("", 0) lane (--ai-fake, or any routing config that
// never bound an embeddings model) whose Embed panics if ever invoked.
// The F1 fix's whole premise is that neither UpsertEmbedding nor
// HybridSearch may reach Embed on this lane — a panicking stub makes
// "never called" a hard test failure rather than a call-count assertion
// that could silently pass at zero for the wrong reason.
type embedIdentityNeverCalled struct{}

func (embedIdentityNeverCalled) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	panic("Embed must never be called on an unbound embed lane")
}

func (embedIdentityNeverCalled) EmbedIdentity() (string, int) { return "", 0 }

// TestUpsertEmbeddingNoOpsAndWritesNoRowOnUnboundLane pins the F1 write-
// seam fix against the real embedding table: an unbound embed lane must
// leave UpsertEmbedding a clean no-op — no error, fresh=false, and no row
// written for the entity — rather than the width-guard hard error
// (dims==0 vs. the fake's own live-width default) that used to fire on
// EVERY embedding write and keep search.EmbedGen.HandleEvent from ever
// acking (platform/events' at-least-once bus then redelivers forever).
func TestUpsertEmbeddingNoOpsAndWritesNoRowOnUnboundLane(t *testing.T) {
	e := setupSearch(t)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Unbound Person', 'manual', 'human:x')`)

	fresh, err := e.store.UpsertEmbedding(e.Admin(), "person", personID, "Unbound Person", embedIdentityNeverCalled{})
	if err != nil {
		t.Fatalf("unbound lane must not error, got %v", err)
	}
	if fresh {
		t.Fatal("unbound lane must not report fresh=true — nothing was embedded")
	}

	var count int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM embedding WHERE workspace_id = $1 AND entity_type = 'person' AND entity_id = $2`,
		e.WS, personID).Scan(&count); err != nil {
		t.Fatalf("counting embedding rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("unbound lane must write NO row, found %d", count)
	}
}

// TestHybridSearchDegradesToLexicalOnUnboundLane is the query-side
// counterpart: HybridSearch must degrade to the lexical lane alone on an
// unbound embed lane — the SAME honest degrade a nil embedder already
// gets — rather than calling Embed/SimilarEntities against a lane with
// no live width or model.
func TestHybridSearchDegradesToLexicalOnUnboundLane(t *testing.T) {
	e := setupSearch(t)
	personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, 'Lexical Only Grid', 'manual', 'human:x')`)

	hits, err := e.store.HybridSearch(e.Admin(), "Lexical Only Grid", embedIdentityNeverCalled{}, 10)
	if err != nil {
		t.Fatalf("unbound lane must not error, got %v", err)
	}
	found := false
	for _, h := range hits {
		if h.ID == personID {
			found = true
		}
	}
	if !found {
		t.Fatalf("lexical lane must still find the seeded row on an unbound embed lane, got %+v", hits)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Option's own wiring-only unit-level proof: each Option is a plain
// field assignment/rebuild that never touches the pool at construction
// time (the module constructors it calls — approvals.NewService,
// people.NewStore, briefs.Handlers.WithL2Ranker — all just store the
// pool reference for later use), so a nil pool and nil PageFetcher/Brain
// are safe here: this test never calls the wired handler, only proves
// the Option actually set what it documents.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func TestWithBusReadySetsTheProbe(t *testing.T) {
	s := &Server{}
	var called bool
	WithBusReady(func(ctx context.Context) error { called = true; return nil })(s, nil)

	if s.busReady == nil {
		t.Fatal("WithBusReady did not set busReady")
	}
	if err := s.busReady(context.Background()); err != nil {
		t.Fatalf("busReady: unexpected error: %v", err)
	}
	if !called {
		t.Fatal("busReady did not invoke the check function WithBusReady was given")
	}
}

func TestWithCaptureConfigRecordsTheDeployedLists(t *testing.T) {
	s := &Server{}
	cfg := CaptureConfig{
		FreemailExtra:      []string{"consumer.example"},
		TransactionalExtra: []string{"esp.example"},
		TransactionalNever: []string{"realco.example"},
	}
	WithCaptureConfig(cfg)(s, nil)

	if len(s.captureConfig.TransactionalExtra) != 1 || s.captureConfig.TransactionalExtra[0] != "esp.example" {
		t.Fatalf("captureConfig = %+v, want the deployed lists recorded so the vault/graph registries apply them", s.captureConfig)
	}
	if len(s.captureConfig.FreemailExtra) != 1 || len(s.captureConfig.TransactionalNever) != 1 {
		t.Fatalf("captureConfig dropped a list: %+v", s.captureConfig)
	}
}

func TestWithColdStartWiresTheEngine(t *testing.T) {
	s := &Server{}
	WithColdStart(nil, nil)(s, nil)
	if s.coldstartHandlers.engine == nil {
		t.Fatal("WithColdStart did not wire a coldStartEngine")
	}
}

func TestWithScrapeWiresTheEngine(t *testing.T) {
	s := &Server{}
	WithScrape(nil, nil)(s, nil)
	if s.scrapeHandlers.engine == nil {
		t.Fatal("WithScrape did not wire a scrapeEngine")
	}
}

// insertOnlyRunnerForTest builds a real *jobs.Runner over a nil pool —
// jobs.NewInserter only stores the pool reference (River's client
// construction never dials), so this is safe at Option-construction time,
// the same "never touches the pool at construction" precedent this file's
// header comment already documents for the module constructors.
func insertOnlyRunnerForTest(t *testing.T) *jobs.Runner {
	t.Helper()
	inserter, err := jobs.NewInserter(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	return inserter
}

// TestWithEmbedReindexLeavesEngineNilOnUnboundLane is the reindex-surface
// half of the F1 fix (the unbound embed lane is a system-wide no-op):
// EmbedIdentity() == "" (--ai-fake, or any routing config that never
// bound an embeddings model) must leave the engine nil exactly like a nil
// router does, so EmbedReindexStatus/Preview/Start fall through to their
// generated 501 instead of wiring a handler that would 500 the first time
// it tries to read a binding marker seedEmbedBinding never planted.
func TestWithEmbedReindexLeavesEngineNilOnUnboundLane(t *testing.T) {
	s := &Server{}
	cfg := ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers:   map[ai.Tier]ai.ProviderConfig{ai.TierLocalSmall: {Provider: ai.ProviderFake}},
		// Embeddings.Model deliberately left empty: the unbound-lane shape
		// (routing_bind.go only stamps TierEmbedLane when Model is
		// non-empty), mirroring ai.FakeRoutingConfig()'s own --ai-fake
		// posture.
		Embeddings: ai.EmbeddingsConfig{ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake}},
	}
	router, err := ai.NewLocalRouter(cfg)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	if identity, _ := router.EmbedIdentity(); identity != "" {
		t.Fatalf("test setup did not produce an unbound router, got identity %q", identity)
	}

	WithEmbedReindex(router, insertOnlyRunnerForTest(t))(s, nil)

	if s.embedReindexHandlers.engine != nil {
		t.Fatal("WithEmbedReindex must leave the engine nil on an unbound embed lane")
	}
}

// TestWithEmbedReindexWiresTheEngineWhenBound is the control: a router
// whose embed lane IS bound must still wire the engine as before — the
// new identity=="" gate must not swallow the legitimate configured case.
func TestWithEmbedReindexWiresTheEngineWhenBound(t *testing.T) {
	s := &Server{}
	cfg := ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers:   map[ai.Tier]ai.ProviderConfig{ai.TierLocalSmall: {Provider: ai.ProviderFake}},
		Embeddings: ai.EmbeddingsConfig{
			ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake, Model: "bound-model"},
			Dimensions:     1024,
		},
	}
	router, err := ai.NewLocalRouter(cfg)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	if identity, _ := router.EmbedIdentity(); identity == "" {
		t.Fatal("test setup did not produce a bound router")
	}

	WithEmbedReindex(router, insertOnlyRunnerForTest(t))(s, nil)

	if s.embedReindexHandlers.engine == nil {
		t.Fatal("WithEmbedReindex must wire the engine when the embed lane is bound")
	}
}

// TestReadyzEmbedStateUnboundLaneReportsUnknownWithoutReadingTheMarker is
// the /readyz half of the F1 fix: an engine wired over an unbound embed
// lane (identity == "") must report "unknown" — the same line an
// engine-nil role reports — WITHOUT calling PopulatedIdentity at all.
// store is deliberately backed by a nil pool: if the identity=="" early
// return ever regresses, the subsequent marker read panics on the nil
// pool instead of quietly returning the wrong status, so this test fails
// loudly rather than passing on a coincidentally-empty error path.
func TestReadyzEmbedStateUnboundLaneReportsUnknownWithoutReadingTheMarker(t *testing.T) {
	s := Server{embedReindexHandlers: embedReindexHandlers{engine: &embedReindexEngine{
		store:    search.NewStore(nil),
		embedder: unboundTestEmbedder{},
	}}}

	got := s.readyzEmbedState()(context.Background())
	if got != embedStateUnknown {
		t.Fatalf("readyzEmbedState on an unbound lane = %q, want %q", got, embedStateUnknown)
	}
}

// unboundTestEmbedder is a minimal search.Embedder reporting the unbound
// ("", 0) identity — Embed is never expected to be called by anything
// this file exercises, so it is left unimplemented via a panic rather
// than a fuller fake.
type unboundTestEmbedder struct{}

func (unboundTestEmbedder) Embed(context.Context, model.EmbedRequest) (model.Embeddings, error) {
	panic("Embed must never be called by readyzEmbedState")
}

func (unboundTestEmbedder) EmbedIdentity() (string, int) { return "", 0 }

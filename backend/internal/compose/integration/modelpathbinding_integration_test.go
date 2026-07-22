// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// NewModelPath's boot-time embed-binding wiring (Task 16, ADR-0068 design
// §5.6-swap): construction seeds the deployment's embed_store_binding
// marker to the configured identity on an empty store (Task 9's
// SeedBinding, no first-boot wart), and loudly logs — never fails
// construction — when the store is already populated under a DIFFERENT
// identity than the one just configured (an operator swapped the embed
// binding since the marker was seeded). The AI-unconfigured carve-out
// (Router.EmbedIdentity() == "", e.g. a routing config with no embeddings
// model bound) must skip the seed entirely rather than planting an empty
// identity.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
)

// boundEmbedRoutingConfig is a hand-built RoutingConfig (bypassing
// ai.ParseRouting's yaml decode) whose embeddings lane is bound to a
// deterministic fake identity, so EmbedIdentity() reports a known,
// non-empty string this suite can assert against.
func boundEmbedRoutingConfig(embedModel string) ai.RoutingConfig {
	return ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierLocalSmall: {Provider: ai.ProviderFake},
		},
		Embeddings: ai.EmbeddingsConfig{
			ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake, Model: embedModel},
			Dimensions:     1024,
		},
	}
}

// unboundEmbedRoutingConfig leaves the embeddings lane's Model empty —
// embedInclusiveMeta (routing_bind.go) only stamps TierEmbedLane when
// Model is non-empty, so this reproduces the "no embeddings configured"
// deployment (e.g. --ai-fake with no retrieval lane) EmbedIdentity()
// reports as "" without a map-lookup panic.
func unboundEmbedRoutingConfig() ai.RoutingConfig {
	return ai.RoutingConfig{
		Profile: ai.ProfileEUHosted,
		Tiers: map[ai.Tier]ai.ProviderConfig{
			ai.TierLocalSmall: {Provider: ai.ProviderFake},
		},
		Embeddings: ai.EmbeddingsConfig{ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake}},
	}
}

// bufferLogger hands back a *slog.Logger plus the buffer its records land
// in, so a test can assert on the rendered level/message/attrs of a log
// line NewModelPath emits, without depending on a package-level capture
// helper this suite doesn't otherwise need.
func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestNewModelPathSeedsBindingMarkerOnEmptyStore(t *testing.T) {
	e := Setup(t)
	cfg := boundEmbedRoutingConfig("fake-embed-a")
	log, buf := bufferLogger()

	if _, err := compose.NewModelPath(cfg, e.Pool, false, log); err != nil {
		t.Fatalf("NewModelPath: %v", err)
	}

	store := search.NewStore(e.Pool)
	populated, status, _, err := store.PopulatedIdentity(context.Background())
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	const wantIdentity = "fake/fake-embed-a@1024"
	if populated != wantIdentity {
		t.Fatalf("populated_identity = %q, want %q (construction must seed the LIVE configured identity)", populated, wantIdentity)
	}
	if status != "idle" {
		t.Fatalf("status = %q, want idle", status)
	}
	if strings.Contains(buf.String(), "embed binding changed") {
		t.Fatalf("a freshly seeded, empty store must not log a binding change, got: %s", buf.String())
	}
}

func TestNewModelPathLogsLoudlyOnChangedBinding(t *testing.T) {
	e := Setup(t)
	store := search.NewStore(e.Pool)
	const priorIdentity = "fake/fake-embed-old@2048"
	if err := store.SeedBinding(context.Background(), priorIdentity); err != nil {
		t.Fatalf("seeding the prior binding: %v", err)
	}

	cfg := boundEmbedRoutingConfig("fake-embed-new")
	log, buf := bufferLogger()

	if _, err := compose.NewModelPath(cfg, e.Pool, false, log); err != nil {
		t.Fatalf("NewModelPath must not fail construction on a changed binding: %v", err)
	}

	// SeedBinding's ON CONFLICT DO NOTHING must leave the marker untouched —
	// the store still serves reads under the OLD identity; a reindex is an
	// ops action this construction call never takes on its own.
	populated, _, _, err := store.PopulatedIdentity(context.Background())
	if err != nil {
		t.Fatalf("PopulatedIdentity: %v", err)
	}
	if populated != priorIdentity {
		t.Fatalf("populated_identity = %q, want %q (a changed binding must never be silently overwritten)", populated, priorIdentity)
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=ERROR") {
		t.Fatalf("a changed embed binding must log at error level, got: %s", logged)
	}
	if !strings.Contains(logged, "embed binding changed") {
		t.Fatalf("expected the loud \"embed binding changed\" message, got: %s", logged)
	}
	const configuredIdentity = "fake/fake-embed-new@1024"
	if !strings.Contains(logged, configuredIdentity) {
		t.Fatalf("expected the configured identity %q in the log line, got: %s", configuredIdentity, logged)
	}
	if !strings.Contains(logged, priorIdentity) {
		t.Fatalf("expected the populated identity %q in the log line, got: %s", priorIdentity, logged)
	}
}

// TestNewModelPathUnboundEmbedLaneSkipsSeed proves the AI-unconfigured
// carve-out: a routing config whose embeddings lane never got a model
// (EmbedIdentity() == "") must construct cleanly WITHOUT seeding the
// binding marker at all — this is a legitimate deployment shape
// (--ai-fake/no-embeddings), not an error, and there is no live identity
// to seed the marker with.
func TestNewModelPathUnboundEmbedLaneSkipsSeed(t *testing.T) {
	e := Setup(t)
	cfg := unboundEmbedRoutingConfig()
	log, buf := bufferLogger()

	if _, err := compose.NewModelPath(cfg, e.Pool, false, log); err != nil {
		t.Fatalf("NewModelPath must not fail on an unbound embed lane: %v", err)
	}

	store := search.NewStore(e.Pool)
	if _, _, _, err := store.PopulatedIdentity(context.Background()); err == nil {
		t.Fatal("the binding marker must NOT exist — an unbound embed lane must skip the seed, not plant an empty identity")
	}

	if strings.Contains(buf.String(), "embed binding changed") {
		t.Fatalf("an unbound embed lane must never log a binding change, got: %s", buf.String())
	}
}

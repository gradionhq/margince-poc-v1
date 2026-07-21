// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// resolveModelPath is the ONE place the api process decides what serves
// its AI surfaces: a declared ai-routing.yaml, the offline fake behind
// --ai-fake, or neither — a single three-way switch run() runs exactly
// once. coldStartOptions, offerDraftOptions and /readyz's AI line all
// consume the one *compose.ModelPath (and the state string) this
// returns, so the process holds one Router, one cache, one budget —
// never a doubled pair from two callers each resolving their own.
//
// The --ai-fake arm builds a real compose.ModelPath over
// ai.FakeRoutingConfig() rather than compose.FakeModelPath's direct
// client wiring: the api always has a pool, so --ai-fake safely rides
// the real Router (tiering, budget guardrail, metering, call tracing)
// with only the provider swapped for the deterministic fake — dev/test
// exercises the same wiring production does, not a bypass of it.
//
// A nil path (the neither-flag case) is not a boot error: an
// AI-unconfigured deployment is a legitimate, ready one (aistate.go);
// coldStartOptions/offerDraftOptions/writeAIMetrics all treat nil as
// "this role wires no AI surfaces" rather than panicking.
func resolveModelPath(routingPath string, fakeBrain bool, pool *pgxpool.Pool, capturePayloads bool, log *slog.Logger) (*compose.ModelPath, string, ai.PublicProfile, error) {
	switch {
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return nil, "", ai.PublicProfile{}, err
		}
		// A task whose whole fallback ladder has no bound tier is not a
		// boot error (a deployment may legitimately not run every
		// workload), but it must be loud: log it now, not discover it
		// from a refused call.
		for _, w := range cfg.UnboundLadderWarnings() {
			log.Warn(w)
		}
		modelPath, err := compose.NewModelPath(cfg, pool, capturePayloads, log)
		if err != nil {
			return nil, "", ai.PublicProfile{}, err
		}
		return &modelPath, compose.AIStateConfigured, ai.NewPublicProfile(compose.AIStateConfigured, cfg), nil
	case fakeBrain:
		cfg := ai.FakeRoutingConfig()
		modelPath, err := compose.NewModelPath(cfg, pool, capturePayloads, log)
		if err != nil {
			return nil, "", ai.PublicProfile{}, err
		}
		return &modelPath, compose.AIStateFake, ai.NewPublicProfile(compose.AIStateFake, cfg), nil
	default:
		return nil, compose.AIStateUnconfigured, ai.NewPublicProfile(compose.AIStateUnconfigured, ai.RoutingConfig{}), nil
	}
}

// coldStartOptions wires the cold-start read-back's model surface over
// an already-resolved model path: a real deployment or --ai-fake lights
// it up, no path leaves the operation an explicit 501 (same posture as
// the worker's runner lane).
func coldStartOptions(modelPath *compose.ModelPath) []compose.Option {
	if modelPath == nil {
		return nil
	}
	// The read-back and per-org enrichment share the fetch + extraction
	// seam, so both light up together on the one resolved model path;
	// the Morning-Brief L2 re-order rides its own routed lane.
	fetch := compose.NewWebFetcher()
	return []compose.Option{
		compose.WithColdStart(fetch, modelPath.ColdStart),
		compose.WithScrape(fetch, modelPath.ColdStart),
		compose.WithBrief(modelPath.BriefRank),
		compose.WithReplyDraft(modelPath.DraftReply),
	}
}

// offerDraftOptions wires the AI-drafted offer regeneration's model +
// retrieval surface (arc 4b) over the SAME resolved model path
// coldStartOptions consumes — a role that lights up one AI surface
// lights up both rather than growing a second resolution for what is,
// at boot time, one decision ("does this role have a model?"). Absent a
// path, regenerateOffer stays the mechanical clone alone (offerregenerate.go's
// honest "offerDrafter unwired" path) — never a silently different behavior.
func offerDraftOptions(pool *pgxpool.Pool, modelPath *compose.ModelPath) []compose.Option {
	if modelPath == nil {
		return nil
	}
	retriever := search.NewRetriever(search.NewStore(pool), modelPath.Embedder)
	return []compose.Option{compose.WithOfferDraft(modelPath.OfferDraft, retriever)}
}

// deepReadOption wires the deep-read transport over an insert-only River
// client: the api enqueues the crawl for the worker role, it never works
// jobs (jobs.NewInserter documents that Start is never called on it).
func deepReadOption(pool *pgxpool.Pool, logger *slog.Logger) (compose.Option, error) {
	inserter, err := jobs.NewInserter(pool, logger)
	if err != nil {
		return nil, err
	}
	return compose.WithDeepRead(inserter), nil
}

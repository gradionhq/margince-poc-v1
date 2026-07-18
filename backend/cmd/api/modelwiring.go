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

// resolvedModelPath loads ai-routing.yaml and builds the process's
// ModelPath over it — coldStartOptions and offerDraftOptions each call
// this on the declared routing file so both stay a plain mirror of the
// same three-way switch (declared routing / --ai-fake / neither) rather
// than threading a pre-built ModelPath through run() as a fourth kind of
// boot parameter.
func resolvedModelPath(routingPath string, pool *pgxpool.Pool, capturePayloads bool, log *slog.Logger) (compose.ModelPath, error) {
	cfg, err := ai.LoadRoutingFile(routingPath)
	if err != nil {
		return compose.ModelPath{}, err
	}
	// A task whose whole fallback ladder has no bound tier is not a boot
	// error (a deployment may legitimately not run every workload), but
	// it must be loud: log it now, not discover it from a refused call.
	for _, w := range cfg.UnboundLadderWarnings() {
		log.Warn(w)
	}
	return compose.NewModelPath(cfg, pool, capturePayloads, log)
}

// aiState reports /readyz's AI visibility line — the same declared-
// routing/--ai-fake/neither switch resolvedModelPath's callers each run,
// collapsed to the one string an operator reads off the probe.
func aiState(routingPath string, fakeBrain bool) string {
	switch {
	case routingPath != "":
		return compose.AIStateConfigured
	case fakeBrain:
		return compose.AIStateFake
	default:
		return compose.AIStateUnconfigured
	}
}

// coldStartOptions resolves the cold-start read-back's model wiring: a
// declared routing file for real deployments, the offline fake behind an
// explicit dev flag, or nothing — the operation then stays an explicit
// 501 (same posture as the worker's runner lane).
func coldStartOptions(routingPath string, fakeBrain bool, pool *pgxpool.Pool, capturePayloads bool, log *slog.Logger) ([]compose.Option, error) {
	switch {
	case routingPath != "":
		modelPath, err := resolvedModelPath(routingPath, pool, capturePayloads, log)
		if err != nil {
			return nil, err
		}
		// The read-back and per-org enrichment share the fetch + extraction
		// seam, so both light up together on the one declared model path;
		// the Morning-Brief L2 re-order rides its own routed lane.
		fetch := compose.NewWebFetcher()
		return []compose.Option{
			compose.WithColdStart(fetch, modelPath.ColdStart),
			compose.WithScrape(fetch, modelPath.ColdStart),
			compose.WithBrief(modelPath.BriefRank),
			compose.WithAIMetrics(modelPath.WriteMetrics),
		}, nil
	case fakeBrain:
		fetch := compose.NewWebFetcher()
		fake := ai.NewFakeClient()
		return []compose.Option{
			compose.WithColdStart(fetch, fake),
			compose.WithScrape(fetch, fake),
			compose.WithBrief(fake),
		}, nil
	default:
		return nil, nil
	}
}

// offerDraftOptions resolves the AI-drafted offer regeneration's model +
// retrieval wiring (arc 4b): the SAME declared-routing/--ai-fake/absent
// three-way switch coldStartOptions runs, over the SAME two flags — a
// role that lights up one AI surface lights up both rather than growing
// a second pair of flags for what is, at boot time, one decision ("does
// this role have a model?"). Absent either, regenerateOffer stays the
// mechanical clone alone (offerregenerate.go's honest "offerDrafter
// unwired" path) — never a silently different behavior.
func offerDraftOptions(routingPath string, fakeBrain bool, pool *pgxpool.Pool, capturePayloads bool, log *slog.Logger) ([]compose.Option, error) {
	switch {
	case routingPath != "":
		modelPath, err := resolvedModelPath(routingPath, pool, capturePayloads, log)
		if err != nil {
			return nil, err
		}
		retriever := search.NewRetriever(search.NewStore(pool), modelPath.Embedder)
		return []compose.Option{
			compose.WithOfferDraft(modelPath.OfferDraft, retriever),
			compose.WithAIMetrics(modelPath.WriteMetrics),
		}, nil
	case fakeBrain:
		fake := ai.NewFakeClient()
		retriever := search.NewRetriever(search.NewStore(pool), nil)
		return []compose.Option{compose.WithOfferDraft(fake, retriever)}, nil
	default:
		return nil, nil
	}
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

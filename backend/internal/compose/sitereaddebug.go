// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's DB-less debug facade: the worker's crawl→extract→merge
// spine run in memory for the `worker siteread` subcommand, with every
// intermediate the production path keeps to itself — per-page findings,
// merge decisions, model-call telemetry — surfaced in one report. No
// dossier, no staging, no approvals: the report ends where stage()
// would begin, carrying the exact proposal payload staging would
// marshal. Tuning extraction quality needs this visibility; the SPA's
// dossier only shows what SURVIVED.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// SiteReadDebugOptions configures one debug read. Brain is
// caller-selected (a routed config, a direct model override, or the
// offline fake) — the facade never picks a model itself.
type SiteReadDebugOptions struct {
	SeedURL string
	Caps    CrawlCaps
	Brain   runner.Brain
	// IncludePageText carries each fetched page's reduced text into the
	// report (DebugPage.Text) — for the --dump-pages flag; off by default
	// because page text dwarfs everything else in the JSON.
	IncludePageText bool
	// Progress (may be nil) fires at phase boundaries — after the crawl
	// and after each corpus chunk — the CLI's live status line.
	Progress func(phase string, done, total int)
}

// SiteReadDebugReport is the whole run, machine-readable. Arrays follow
// the deterministic crawl/merge order, so two runs of the same site
// diff cleanly field by field.
type SiteReadDebugReport struct {
	SeedURL    string                   `json:"seed_url"`
	Caps       DebugCaps                `json:"caps"`
	Crawl      DebugCrawl               `json:"crawl"`
	Extraction DebugExtraction          `json:"extraction"`
	ModelCalls []DebugModelCall         `json:"model_calls"`
	Proposal   *people.DeepReadProposal `json:"proposal"`
	// ModelLaneError mirrors the worker's degraded-to-partial path: the
	// extraction error that stopped the model lane midway, empty when
	// every page got its passes.
	ModelLaneError string `json:"model_lane_error,omitempty"`
	// Warnings are debug-only quality signals (legal-page conflicts, a
	// legal name foreign to the domain) — advice for the human tuning
	// the read, never part of the production outcome.
	Warnings []string `json:"warnings,omitempty"`
}

// RunSiteReadDebug runs one full deep read in memory and reports every
// intermediate. Only a failed seed page fails the run — like the worker,
// a midway model-lane death degrades to a partial report.
func RunSiteReadDebug(ctx context.Context, opts SiteReadDebugOptions) (SiteReadDebugReport, error) {
	fetcher := webread.New()
	return siteReadDebugRun(ctx, opts, newSiteCrawler(fetcher, opts.Caps), fetcher)
}

// siteReadDebugRun is the seam unit tests drive with an in-memory site;
// production enters through RunSiteReadDebug's real fetcher.
func siteReadDebugRun(ctx context.Context, opts SiteReadDebugOptions, crawler *siteCrawler, pageFetch PageFetcher) (SiteReadDebugReport, error) {
	if opts.Brain == nil {
		return SiteReadDebugReport{}, fmt.Errorf("siteread debug: no brain configured")
	}
	if _, ok := principal.WorkspaceID(ctx); !ok {
		// The router meters per workspace; a DB-less run has none, so it
		// gets a synthetic one for the life of the process.
		ctx = principal.WithWorkspaceID(ctx, ids.NewV7())
	}
	caps := opts.Caps.withDefaults()
	rec := &recordingBrain{inner: opts.Brain}
	var dropped []DebugDrop
	extract := evidenceExtractor{fetch: pageFetch, brain: rec, drops: func(sourceURL string, d droppedFinding) {
		dropped = append(dropped, DebugDrop{
			PageURL: sourceURL, Lane: d.Lane, Field: d.Field, Value: d.Value,
			EvidenceSnippet: d.EvidenceSnippet, Reason: d.Reason,
		})
	}}

	report := SiteReadDebugReport{
		SeedURL: opts.SeedURL,
		Caps:    DebugCaps{MaxPages: caps.MaxPages, MaxBytes: caps.MaxBytes, WallMs: caps.Wall.Milliseconds()},
	}

	crawlStart := time.Now()
	crawl, err := crawler.Crawl(ctx, opts.SeedURL)
	if err != nil {
		return SiteReadDebugReport{}, err
	}
	crawlMs := time.Since(crawlStart).Milliseconds()
	if opts.Progress != nil {
		opts.Progress("crawled", len(crawl.Pages), len(crawl.Pages))
	}

	chunks := buildCorpusChunks(crawl.Pages, corpusBudgetRunes)
	results, extractedPages, modelErr := extractCorpusChunks(ctx, extract, opts.SeedURL, chunks, func(done, total int) {
		if opts.Progress != nil {
			opts.Progress("extracted chunk", done, total)
		}
	})
	if modelErr != nil {
		report.ModelLaneError = modelErr.Error()
	}
	report.Crawl = debugCrawl(crawl, extractedPages, opts.IncludePageText, crawlMs)

	merged := mergeChunkResults(results)
	idx := indexCorpusPages(corpusChunk{pages: extractedPages})
	mergedFields, legalConflict, legalDrops := applyLegalGate(merged, idx)
	extract.reportDrops(ctx, laneLegal, legalDrops)
	if legalConflict {
		report.Warnings = append(report.Warnings, legalWarningMultipleEntities)
	}
	report.Extraction = DebugExtraction{
		Fields:        debugFields(mergedFields),
		Facts:         debugFacts(merged.facts),
		People:        debugPeople(merged.people),
		LegalEntities: debugLegalEntities(merged.legalEntities),
		Dropped:       dropped,
	}
	report.ModelCalls = rec.calls
	report.Proposal = debugProposal(opts.SeedURL, mergedFields, merged.facts)
	if warning := wrongCompanySignal(opts.SeedURL, mergedFields); warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}
	return report, nil
}

// recordingBrain decorates the injected brain with per-call telemetry
// for the debug report. The debug loop is sequential, so the mutable
// page label is safe; production never sees this type.
type recordingBrain struct {
	inner runner.Brain
	page  string
	calls []DebugModelCall
}

func (b *recordingBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	start := time.Now()
	resp, err := b.inner.Complete(ctx, req)
	b.record(req, resp, err, time.Since(start))
	return resp, err
}

// CompleteValidated keeps the structured-output pipeline reachable
// through the decorator: without it the extractor's validatedBrain
// type-assert would miss and silently downgrade every call.
func (b *recordingBrain) CompleteValidated(ctx context.Context, req model.Request, validate ai.Validator) (model.Response, error) {
	structured, ok := b.inner.(validatedBrain)
	if !ok {
		return b.Complete(ctx, req)
	}
	start := time.Now()
	resp, err := structured.CompleteValidated(ctx, req, validate)
	b.record(req, resp, err, time.Since(start))
	return resp, err
}

func (b *recordingBrain) record(req model.Request, resp model.Response, err error, dur time.Duration) {
	call := DebugModelCall{
		PageURL:      b.page,
		Lane:         extractionLane(req.System),
		LatencyMs:    dur.Milliseconds(),
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
	}
	if err != nil {
		call.Error = err.Error()
	}
	b.calls = append(b.calls, call)
}

// SiteReadDebugBrain resolves the subcommand's model selection — exactly
// one of a routing file, a direct provider:model override, or the
// offline fake — into a Brain plus a banner naming what will serve the
// calls. The override builds a one-tier routing config in process, so
// even a pinned model rides the full routed pipeline (structured-output
// retries, budget bands, secret stripping).
//
//nolint:ireturn // the Brain seam is the point: three providers (routed, override, fake) behind the one interface every consumer takes.
func SiteReadDebugBrain(routingPath, modelOverride string, fake bool) (runner.Brain, string, error) {
	selected := 0
	for _, on := range []bool{routingPath != "", modelOverride != "", fake} {
		if on {
			selected++
		}
	}
	if selected != 1 {
		return nil, "", fmt.Errorf("pick exactly one of --ai-routing, --model, --ai-fake")
	}
	switch {
	case fake:
		return ai.NewFakeClient(), "fake (offline; extraction yields nothing — crawl dry-run)", nil
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return nil, "", err
		}
		router, err := ai.NewUnmeteredRouter(cfg)
		if err != nil {
			return nil, "", err
		}
		return routerBrain{router: router, task: ai.TaskSiteExtract}, "routing " + routingPath, nil
	default:
		provider, modelName, found := strings.Cut(modelOverride, ":")
		if !found || provider == "" || modelName == "" {
			return nil, "", fmt.Errorf("--model wants provider:model (e.g. anthropic:claude-sonnet-4-6), got %q", modelOverride)
		}
		router, err := ai.NewUnmeteredRouter(ai.RoutingConfig{
			Profile:    ai.ProfileCloudFrontier,
			Tiers:      map[ai.Tier]ai.ProviderConfig{ai.TierCheapCloud: {Provider: provider, Model: modelName}},
			Embeddings: ai.ProviderConfig{Provider: "fake"},
		})
		if err != nil {
			return nil, "", err
		}
		return routerBrain{router: router, task: ai.TaskSiteExtract}, "model override " + modelOverride, nil
	}
}

// extractionLane names which extraction a call served, recovered from
// the system prompt — the corpus call is the deep read's one lane; the
// per-page company-fact prompt still serves the quick scrape.
func extractionLane(system string) string {
	switch system {
	case corpusSystem:
		return laneCorpus
	case companyFactsSystem:
		return laneFields
	default:
		return "other"
	}
}

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
	"sync"
	"time"

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
	Brain   completer
	// FactBrain serves the page-parallel fact lane (nil ⇒ Brain).
	FactBrain completer
	// TriageBrain serves the domain-triage classification (nil ⇒ FactBrain,
	// then Brain). Its own lane on purpose: the classifier rides a cheap-first
	// ladder in production, and tuning its prompt against the profile lane's
	// premium-only binding would tune it against a model that never runs it.
	TriageBrain completer
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
	// Logo is what the visual-identity lane made of the seed page's
	// declarations. The debug run resolves and normalizes exactly as the
	// worker does but stores nothing — it is DB-less and blob-less.
	Logo DebugLogo `json:"logo"`
	// ModelLaneError mirrors the worker's degraded-to-partial path: the
	// extraction error that stopped the model lane midway, empty when
	// every page got its passes.
	ModelLaneError string `json:"model_lane_error,omitempty"`
	// Warnings are debug-only quality signals (legal-page conflicts, a
	// legal name foreign to the domain) — advice for the human tuning
	// the read, never part of the production outcome.
	Warnings []string `json:"warnings,omitempty"`
	// ExtractionDurationMs is the parallel extraction's wall clock —
	// with the crawl duration, the read's whole latency story.
	ExtractionDurationMs int64 `json:"extraction_duration_ms"`
	// Triage is what the domain-triage classifier made of the landing page.
	// It is reported for EVERY debug run, including seeds that would never be
	// triaged in production, because tuning that prompt is the whole reason to
	// point this tool at a personal domain or a mailbox vendor.
	Triage DebugTriage `json:"triage"`
}

// DebugTriage is the seed-page classification and what it would have cost.
type DebugTriage struct {
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	// Aborts reports whether this verdict would have stopped the crawl after
	// the landing page — the saving the classifier exists to buy.
	Aborts bool `json:"aborts"`
	// Error is the classification call's own failure, if it had one. A failed
	// classification is not a failed read: production falls through to the
	// full crawl, and so does this.
	Error string `json:"error,omitempty"`
}

// RunSiteReadDebug runs one full deep read in memory and reports every
// intermediate. Only a failed seed page fails the run — like the worker,
// a midway model-lane death degrades to a partial report.
func RunSiteReadDebug(ctx context.Context, opts SiteReadDebugOptions) (SiteReadDebugReport, error) {
	fetcher := webread.New()
	return siteReadDebugRun(ctx, opts, newSiteCrawler(fetcher, opts.Caps), fetcher, fetcher)
}

// siteReadDebugRun is the seam unit tests drive with an in-memory site;
// production enters through RunSiteReadDebug's real fetcher. A nil pageFetch
// or logoFetch leaves that lane out of the report, the way a test that is
// asserting about the model lanes does.
func siteReadDebugRun(ctx context.Context, opts SiteReadDebugOptions, crawler *siteCrawler, pageFetch PageFetcher, logoFetch assetFetcher) (SiteReadDebugReport, error) {
	if opts.Brain == nil {
		return SiteReadDebugReport{}, fmt.Errorf("siteread debug: no brain configured")
	}
	if _, ok := principal.WorkspaceID(ctx); !ok {
		// The router meters per workspace; a DB-less run has none, so it
		// gets a synthetic one for the life of the process.
		ctx = principal.WithWorkspaceID(ctx, ids.NewV7())
	}
	caps := opts.Caps.withDefaults()
	log := &callLog{}
	rec := &recordingBrain{inner: opts.Brain, log: log}
	factInner := opts.FactBrain
	if factInner == nil {
		factInner = opts.Brain
	}
	recFacts := &recordingBrain{inner: factInner, log: log}
	triageInner := opts.TriageBrain
	if triageInner == nil {
		triageInner = factInner
	}
	recTriage := &recordingBrain{inner: triageInner, log: log}
	var dropped []DebugDrop
	extract := evidenceExtractor{fetch: pageFetch, brain: rec, factBrain: recFacts, drops: func(sourceURL string, d droppedFinding) {
		dropped = append(dropped, DebugDrop{
			PageURL: sourceURL, Lane: d.Lane, Field: d.Field, Value: d.Value,
			EvidenceSnippet: d.EvidenceSnippet, Reason: d.Reason,
		})
	}}

	report := SiteReadDebugReport{
		SeedURL: opts.SeedURL,
		Caps:    DebugCaps{MaxPages: caps.MaxPages, MaxBytes: caps.MaxBytes, WallMs: caps.Wall.Milliseconds()},
	}

	start := time.Now()
	crawl, extraction, err := crawlAndExtract(ctx, crawler, extract, opts.SeedURL, func(phase string, pages []crawlPage) {
		if opts.Progress != nil {
			// The total is unknowable mid-crawl (pages stream in); done
			// alone is the honest signal.
			done := len(pages)
			opts.Progress(phase, done, done)
		}
	}, nil)
	if err != nil {
		return SiteReadDebugReport{}, err
	}
	// Crawl and extraction overlap: ExtractionDurationMs is the whole
	// overlapped run, Crawl.DurationMs the crawl's own share within it —
	// they no longer sum.
	report.ExtractionDurationMs = time.Since(start).Milliseconds()
	crawlMs := extraction.crawlMs
	if extraction.err != nil {
		report.ModelLaneError = extraction.err.Error()
	}
	report.Crawl = debugCrawl(crawl, crawl.Pages, opts.IncludePageText, crawlMs)
	if len(crawl.Pages) > 0 {
		report.Triage = debugTriage(ctx, recTriage, crawl.Pages[0])
	}
	if logoFetch != nil {
		logoSeed := crawl.SeedURL
		if logoSeed == "" {
			logoSeed = opts.SeedURL
		}
		report.Logo = debugLogo(resolveOrganizationLogo(ctx, logoFetch, logoSeed, crawl.SeedAssets))
	}

	mergedFields, legalConflict, legalDrops := applyLegalGate(extraction.fields, extraction.merged.entities, pageKindsOf(crawl.Pages), extraction.legalCensusIncomplete)
	extraction.merged.entities = enrichLegalEntitiesFromProfile(extraction.merged.entities, mergedFields)
	extract.reportDrops(ctx, laneLegal, legalDrops)
	if legalConflict {
		report.Warnings = append(report.Warnings, legalWarningMultipleEntities)
	}
	report.Extraction = DebugExtraction{
		Fields:        debugFields(mergedFields),
		Facts:         debugFacts(extraction.merged.facts),
		People:        debugPeople(extraction.merged.people),
		LegalEntities: debugLegalEntities(extraction.merged.entities),
		Dropped:       dropped,
	}
	report.ModelCalls = log.calls
	report.Proposal = debugProposal(opts.SeedURL, mergedFields, extraction.merged.facts)
	if warning := wrongCompanySignal(opts.SeedURL, mergedFields); warning != "" {
		report.Warnings = append(report.Warnings, warning)
	}
	return report, nil
}

// debugTriage runs the domain-triage classifier over the landing page the crawl
// already read. It classifies AFTER the fact here rather than before, because a
// debug run wants the whole read regardless of the verdict — what it reports is
// what production WOULD have decided.
func debugTriage(ctx context.Context, brain completer, seed crawlPage) DebugTriage {
	req := triageRequest(seed)
	var resp model.Response
	var err error
	// The same validated call classifySeed makes. Without it a malformed reply
	// is retried in production and downgraded to `unclear` here, so the report
	// would show a verdict production never reached.
	if structured, ok := brain.(validatedBrain); ok {
		resp, err = structured.CompleteValidated(ctx, req, triageShapeValid)
	} else {
		resp, err = brain.Complete(ctx, req)
	}
	if err != nil {
		return DebugTriage{Kind: siteKindUnclear, Error: err.Error()}
	}
	verdict := gateTriageVerdict(resp.Text)
	return DebugTriage{
		Kind:       verdict.Kind,
		Confidence: float64(verdict.Confidence),
		Reason:     verdict.Reason,
		Aborts:     verdict.Aborts(),
	}
}

// recordingBrain decorates the injected brain with per-call telemetry
// for the debug report. Calls arrive from the concurrent fan-out, so
// the record is mutex-guarded and the page attribution is recovered
// from the request itself; production never sees this type.
type recordingBrain struct {
	inner completer
	log   *callLog
}

// callLog is the shared, mutex-guarded call record both lane recorders
// append to.
type callLog struct {
	mu    sync.Mutex
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
	lane := extractionLane(req.System)
	page := pageOfRequest(req)
	if page == "" {
		page = lane // the profile call reads the whole excerpt corpus
	}
	call := DebugModelCall{
		PageURL:      page,
		Lane:         lane,
		LatencyMs:    dur.Milliseconds(),
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
	}
	if err != nil {
		call.Error = err.Error()
	}
	b.log.mu.Lock()
	defer b.log.mu.Unlock()
	b.log.calls = append(b.log.calls, call)
}

// pageOfRequest recovers which page a call served from the request's
// own source label ("Page <url>:") — attribution that survives the
// concurrent fan-out, where a mutable shared label would not.
func pageOfRequest(req model.Request) string {
	if len(req.Messages) == 0 {
		return ""
	}
	rest, found := strings.CutPrefix(req.Messages[0].Content, "Page ")
	if !found {
		return ""
	}
	pageURL, _, found := strings.Cut(rest, ":\n")
	if !found {
		return ""
	}
	return pageURL
}

// SiteReadDebugBrain resolves the subcommand's model selection — exactly
// one of a routing file, a direct provider:model override, or the
// offline fake — into a Brain plus a banner naming what will serve the
// calls. The override builds a one-tier routing config in process, so
// even a pinned model rides the full routed pipeline (structured-output
// retries, budget bands, secret stripping).
//
//nolint:ireturn // the completer seam is the point: three providers (routed, override, fake) behind the one interface every consumer takes.
func SiteReadDebugBrain(routingPath, modelOverride string, fake bool) (profile, facts, triage completer, banner string, err error) {
	selected := 0
	for _, on := range []bool{routingPath != "", modelOverride != "", fake} {
		if on {
			selected++
		}
	}
	if selected != 1 {
		return nil, nil, nil, "", fmt.Errorf("pick exactly one of --ai-routing, --model, --ai-fake")
	}
	switch {
	case fake:
		client := ai.NewFakeClient()
		return client, client, client, "fake (offline; extraction yields nothing — crawl dry-run)", nil
	case routingPath != "":
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return nil, nil, nil, "", err
		}
		router, err := ai.NewLocalRouter(cfg)
		if err != nil {
			return nil, nil, nil, "", err
		}
		return routerBrain{router: router, task: ai.TaskSiteExtract},
			routerBrain{router: router, task: ai.TaskSiteFactExtract},
			routerBrain{router: router, task: ai.TaskSiteTriage},
			"routing " + routingPath, nil
	default:
		provider, modelName, found := strings.Cut(modelOverride, ":")
		if !found || provider == "" || modelName == "" {
			return nil, nil, nil, "", fmt.Errorf("--model wants provider:model (e.g. anthropic:claude-sonnet-4-6), got %q", modelOverride)
		}
		router, err := ai.NewLocalRouter(ai.RoutingConfig{
			Profile:    ai.ProfileCloudFrontier,
			Tiers:      map[ai.Tier]ai.ProviderConfig{ai.TierCheapCloud: {Provider: provider, Model: modelName}},
			Embeddings: ai.EmbeddingsConfig{ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake}},
		})
		if err != nil {
			return nil, nil, nil, "", err
		}
		// One pinned model serves every lane: each task's ladder falls
		// through to the one bound tier.
		lane := func(task ai.Task) completer { return routerBrain{router: router, task: task} }
		return lane(ai.TaskSiteExtract), lane(ai.TaskSiteFactExtract), lane(ai.TaskSiteTriage),
			"model override " + modelOverride, nil
	}
}

// extractionLane names which extraction a call served, recovered from
// the system prompt: the profile lane and the per-page fact lane are
// the deep read's two prompts; the company-fact prompt still serves the
// quick scrape. Each lane's prompt ends with that call's own fence rule,
// which names a per-call nonce, so the lane is recognised by its stable
// PREFIX — an equality test would match nothing.
func extractionLane(system string) string {
	switch {
	case strings.HasPrefix(system, triageSystem):
		return laneTriage
	case strings.HasPrefix(system, profileSystem):
		return laneProfile
	case strings.HasPrefix(system, "You extract company facts from ONE page"):
		return lanePageFacts
	case strings.HasPrefix(system, companyFactsSystem):
		return laneFields
	default:
		return "other"
	}
}

// TaskProbeCompleter is a debug-lane completer that also reports the route that
// served each call. The plain completer seam deliberately hides routing — a
// case must not be able to reason about which tier answered it — but a probe's
// whole job is to REPORT what happened, and "which model actually served this"
// is the first thing a surprising answer is explained by.
type TaskProbeCompleter func(ctx context.Context, req model.Request) (model.Response, ai.RouteInfo, error)

// TaskProbeBrain binds one task to the model that will answer it for the
// `worker aitask` probe, under the same one-of-three rule SiteReadDebugBrain
// uses: a routing file, a direct provider:model override, or the offline fake.
//
// It lives HERE, beside SiteReadDebugBrain, because this file is one of the two
// files that ARE the model-path assembly seam (backend/arch_test.go's
// modelPathAssemblySeam). A probe that built its own router in cmd/ would be a
// third gate, and the invariant is that there are exactly two.
//
// DB-less like its sibling: the probe has no pool, so this is the local router
// — no metering, no budget store, no call tracing. That is what makes a probe
// free to run and is also why it is not a production path.
func TaskProbeBrain(routingPath, modelSpec string, fake bool, task ai.Task) (TaskProbeCompleter, string, error) {
	selected := 0
	for _, on := range []bool{routingPath != "", modelSpec != "", fake} {
		if on {
			selected++
		}
	}
	if selected != 1 {
		return nil, "", fmt.Errorf("pick exactly one of --ai-routing, --model, --ai-fake")
	}

	if fake {
		client := ai.NewFakeClient()
		return func(ctx context.Context, req model.Request) (model.Response, ai.RouteInfo, error) {
			resp, err := client.Complete(ctx, req)
			return resp, ai.RouteInfo{Provider: string(ai.ProviderFake)}, err
		}, "fake (offline; the seam is driven, nothing is spent)", nil
	}

	cfg, banner, err := taskProbeRouting(routingPath, modelSpec)
	if err != nil {
		return nil, "", err
	}
	// The result cache is disabled for the same reason the certification lane
	// disables it: a probe exists to report what a call DID, and a
	// cache-served repeat would report a call that never happened.
	router, err := ai.NewLocalRouter(cfg, ai.WithoutResultCache())
	if err != nil {
		return nil, "", err
	}
	return func(ctx context.Context, req model.Request) (model.Response, ai.RouteInfo, error) {
		return router.Complete(ctx, task, req)
	}, banner, nil
}

func taskProbeRouting(routingPath, modelSpec string) (ai.RoutingConfig, string, error) {
	if routingPath != "" {
		cfg, err := ai.LoadRoutingFile(routingPath)
		if err != nil {
			return ai.RoutingConfig{}, "", err
		}
		return cfg, "routing " + routingPath, nil
	}
	provider, modelName, found := strings.Cut(modelSpec, ":")
	if !found || provider == "" || modelName == "" {
		return ai.RoutingConfig{}, "", fmt.Errorf("--model wants provider:model (e.g. anthropic:claude-sonnet-4-6), got %q", modelSpec)
	}
	// One pinned model serves every lane: each task's ladder falls through to
	// the single bound tier.
	return ai.RoutingConfig{
		Profile:    ai.ProfileCloudFrontier,
		Tiers:      map[ai.Tier]ai.ProviderConfig{ai.TierCheapCloud: {Provider: provider, Model: modelName}},
		Embeddings: ai.EmbeddingsConfig{ProviderConfig: ai.ProviderConfig{Provider: ai.ProviderFake}},
	}, "model override " + modelSpec, nil
}

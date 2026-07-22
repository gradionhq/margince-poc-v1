// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The model path, assembled over the ai module's tiered router so every
// model consumer — the Surface-B runner, the retrieval embed lane, the
// cold-start read-back — rides routing policy, the budget guardrail,
// metering and secret-stripping through ONE router. Consumers only ever
// see the narrow Brain seam.

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// completer is the 2-return completion seam the direct-call lanes
// (cold-start read-back, site extraction, brief re-rank, offer drafting)
// consume: they call the model once and use only its Response, never the
// runner's per-step Meta. Both routerBrain and the offline *ai.FakeClient
// satisfy it, so the fake path needs no adapter on these lanes. Only the
// Surface-B runner lane (Agent) needs runner.Brain's Meta return.
type completer interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// ModelPath is the wired model surface one process role hands around:
// each lane is the same router under a task label, so ai-routing.yaml
// decides the tier per workload, and every lane draws on the
// seat-derived monthly budget.
type ModelPath struct {
	Agent           runner.Brain // the Surface-B reason-act loop (records served model identity)
	ColdStart       completer    // the website read-back extraction
	SiteExtract     completer    // the deep read's profile lane (one premium-first call)
	SiteFactExtract completer    // the deep read's page-parallel fact lane (fast tier)
	BriefRank       completer    // the Morning-Brief L2 re-order (B-E05.2)
	DraftReply      completer    // activity-anchored email reply drafting
	OfferDraft      completer    // the offer regenerate-from-signal drafting call
	// CaptureClassify is the §2.8 batched mail-label lane (ADR-0063) —
	// the highest-volume, cheapest task, routed L-S with the C-C solo
	// re-ask riding the same ladder.
	CaptureClassify completer
	// SignatureEnrich is the §2.9 evidence-or-omit field extraction lane.
	SignatureEnrich completer
	// VoiceBuild is the durable Voice DNA build lane: the builder pass and
	// its evaluation drafts ride the same task label and budget.
	VoiceBuild completer
	Embedder   search.Embedder // the retrieval embed lane
}

// SetCompanyContextEnabled applies the operator's ordered task-rollout stage
// to every lane sharing this path. Task policy remains exhaustive even while
// injection is disabled.
func (p *ModelPath) SetCompanyContextEnabled(enabled bool) {
	if p == nil {
		return
	}
	if brain, ok := p.Agent.(agentBrain); ok && brain.companyContext != nil {
		brain.companyContext.enabled = enabled
	}
}

// NewModelPath builds the production model path from a validated
// routing config. capturePayloads and log ride straight into the
// router's ai_call tracing (ai.NewRouter) — the deployment's
// AI.CapturePayloads posture and the process logger, never a stand-in.
func NewModelPath(cfg ai.RoutingConfig, pool *pgxpool.Pool, capturePayloads bool, log *slog.Logger) (ModelPath, error) {
	router, err := ai.NewRouter(cfg, ai.NewMeter(pool), NewSeatBudget(pool), ai.NewCallMeter(pool), capturePayloads, log)
	if err != nil {
		return ModelPath{}, err
	}
	if err := seedEmbedBinding(context.Background(), search.NewStore(pool), router, log); err != nil {
		return ModelPath{}, err
	}
	return modelPathForRouter(router, newCompanyContextProvider(people.NewStore(pool))), nil
}

// seedEmbedBinding plants search's embed_store_binding marker (Task 9's
// Store.SeedBinding) at the router's configured embed identity — both
// process roles (api, worker) construct a ModelPath, so this runs on
// every boot, and SeedBinding's ON CONFLICT DO NOTHING makes that
// idempotent rather than a race.
//
// An unbound embed lane (EmbedIdentity() == "") is a legitimate
// AI-unconfigured deployment shape (--ai-fake or any routing config that
// never bound an embeddings model) — there is no live identity to plant,
// so this is a no-op, never an error.
//
// A genuine store failure (SeedBinding or PopulatedIdentity) is a real DB
// fault surfacing right after the pool connected — it aborts boot through
// NewModelPath rather than launching a process whose embed marker is
// unestablished or unverified.
//
// A store already populated under a DIFFERENT identity is NOT a fault: it
// means an operator changed the embed binding since the marker was last
// seeded. The store still serves reads correctly under its existing
// populated identity (the N+1 read path tolerates a stale binding);
// reindexing onto the new one is a deliberate ops action, not something
// boot should force. So that case logs LOUDLY at error level — an admin
// must see it — and construction still succeeds.
func seedEmbedBinding(ctx context.Context, store *search.Store, router *ai.Router, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	identity, _ := router.EmbedIdentity()
	if identity == "" {
		return nil
	}
	if err := store.SeedBinding(ctx, identity); err != nil {
		return fmt.Errorf("seeding embed binding marker: %w", err)
	}
	populated, _, _, err := store.PopulatedIdentity(ctx)
	if err != nil {
		return fmt.Errorf("reading embed binding marker: %w", err)
	}
	if populated != identity {
		log.Error("embed binding changed", "configured", identity, "populated", populated)
	}
	return nil
}

// NewLocalModelPath builds a ModelPath over the DB-less local router
// (ai.NewLocalRouter) instead of NewModelPath's Postgres-backed one —
// the same lane set, wired the same way, for a caller with no pool (the
// compose integration suites' offline fixtures, which need the named
// ModelPath lanes rather than a bare *ai.Router). The aicert lane's
// candidate/judge routers go through NewLocalRouterForCert instead —
// they drive an arbitrary corpus task, including the judge-only
// cert_judge, so they need the router itself, not one of ModelPath's
// fixed named completers. opts ride straight through to NewLocalRouter,
// so a caller wires a call recorder, disables the result cache, or pins
// a static budget exactly as it would calling the router constructor
// directly.
func NewLocalModelPath(cfg ai.RoutingConfig, opts ...ai.LocalOption) (ModelPath, error) {
	router, err := ai.NewLocalRouter(cfg, opts...)
	if err != nil {
		return ModelPath{}, err
	}
	return modelPathForRouter(router, newCompanyContextProvider(nil)), nil
}

func modelPathForRouter(router *ai.Router, companyContext *companyContextProvider) ModelPath {
	brain := func(task ai.Task) routerBrain {
		return routerBrain{router: router, task: task, companyContext: companyContext}
	}
	return ModelPath{
		Agent:           agentBrain{router: router, companyContext: companyContext},
		ColdStart:       brain(ai.TaskColdStart),
		SiteExtract:     brain(ai.TaskSiteExtract),
		SiteFactExtract: brain(ai.TaskSiteFactExtract),
		BriefRank:       brain(ai.TaskBriefRanking),
		DraftReply:      brain(ai.TaskDraftReply),
		OfferDraft:      brain(ai.TaskOfferDraft),
		CaptureClassify: brain(ai.TaskCaptureClassify),
		SignatureEnrich: brain(ai.TaskEnrich),
		VoiceBuild:      brain(ai.TaskVoiceBuild),
		Embedder:        router,
	}
}

// NewLocalRouterForCert builds the DB-less local router the aicert
// certification lane (compose/aicert) drives directly. ModelPath's own
// lanes (ColdStart, SiteExtract, ...) are fixed, named workloads; the
// cert lane must complete an arbitrary corpus task — any ai.Task,
// including the judge-only cert_judge — on two independently
// configured routers (the candidate, optionally MODEL=-overridden on
// just its own task's ladder; the judge, always the unmodified config),
// so it needs the router itself, not one of ModelPath's named
// completers. This thin passthrough exists so the raw ai.NewLocalRouter
// construction stays inside this file — the one seam
// arch_test.go's TestNoModelClientOutsideTheGate enforces — rather than
// aicert becoming a second, ungated construction site.
func NewLocalRouterForCert(cfg ai.RoutingConfig, opts ...ai.LocalOption) (*ai.Router, error) {
	return ai.NewLocalRouter(cfg, opts...)
}

// Router exposes the model path's underlying router — the same one every model
// lane rides — so the ADR-0068 cost pre-flight can price observed history at the
// exact tier bindings that will serve it (via the router's BoundLadder /
// CurrentModelForTier resolvers). Nil when no router backs this path (a nil-Agent
// ModelPath), so a caller wires no priced estimate rather than pricing against an
// absent ladder.
func (p ModelPath) Router() *ai.Router {
	if r, ok := p.Agent.(agentBrain); ok {
		return r.router
	}
	return nil
}

// WriteMetrics renders the model path's underlying router's AI call
// counters (margince_ai_calls_total et al.) for the /metrics endpoint.
// Nil-safe for a ModelPath built with a nil Agent (no model path
// configured), so a role that never wired one writes nothing rather than
// panicking.
func (p ModelPath) WriteMetrics(w io.Writer) {
	if r, ok := p.Agent.(agentBrain); ok {
		r.router.WriteMetrics(w)
	}
}

// routerBrain adapts the tiered router into the 2-return completer seam
// the direct-call lanes use, under a fixed task label.
type routerBrain struct {
	router         *ai.Router
	task           ai.Task
	companyContext *companyContextProvider
}

func (b routerBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	prepared, err := b.companyContext.Prepare(ctx, b.task, req)
	if err != nil {
		return model.Response{}, err
	}
	resp, _, err := b.router.Complete(ctx, b.task, prepared)
	return resp, err
}

// agentBrain adapts the router into the runner's Brain seam: it surfaces
// the served model identity from RouteInfo as runner.Meta so the
// Surface-B trace records what answered each step WITHOUT re-calling the
// model (RUNNER-AC-4). The runner lane is the only consumer that needs
// this — the direct-call lanes use routerBrain.
type agentBrain struct {
	router         *ai.Router
	companyContext *companyContextProvider
}

func (b agentBrain) Complete(ctx context.Context, req model.Request) (model.Response, runner.Meta, error) {
	prepared, err := b.companyContext.Prepare(ctx, ai.TaskAgentLoop, req)
	if err != nil {
		return model.Response{}, runner.Meta{}, err
	}
	resp, info, err := b.router.Complete(ctx, ai.TaskAgentLoop, prepared)
	return resp, runner.Meta{ModelID: info.ModelID, Tier: string(info.Tier)}, err
}

// CompleteValidated exposes the §5.2 structured-output pipeline
// (validate → retry with feedback → escalate a tier) on the lane's own
// task label.
func (b routerBrain) CompleteValidated(ctx context.Context, req model.Request, validate ai.Validator) (model.Response, error) {
	prepared, err := b.companyContext.Prepare(ctx, b.task, req)
	if err != nil {
		return model.Response{}, err
	}
	resp, _, err := b.router.CompleteStructured(ctx, b.task, prepared, validate)
	return resp, err
}

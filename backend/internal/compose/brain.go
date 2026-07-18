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
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
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
	OfferDraft      completer    // the offer regenerate-from-signal drafting call
	// CaptureClassify is the §2.8 batched mail-label lane (ADR-0063) —
	// the highest-volume, cheapest task, routed L-S with the C-C solo
	// re-ask riding the same ladder.
	CaptureClassify completer
	Embedder        search.Embedder // the retrieval embed lane
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
	return ModelPath{
		Agent:           agentBrain{router: router},
		ColdStart:       routerBrain{router: router, task: ai.TaskColdStart},
		SiteExtract:     routerBrain{router: router, task: ai.TaskSiteExtract},
		SiteFactExtract: routerBrain{router: router, task: ai.TaskSiteFactExtract},
		BriefRank:       routerBrain{router: router, task: ai.TaskBriefRanking},
		OfferDraft:      routerBrain{router: router, task: ai.TaskOfferDraft},
		CaptureClassify: routerBrain{router: router, task: ai.TaskCaptureClassify},
		Embedder:        router,
	}, nil
}

// WriteMetrics renders the model path's underlying router's AI call
// counters (margince_ai_calls_total et al.) for the /metrics endpoint.
// Nil-safe for the fake path (FakeModelPath's Agent is a fakeBrain, not an
// agentBrain, so it writes nothing rather than panicking).
func (p ModelPath) WriteMetrics(w io.Writer) {
	if r, ok := p.Agent.(agentBrain); ok {
		r.router.WriteMetrics(w)
	}
}

// FakeModelPath drives every lane with one offline fake — the dev/test
// path behind an explicit flag, never a silent default. The Agent lane
// wraps the fake in fakeBrain to satisfy runner.Brain's Meta return; the
// direct-call lanes take the fake directly through the completer seam.
func FakeModelPath(client *ai.FakeClient) ModelPath {
	return ModelPath{Agent: fakeBrain{client: client}, ColdStart: client, SiteExtract: client, SiteFactExtract: client, BriefRank: client, OfferDraft: client, CaptureClassify: client, Embedder: client}
}

// routerBrain adapts the tiered router into the 2-return completer seam
// the direct-call lanes use, under a fixed task label.
type routerBrain struct {
	router *ai.Router
	task   ai.Task
}

func (b routerBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	resp, _, err := b.router.Complete(ctx, b.task, req)
	return resp, err
}

// agentBrain adapts the router into the runner's Brain seam: it surfaces
// the served model identity from RouteInfo as runner.Meta so the
// Surface-B trace records what answered each step WITHOUT re-calling the
// model (RUNNER-AC-4). The runner lane is the only consumer that needs
// this — the direct-call lanes use routerBrain.
type agentBrain struct {
	router *ai.Router
}

func (b agentBrain) Complete(ctx context.Context, req model.Request) (model.Response, runner.Meta, error) {
	resp, info, err := b.router.Complete(ctx, ai.TaskAgentLoop, req)
	return resp, runner.Meta{ModelID: info.ModelID, Tier: string(info.Tier)}, err
}

// fakeBrain wraps the offline fake into runner.Brain for the Agent lane:
// ai.FakeClient cannot return runner.Meta (the ai module must not import
// runner — that inverts the module DAG), so compose supplies the adapter
// and stamps a fixed "fake" identity.
type fakeBrain struct{ client *ai.FakeClient }

func (b fakeBrain) Complete(ctx context.Context, req model.Request) (model.Response, runner.Meta, error) {
	resp, err := b.client.Complete(ctx, req)
	return resp, runner.Meta{ModelID: "fake", Tier: "fake"}, err
}

// CompleteValidated exposes the §5.2 structured-output pipeline
// (validate → retry with feedback → escalate a tier) on the lane's own
// task label.
func (b routerBrain) CompleteValidated(ctx context.Context, req model.Request, validate ai.Validator) (model.Response, error) {
	resp, _, err := b.router.CompleteStructured(ctx, b.task, req, validate)
	return resp, err
}

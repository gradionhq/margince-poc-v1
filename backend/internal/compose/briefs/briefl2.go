// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package briefs

// The L2 Morning-Brief ranker (B-E05.2, formulas-and-rules §10): the
// model layer re-orders the deterministic §10.1 candidate set within
// itself. It is advisory over a real floor (ADR-0009 — L2 over the graph,
// no frontier reinvention): the model may re-order but can never inject a
// deal below the §10 cutoff, drop the set below it, or ship a claim with
// no evidence. That guarantee is enforced HERE, deterministically, in
// boundToCandidates — not trusted to the model. When the model is
// unavailable or answers malformed, the ranker returns the deterministic
// composite order unchanged (the AI-off fallback rank, §10.1).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// briefBrain is the narrow model seam the L2 ranker needs — one
// completion call. Compose adapts the tiered ai.Router into it (the
// brief_ranking task lane), so the ranker rides routing, budget bands and
// secret-stripping without importing a sibling module.
type briefBrain interface {
	Complete(ctx context.Context, req model.Request) (model.Response, error)
}

// briefL2Ranker re-orders the deterministic candidate set through the
// model. A nil brain is not constructible here — the engine simply skips
// the L2 pass when no ranker is wired, so this type always has a model.
type briefL2Ranker struct {
	brain briefBrain
	log   *slog.Logger
}

// briefL2MaxTokens bounds the re-order response: a permutation of at most
// a handful of candidate ids is small, and the cap keeps a runaway model
// from spending the run's whole budget on this advisory step.
const briefL2MaxTokens = 1024

// briefL2System instructs the model to re-rank the deterministic
// candidates and return ONLY their own ids. The bounding step enforces
// this regardless of what the model returns, so the prompt is guidance,
// never a trust boundary.
const briefL2System = `You re-rank a sales rep's morning-brief deal queue.
Each candidate carries a deterministic feature vector (each factor 0..1): winnability, revenue, timing, momentum (overnight change), warmth (strongest stakeholder). Higher is more worth acting on today.
Re-order the deals best-first using judgment the flat weighted score cannot capture (e.g. a fresh overnight reply on a high-value deal outranks a slightly higher static score).
Return ONLY a JSON object {"order":[deal_id,...]} listing EVERY given deal id exactly once, best-first. Never invent an id, never drop one, never add commentary.`

// reorder asks the model to re-rank candidates and returns a permutation
// strictly bounded to that set. The candidate list is already the §10
// candidate set (each item ≥ cutoff, deterministically ordered); the
// result is guaranteed a permutation of it, so the caller's honest-short
// truncation and evidence gate hold by construction.
func (rk briefL2Ranker) reorder(ctx context.Context, candidates []BriefQueueItem) []BriefQueueItem {
	if len(candidates) < 2 {
		// Nothing to re-order — an empty or singleton queue is its own order.
		return candidates
	}
	order, err := rk.askModel(ctx, candidates)
	if err != nil {
		// The L2 layer is advisory over the deterministic floor: an
		// unavailable or malformed model response degrades to the §10.1
		// composite order, it never fails the brief.
		rk.log.WarnContext(ctx, "brief: L2 re-order unavailable — using the deterministic composite order", "err", err)
		return candidates
	}
	return boundToCandidates(order, candidates)
}

// askModel builds the re-order prompt from the candidates' feature
// vectors, calls the model, and parses the ordered id list. The feature
// vector — not raw graph rows — is what the deterministic layer hands the
// L2 ranker (§10.1 output); it is the same no-mystery-number basis the
// rep sees, so the model reasons over exactly the evidenced factors.
func (rk briefL2Ranker) askModel(ctx context.Context, candidates []BriefQueueItem) ([]ids.UUID, error) {
	var b strings.Builder
	b.WriteString("Candidates:\n")
	for _, item := range candidates {
		f := item.Features
		fmt.Fprintf(&b, "- %s: winnability=%.2f revenue=%.2f timing=%.2f momentum=%.2f warmth=%.2f (composite=%.3f)\n",
			item.DealID, f.Winnability, f.Revenue, f.Timing, f.Momentum, f.Warmth, item.Composite)
	}

	resp, err := rk.brain.Complete(ctx, model.Request{
		System:         briefL2System,
		Messages:       []model.Message{{Role: "user", Content: b.String()}},
		MaxTokens:      briefL2MaxTokens,
		SecretStripper: ai.NewSecretStripper(),
	})
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Order []ids.UUID `json:"order"`
	}
	if err := json.Unmarshal([]byte(ai.Unfence(resp.Text)), &parsed); err != nil {
		return nil, fmt.Errorf("brief: L2 response is not {\"order\":[...]}: %w", err)
	}
	return parsed.Order, nil
}

// boundToCandidates is the deterministic guardrail that makes the L2
// layer safe (B-E05.2): whatever ids the model returned, the result is
// exactly a permutation of the candidate set. A hallucinated or duplicate
// id can never enter the queue, and a candidate the model omitted keeps
// its deterministic slot at the tail — so the model re-orders the set but
// can never shrink it below the §10 cutoff or drop an evidenced deal.
func boundToCandidates(order []ids.UUID, candidates []BriefQueueItem) []BriefQueueItem {
	byID := make(map[ids.UUID]BriefQueueItem, len(candidates))
	for _, c := range candidates {
		byID[c.DealID] = c
	}
	out := make([]BriefQueueItem, 0, len(candidates))
	taken := make(map[ids.UUID]bool, len(candidates))
	for _, id := range order {
		item, known := byID[id]
		if !known || taken[id] {
			continue
		}
		taken[id] = true
		out = append(out, item)
	}
	for _, c := range candidates {
		if !taken[c.DealID] {
			out = append(out, c)
		}
	}
	return out
}

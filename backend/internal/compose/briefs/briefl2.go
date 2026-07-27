// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package briefs

// The L2 Morning-Brief ranker (B-E05.2, formulas-and-rules §10): the
// model layer re-orders the deterministic §10.1 candidate set within
// itself. It is advisory over a real floor (ADR-0009 — L2 over the graph,
// no frontier reinvention): the model may re-order but can never inject a
// deal below the §10 cutoff, drop the set below it, or ship a claim with
// no evidence. That guarantee is enforced HERE, deterministically, in
// BoundToCandidates — not trusted to the model. When the model is
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
	return BoundToCandidates(order, candidates)
}

// askModel asks the model for a re-order and reads the id list back. Both
// halves are pure functions of plain data, exported below so the
// certification lane runs THEM rather than a re-creation: a copy of a
// prompt stays green through the change that breaks the original.
func (rk briefL2Ranker) askModel(ctx context.Context, candidates []BriefQueueItem) ([]ids.UUID, error) {
	resp, err := rk.brain.Complete(ctx, RankRequest(candidates))
	if err != nil {
		return nil, err
	}
	return ParseRankOrder(resp.Text)
}

// RankRequest builds the re-order prompt from the candidates' feature
// vectors. The feature vector — not raw graph rows — is what the
// deterministic layer hands the L2 ranker (§10.1 output); it is the same
// no-mystery-number basis the rep sees, so the model reasons over exactly
// the evidenced factors.
//
// NOTHING UNTRUSTED REACHES THIS PROMPT. Every byte it renders is
// machine-made: deal ids this installation minted and factors the
// deterministic §10.1 fold computed. That is why the request declares no
// data boundary and wraps nothing in a promptfence — there is no
// attacker-authored span to bound, and a fence around numbers would be a
// boundary sentence with nothing behind it.
//
// A field carrying anything a human or a counterparty wrote — a deal name,
// a note, a subject line — changes that on the spot: it would need
// promptfence.Wrap around the span and fence.Rule in the system prompt,
// neither of which this site has today. Adding one without the other is
// how an ordinary product improvement becomes an injection surface.
//
// The reply is likewise a closed shape (ids, nothing else), so the request
// carries no ResponseSchema: BoundToCandidates enforces the shape that
// matters regardless of what the model returns.
func RankRequest(candidates []BriefQueueItem) model.Request {
	var b strings.Builder
	b.WriteString("Candidates:\n")
	for _, item := range candidates {
		f := item.Features
		fmt.Fprintf(&b, "- %s: winnability=%.2f revenue=%.2f timing=%.2f momentum=%.2f warmth=%.2f (composite=%.3f)\n",
			item.DealID, f.Winnability, f.Revenue, f.Timing, f.Momentum, f.Warmth, item.Composite)
	}
	return model.Request{
		System:         briefL2System,
		Messages:       []model.Message{{Role: "user", Content: b.String()}},
		MaxTokens:      ai.ReasoningOutputMaxTokens,
		SecretStripper: ai.NewSecretStripper(),
	}
}

// ParseRankOrder reads the ordered id list out of a reply. It is this
// site's ONLY refusal: a reply that cannot be read degrades the whole L2
// pass to the deterministic composite order, while a reply that can be
// read is bounded rather than rejected — see BoundToCandidates.
func ParseRankOrder(text string) ([]ids.UUID, error) {
	var parsed struct {
		Order []ids.UUID `json:"order"`
	}
	if err := json.Unmarshal([]byte(ai.Unfence(text)), &parsed); err != nil {
		return nil, fmt.Errorf("brief: L2 response is not {\"order\":[...]}: %w", err)
	}
	return parsed.Order, nil
}

// BoundToCandidates is the deterministic guardrail that makes the L2
// layer safe (B-E05.2): whatever ids the model returned, the result is
// exactly a permutation of the candidate set. A hallucinated or duplicate
// id can never enter the queue, and a candidate the model omitted keeps
// its deterministic slot at the tail — so the model re-orders the set but
// can never shrink it below the §10 cutoff or drop an evidenced deal.
//
// It repairs rather than refuses, which is what keeps a bad reply from
// failing a rep's morning — and it is why a reply that needed repairing is
// only visible by comparing what the model said against what this returns.
func BoundToCandidates(order []ids.UUID, candidates []BriefQueueItem) []BriefQueueItem {
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

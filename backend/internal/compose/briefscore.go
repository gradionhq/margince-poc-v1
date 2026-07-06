// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The pure §10.1 fold behind the Morning-Brief rank (B-E05.1): the
// tunables, the feature vector, the per-deal score, the honest-short
// queue cutoff with its stable tie-breaks, and the B-E05.12
// evidence-or-omit gate. No I/O lives here — same facts + clock always
// fold to the same queue, which is what makes the rank testable and the
// AI re-order above it (B-E05.2) bounded by something real.

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// §10.1 tunables (spec parameter-registry names in comments).
const (
	briefWeightWinnability = 0.30 // W_WIN
	briefWeightRevenue     = 0.25 // W_REV
	briefWeightTiming      = 0.20 // W_TIME
	briefWeightMomentum    = 0.15 // W_MOM
	briefWeightWarmth      = 0.10 // W_WARM

	// briefCandidateMinScore is the honest-short bar: a deal below it is
	// not actionable and never pads the queue.
	briefCandidateMinScore = 0.15 // BRIEF_CANDIDATE_MIN_SCORE
	briefQueueTarget       = 7    // BRIEF_QUEUE_TARGET

	// The revenue factor normalizes against the workspace P90 deal size;
	// below ten deals of history the P90 is statistical noise, so a fixed
	// €50,000 (minor units) stands in.
	briefRevenueNormPercentile    = 0.9 // REVENUE_NORM = workspace P90
	briefRevenueNormMinDeals      = 10
	briefRevenueNormFallbackMinor = 50_000_00 // REVENUE_NORM_FALLBACK

	// Momentum floor: a deal with no evidenced overnight change still
	// carries baseline momentum, never zero (§10.1).
	briefMomentumChanged   = 1.0
	briefMomentumUnchanged = 0.4

	// briefOvernightEvidenceCap bounds the per-deal overnight-activity
	// evidence payload; the momentum boolean is unaffected.
	briefOvernightEvidenceCap = 50
)

// BriefFeatureVector is the §10.1 factor decomposition, each 0..1 — the
// per-item "no mystery number" breakdown the composite reconciles to.
type BriefFeatureVector struct {
	Winnability float64 `json:"winnability"`
	Revenue     float64 `json:"revenue"`
	Timing      float64 `json:"timing"`
	Momentum    float64 `json:"momentum"`
	Warmth      float64 `json:"warmth"`
}

// composite folds the vector through the §10.1 weights.
func (v BriefFeatureVector) composite() float64 {
	return briefWeightWinnability*v.Winnability +
		briefWeightRevenue*v.Revenue +
		briefWeightTiming*v.Timing +
		briefWeightMomentum*v.Momentum +
		briefWeightWarmth*v.Warmth
}

// BriefQueueItem is one ranked queue entry: the §10.1 output shape
// [{deal_id, composite, feature_vector, evidence_ids[]}].
type BriefQueueItem struct {
	DealID      ids.UUID
	Composite   float64
	Features    BriefFeatureVector
	EvidenceIDs []ids.UUID
}

// briefDealFacts are the raw per-deal facts the candidate query gathers;
// briefScore folds them deterministically so the formula is testable
// without a database.
type briefDealFacts struct {
	dealID         ids.UUID
	winProbability int
	// baseValueMinor is the §6 base-currency value; nil when the deal has
	// no amount or its amount cannot be honestly converted (no FX rate) —
	// the revenue factor then contributes its floor of 0, never a guess.
	baseValueMinor *int64
	expectedClose  *time.Time

	// overnightActivityIDs are the deal-linked activities since the rep's
	// last brief view (all-time when the rep never had a brief) — the
	// momentum evidence.
	overnightActivityIDs []ids.UUID

	// warmth: the strongest visible stakeholder's §4 strength and the
	// interaction rows it decomposes to.
	warmthStrength int
	warmthEvidence []ids.UUID
}

// briefScore is the pure §10.1 fold: same facts + norm, same score, no
// I/O. Every factor that lacks evidence sits at its floor by
// construction — momentum is 1.0 only over overnight rows, warmth only
// over §4 contributing interactions, revenue only over a real base value.
func briefScore(f briefDealFacts, revenueNormMinor int64, now time.Time) BriefQueueItem {
	features := BriefFeatureVector{
		Winnability: float64(f.winProbability) / 100,
		Revenue:     briefRevenueScore(f.baseValueMinor, revenueNormMinor),
		Timing:      briefTimingScore(f.expectedClose, now),
		Momentum:    briefMomentumUnchanged,
		Warmth:      float64(f.warmthStrength) / 100,
	}
	if len(f.overnightActivityIDs) > 0 {
		features.Momentum = briefMomentumChanged
	}
	if len(f.warmthEvidence) == 0 {
		features.Warmth = 0
	}

	// The deal row itself is the source fact behind winnability, revenue
	// and timing (win probability, amount, close date live on it); the
	// activity rows evidence momentum and warmth.
	evidence := make([]ids.UUID, 0, 1+len(f.overnightActivityIDs)+len(f.warmthEvidence))
	evidence = append(evidence, f.dealID)
	seen := map[ids.UUID]bool{f.dealID: true}
	for _, group := range [][]ids.UUID{f.overnightActivityIDs, f.warmthEvidence} {
		for _, id := range group {
			if !seen[id] {
				seen[id] = true
				evidence = append(evidence, id)
			}
		}
	}

	return BriefQueueItem{
		DealID:      f.dealID,
		Composite:   features.composite(),
		Features:    features,
		EvidenceIDs: evidence,
	}
}

// briefRevenueScore is min(1, base_value/REVENUE_NORM); no evidencable
// base value → the floor.
func briefRevenueScore(baseValueMinor *int64, revenueNormMinor int64) float64 {
	if baseValueMinor == nil {
		return 0
	}
	return math.Min(1.0, float64(*baseValueMinor)/float64(revenueNormMinor))
}

// briefTimingScore buckets whole calendar days until the expected close
// (UTC dates, like the column's date type): overdue is urgent, this week
// is hottest, and no date at all reads as the mild-uncertainty midpoint.
func briefTimingScore(expectedClose *time.Time, now time.Time) float64 {
	if expectedClose == nil {
		return 0.3
	}
	today := now.UTC().Truncate(24 * time.Hour)
	d := expectedClose.Sub(today).Hours() / 24
	switch {
	case d < 0:
		return 0.9
	case d <= 7:
		return 1.0
	case d <= 30:
		return 0.7
	case d <= 90:
		return 0.4
	default:
		return 0.2
	}
}

// briefCandidateOrder applies the honest-short cutoff and the §10.1
// stable order — composite desc, ties by higher base_value, then sooner
// expected close, then lowest deal id — and returns the FULL ordered
// candidate set (every deal clearing the bar), the deterministic floor
// the L2 ranker (B-E05.2) re-orders within. The queue is the top
// briefQueueTarget of it; the count is len().
func briefCandidateOrder(scored []BriefQueueItem, facts map[ids.UUID]briefDealFacts) []BriefQueueItem {
	candidates := make([]BriefQueueItem, 0, len(scored))
	for _, item := range scored {
		if item.Composite >= briefCandidateMinScore {
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Composite != b.Composite {
			return a.Composite > b.Composite
		}
		av, bv := facts[a.DealID].baseValueMinor, facts[b.DealID].baseValueMinor
		if !int64PtrEqual(av, bv) {
			// Higher base value wins; a deal with no amount sorts below any
			// valued one.
			switch {
			case av == nil:
				return false
			case bv == nil:
				return true
			default:
				return *av > *bv
			}
		}
		ac, bc := facts[a.DealID].expectedClose, facts[b.DealID].expectedClose
		if !timePtrEqual(ac, bc) {
			// Sooner close wins; no date sorts after any dated deal.
			switch {
			case ac == nil:
				return false
			case bc == nil:
				return true
			default:
				return ac.Before(*bc)
			}
		}
		return bytes.Compare(a.DealID[:], b.DealID[:]) < 0
	})
	return candidates
}

// briefQueue is the deterministic queue: the top briefQueueTarget of the
// ordered candidate set, genuinely shorter when fewer clear the bar
// (padding with stale deals is a test failure). It is the AI-off
// fallback rank — the same order the L2 path yields when the model layer
// is unavailable.
func briefQueue(scored []BriefQueueItem, facts map[ids.UUID]briefDealFacts) (queue []BriefQueueItem, candidateCount int) {
	candidates := briefCandidateOrder(scored, facts)
	if len(candidates) > briefQueueTarget {
		return candidates[:briefQueueTarget], len(candidates)
	}
	return candidates, len(candidates)
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// validateBriefCandidates gates the deterministic floor before the L2
// layer re-orders it (B-E05.1/.12): every candidate carries at least one
// resolvable source id (evidence-or-omit), clears the honest-short bar,
// and the set is in the deterministic composite-descending order. A
// violation means the deterministic ranker itself is broken, so the run
// fails rather than handing the L2 layer — or the rep — a padded or
// unevidenced claim. It does NOT cap the length: this is the full
// candidate set, which may exceed the queue target.
func validateBriefCandidates(candidates []BriefQueueItem) error {
	prevComposite := math.Inf(1)
	for i, item := range candidates {
		if len(item.EvidenceIDs) == 0 {
			return fmt.Errorf("brief: candidate %d (deal %s) carries no evidence — evidence-or-omit forbids shipping it", i+1, item.DealID)
		}
		if item.Composite < briefCandidateMinScore {
			return fmt.Errorf("brief: candidate %d (deal %s) scores %.3f below the %.2f bar — the set must never be padded", i+1, item.DealID, item.Composite, briefCandidateMinScore)
		}
		if item.Composite > prevComposite {
			return fmt.Errorf("brief: candidate %d outranks candidate %d despite a higher composite — the deterministic order was lost", i+1, i)
		}
		prevComposite = item.Composite
	}
	return nil
}

// validateBriefQueue gates the queue the L2 ranker produced (B-E05.2/.12):
// it never exceeds the target, every item still carries evidence and
// clears the bar, and — the deterministic guarantee that stays real when
// the model re-orders — every queued deal is drawn from the deterministic
// candidate set (the AI can re-order but never inject a deal below the
// §10 cutoff). Composite order is deliberately NOT checked: re-ordering
// within the candidate set is exactly what the L2 layer is for.
func validateBriefQueue(queue, candidates []BriefQueueItem) error {
	if len(queue) > briefQueueTarget {
		return fmt.Errorf("brief: queue of %d exceeds the %d target — the honest-short cutoff was not applied", len(queue), briefQueueTarget)
	}
	inCandidateSet := make(map[ids.UUID]bool, len(candidates))
	for _, c := range candidates {
		inCandidateSet[c.DealID] = true
	}
	for i, item := range queue {
		if len(item.EvidenceIDs) == 0 {
			return fmt.Errorf("brief: item %d (deal %s) carries no evidence — evidence-or-omit forbids shipping it", i+1, item.DealID)
		}
		if item.Composite < briefCandidateMinScore {
			return fmt.Errorf("brief: item %d (deal %s) scores %.3f below the %.2f bar — the queue must never be padded", i+1, item.DealID, item.Composite, briefCandidateMinScore)
		}
		if !inCandidateSet[item.DealID] {
			return fmt.Errorf("brief: item %d (deal %s) is not in the deterministic candidate set — the L2 layer breached the §10 cutoff", i+1, item.DealID)
		}
	}
	return nil
}

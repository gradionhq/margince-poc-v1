// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package costestimate

// The AI cost pre-flight estimator (ADR-0068, phase 2/2 of the cost hand-off).
//
// It answers the backfill preview's north star: for a task of this kind, served
// by the model/tier that will actually run it, input X messages → cost Y. Two
// inputs compose per task and degrade INDEPENDENTLY:
//
//   - per-UNIT cost, from this workspace's served ai_call slices, each priced at
//     the model that WILL run it (served-if-still-bound, else the slice's own
//     tier's current binding, else the ladder head, else unpriced);
//   - expected UNITS for X, from this connection's representative backfill yields
//     (else a named work-shape floor).
//
// Either falling to its floor marks the whole estimate heuristic. Nothing priced
// ⇒ HasCost=false and the wire suppresses the cost field — a fabricated or
// silently-zero cost is the worst failure a consent-before-spend number can have
// (cost is transparency, never a gate: ADR-0020, NEVER-4). This is the only
// money-aware code in the tree; the ai / capture / activities reads it composes
// never import one another.

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Quality is the honesty signal surfaced alongside the estimate: whether it was
// priced from real observed history or fell back to a cold-start heuristic.
type Quality string

const (
	// QualityObserved: every priced task read a real per-unit cost from this
	// workspace's ai_call history AND real units from a completed backfill yield.
	QualityObserved Quality = "observed"
	// QualityHeuristic: at least one task fell to a work-shape floor or a
	// defaulted unit ratio, or carried an unpriced slice.
	QualityHeuristic Quality = "heuristic"
)

// BackfillCost is one preview's priced estimate. HasCost=false means nothing
// priced — the caller MUST suppress the cost field rather than render a 0.
type BackfillCost struct {
	CostMinor   int64   // USD minor units (micro-USD ÷ microsPerMinor)
	Currency    string  // "USD" in v1
	HasCost     bool    // false ⇒ nothing priced; suppress the wire cost field
	InputTokens int64   // surfaced estimated_ai_tokens (input-anchored)
	Quality     Quality // observed | heuristic
}

// Clock is injected so the 7-day window and the rate as-of day are testable
// without a real clock (no time.Now() in the estimator; T11).
type Clock interface{ Now() time.Time }

// The narrow dependency ports. They are interfaces, not the concrete stores, so
// the estimator unit-tests against fakes and the module DAG holds (compose
// depends on the module reads, never the reverse).
type (
	// ServedTotalsReader is ai.CallReadStore.ServedTaskTotals.
	ServedTotalsReader interface {
		ServedTaskTotals(ctx context.Context, tasks []ai.Task, since time.Time) ([]ai.ServedTaskTotal, error)
	}
	// RateResolver is ai.RateStore.RateFor — (nil, nil) means unpriced.
	RateResolver interface {
		RateFor(ctx context.Context, provider, model string, day time.Time) (*ai.ModelRate, error)
	}
	// LadderResolver is ai.Router's pure binding resolvers.
	LadderResolver interface {
		BoundLadder(task ai.Task) []ai.ModelRef
		CurrentModelForTier(tier ai.Tier) (ai.ModelRef, bool)
	}
	// LabeledCounter is activities.Store.LabeledCaptureCountSince — classify's
	// exact observed-units denominator.
	LabeledCounter interface {
		LabeledCaptureCountSince(ctx context.Context, since time.Time) (int64, error)
	}
	// YieldReader is capture.Registry.BackfillYields — the previewing
	// connection's representative completed run (zero-value ⇒ floor).
	YieldReader interface {
		BackfillYields(ctx context.Context, provider string, userID ids.UserID) (capture.BackfillYields, error)
	}
)

// Estimator composes the five reads into the priced backfill estimate.
type Estimator struct {
	totals ServedTotalsReader
	rates  RateResolver
	ladder LadderResolver
	labels LabeledCounter
	yields YieldReader
	clock  Clock
}

// NewEstimator wires the estimator over its five reads and an injected clock.
func NewEstimator(totals ServedTotalsReader, rates RateResolver, ladder LadderResolver,
	labels LabeledCounter, yields YieldReader, clock Clock) *Estimator {
	return &Estimator{totals: totals, rates: rates, ladder: ladder, labels: labels, yields: yields, clock: clock}
}

const (
	// estimateWindow is the trailing history the per-unit cost is measured over.
	estimateWindow = 7 * 24 * time.Hour
	// microsPerMinor converts the pricer's micro-USD (1e-6 USD) to USD minor
	// units (cents, 1e-2 USD): 1e-2 / 1e-6 = 1e4.
	microsPerMinor = 10_000
	currencyUSD    = "USD"
)

// EstimateBackfill prices the projected spend of backfilling scannedMessages for
// the given connection. Every port read propagates its error (never swallowed).
func (e *Estimator) EstimateBackfill(ctx context.Context, provider string, userID ids.UserID, scannedMessages int64) (BackfillCost, error) {
	today := e.clock.Now()
	since := today.Add(-estimateWindow)

	totals, err := e.totals.ServedTaskTotals(ctx, backfillTasks, since)
	if err != nil {
		return BackfillCost{}, err
	}
	byTask := make(map[ai.Task][]ai.ServedTaskTotal, len(backfillTasks))
	for _, s := range totals {
		byTask[s.Task] = append(byTask[s.Task], s)
	}

	labeledCount, err := e.labels.LabeledCaptureCountSince(ctx, since)
	if err != nil {
		return BackfillCost{}, err
	}
	yields, err := e.yields.BackfillYields(ctx, provider, userID)
	if err != nil {
		return BackfillCost{}, err
	}

	quality := QualityObserved
	hasCost := false
	var costMicro, inputTokens int64

	for _, task := range backfillTasks {
		units, unitsObserved := expectedUnits(task, scannedMessages, yields)
		if !unitsObserved {
			quality = QualityHeuristic
		}

		slices := byTask[task]
		denom := observedUnitsDenom(task, slices, labeledCount)

		if len(slices) > 0 && denom > 0 {
			taskCost, taskTokens, taskPriced, taskHeuristic, err := e.priceObserved(ctx, task, slices, units, denom, today)
			if err != nil {
				return BackfillCost{}, err
			}
			costMicro += taskCost
			inputTokens += taskTokens
			if taskPriced {
				hasCost = true
			}
			if taskHeuristic {
				quality = QualityHeuristic
			}
			continue
		}

		// No observed cost / no denominator (e.g. a no-label classify week) →
		// the work-shape floor. Always heuristic.
		quality = QualityHeuristic
		floor := workShapeFloor(task)
		inputTokens += int64(floor.TokensIn) * units // tokens surfaced even when unpriced
		bound := e.ladder.BoundLadder(task)
		if len(bound) == 0 {
			continue // empty ladder → unpriced, never index [0]
		}
		rate, err := e.rates.RateFor(ctx, bound[0].Provider, bound[0].Model, today)
		if err != nil {
			return BackfillCost{}, err
		}
		if rate != nil {
			costMicro += ai.PriceCall(floor, *rate) * units
			hasCost = true
		}
	}

	return BackfillCost{
		CostMinor:   costMicro / microsPerMinor,
		Currency:    currencyUSD,
		HasCost:     hasCost,
		InputTokens: inputTokens,
		Quality:     quality,
	}, nil
}

// priceObserved prices one task's observed served slices, scaled from the
// window's observed units (denom) to the expected units for this preview. It
// returns the task's micro-USD cost, its surfaced input tokens, whether anything
// priced, and whether any slice forced a heuristic downgrade (unpriced or
// unresolvable to a model).
func (e *Estimator) priceObserved(ctx context.Context, task ai.Task, slices []ai.ServedTaskTotal, units, denom int64, today time.Time) (costMicro, inputTokens int64, priced, heuristic bool, err error) {
	var pricedCost, pricedCalls, sumCalls int64
	for _, s := range slices {
		sumCalls += s.Calls
		// Input-anchored, multiply-before-divide on the aggregate slice total —
		// no per-unit integer truncation. Surfaced for every slice, priced or not.
		inputTokens += s.TokensIn * units / denom

		eff, ok := effectiveModel(e.ladder, task, s)
		if !ok {
			// Empty ladder → tokens surfaced, the slice unpriced.
			heuristic = true
			continue
		}
		rate, rerr := e.rates.RateFor(ctx, eff.Provider, eff.Model, today)
		if rerr != nil {
			// A read fault is not "unpriced" — it must never be swallowed as one.
			return 0, 0, false, false, rerr
		}
		if rate != nil {
			pricedCost += ai.PriceCall(usageOf(s), *rate)
			pricedCalls += s.Calls
			priced = true
		} else {
			heuristic = true // a rate-less slice scales the priced mix as representative
		}
	}

	if pricedCalls > 0 {
		// Units attributable to the PRICED mix, floored ≥ 1 so pricedCalls > 0
		// never divides by zero.
		pricedDenom := max(denom*pricedCalls/max(sumCalls, 1), 1)
		costMicro = pricedCost * units / pricedDenom
	}
	return costMicro, inputTokens, priced, heuristic, nil
}

// expectedUnits maps this preview's scanned-message count to a task's expected
// unit count via the connection's backfill yields, or the floor when no
// completed run exists. Multiply-before-divide. observed=false ⇒ the floor was
// used (a heuristic).
func expectedUnits(task ai.Task, scanned int64, y capture.BackfillYields) (units int64, observed bool) {
	if y.Scanned <= 0 {
		return unitsFloor(task, scanned), false
	}
	switch task {
	case ai.TaskCaptureClassify:
		return scanned * y.Captured / y.Scanned, true // messages
	case ai.TaskEmbeddings:
		return scanned * (y.Captured + y.PeopleCreated + y.OrganizationsCreated) / y.Scanned, true // entities
	case ai.TaskEnrich:
		return scanned * y.PeopleCreated / y.Scanned, true // persons
	default:
		return unitsFloor(task, scanned), false
	}
}

// observedUnitsDenom is the observed unit count the window's served slices are
// divided by: classify's exact labeled-message count (absorbs batching + solo
// re-asks), else the summed served calls (one enrich call per person, one embed
// call per entity).
func observedUnitsDenom(task ai.Task, slices []ai.ServedTaskTotal, labeledCount int64) int64 {
	if task == ai.TaskCaptureClassify {
		return labeledCount
	}
	var sum int64
	for _, s := range slices {
		sum += s.Calls
	}
	return sum
}

// effectiveModel chooses the model to price a served slice at — the model that
// will actually RUN this task now, keyed on the slice's own recorded tier:
// served-if-still-bound, else the slice's tier's current binding (rebind
// reprice), else the ladder head, else unpriced (empty ladder).
func effectiveModel(ladder LadderResolver, task ai.Task, s ai.ServedTaskTotal) (ai.ModelRef, bool) {
	bound := ladder.BoundLadder(task)
	served := ai.ModelRef{Provider: s.Provider, Model: s.ModelID}
	for _, b := range bound {
		if b == served {
			return served, true // still runs → its own current rate
		}
	}
	if m, ok := ladder.CurrentModelForTier(s.Tier); ok {
		return m, true // departed → repriced at the CURRENT binding of its OWN tier
	}
	if len(bound) > 0 {
		return bound[0], true // its tier is now unbound → the ladder head
	}
	return ai.ModelRef{}, false // empty ladder → unpriced, never index [0]
}

// usageOf narrows a served slice's aggregate int64 token buckets into the int
// Usage the pricer takes. A 7-day workspace-scoped sum of served tokens fits an
// int comfortably (int is 64-bit on every platform this builds for), and every
// downstream product stays in int64 inside PriceCall — the narrowing here is the
// only int boundary and it does not overflow at realistic volumes.
func usageOf(s ai.ServedTaskTotal) ai.Usage {
	return ai.Usage{
		TokensIn:         int(s.TokensIn),
		CachedTokens:     int(s.CachedTokens),
		CacheWriteTokens: int(s.CacheWriteTokens),
		TokensOut:        int(s.TokensOut),
	}
}

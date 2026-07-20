// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The AIRT-WIRE-1 usage read: the AIRT-PARAM-33 meter aggregated per
// day × task × tier, plus the workspace's calendar-month budget
// position and its §1.3 band. Spend is never invisible (ADR-0020: the
// inference bill is the customer's own) — this read makes it visible.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TaskUsage is one (task, tier) aggregate line of a usage day.
//
// CostEstMicroUSD and UnpricedCalls carry RateStore.CostReport's
// (day, task) line merged onto every tier row of that task (ADR-0067
// price-on-read): CostReport prices per calendar day + task, one grain
// coarser than this line's day + task + tier, so a task split across
// tiers on the same day shows its shared task-day total on each tier
// row rather than a fabricated per-tier split. UnpricedCalls never rides
// the wire (AIRT-WIRE-1 has no contract field for it) — the handler
// reads it only to decide whether to omit a misleading zero cost.
type TaskUsage struct {
	Task            string
	Tier            string
	Calls           int
	CachedHits      int
	TokensIn        int
	TokensOut       int
	CostEstMicroUSD int64
	UnpricedCalls   int64
}

// DayUsage groups a day's task lines, ordered task then tier.
type DayUsage struct {
	Day   time.Time
	Tasks []TaskUsage
}

// BudgetStatus is the workspace's calendar-month budget position: the
// seat-derived pool, the month's spend, and the §1.3 band the router is
// currently applying.
type BudgetStatus struct {
	MonthlyTokens int64
	SpentTokens   int64
	Band          string
}

// Band names mirror the contract enum (AIRT-PARAM-9..11).
const (
	BandNormal   = "normal"
	BandDegraded = "degraded"
	BandQueued   = "queued"
)

// budgetBand maps utilization onto the §1.3 band — the same thresholds
// applyBudget enforces on the routing ladder.
func budgetBand(spent, budget int64) string {
	if budget <= 0 {
		// Fail closed on misconfiguration, mirroring applyBudget: a zero
		// budget reads as exhausted, never as unlimited.
		return BandQueued
	}
	utilization := float64(spent) / float64(budget)
	switch {
	case utilization >= queueUtilization:
		return BandQueued
	case utilization >= degradeUtilization:
		return BandDegraded
	default:
		return BandNormal
	}
}

// UsageReport reads the [from, to] window of ai_usage aggregates
// (inclusive day bounds) plus the budget position, then merges rates'
// CostReport (ADR-0067, price-on-read) onto each day's task lines —
// the one place token counts and priced cost join, so the handler stays
// a pure wire mapping with no money computation of its own. The closed
// RBAC object set carries no AI-runtime entry, so the AIRT-WIRE-1 admin
// surface is admitted through the automation-config write grant — held
// by exactly the admin/ops roles that own the workspace's operational
// configuration (the pipeline-config posture); agent principals are
// refused upstream by the contract's human-only marker.
func (m *Meter) UsageReport(ctx context.Context, budget BudgetPolicy, rates *RateStore, from, to time.Time) ([]DayUsage, BudgetStatus, error) {
	if err := auth.Require(ctx, "automation", principal.ActionUpdate); err != nil {
		return nil, BudgetStatus{}, err
	}
	rawWS, ok := principal.WorkspaceID(ctx)
	if !ok {
		return nil, BudgetStatus{}, fmt.Errorf("ai: usage report outside workspace context")
	}
	monthly, err := budget.MonthlyTokenBudget(ctx, ids.From[ids.WorkspaceKind](rawWS))
	if err != nil {
		return nil, BudgetStatus{}, fmt.Errorf("ai: budget policy: %w", err)
	}
	spent, err := m.MonthTokens(ctx)
	if err != nil {
		return nil, BudgetStatus{}, err
	}
	days, err := m.usageDays(ctx, from, to)
	if err != nil {
		return nil, BudgetStatus{}, fmt.Errorf("ai: usage report: %w", err)
	}
	if err := mergeDayCost(ctx, rates, days, from, to); err != nil {
		return nil, BudgetStatus{}, fmt.Errorf("ai: usage report cost: %w", err)
	}
	status := BudgetStatus{
		MonthlyTokens: monthly,
		SpentTokens:   spent,
		Band:          budgetBand(spent, monthly),
	}
	return days, status, nil
}

// usageDays reads the [from, to] inclusive-day window of ai_usage
// aggregates, grouped day → task lines in query order.
func (m *Meter) usageDays(ctx context.Context, from, to time.Time) ([]DayUsage, error) {
	var days []DayUsage
	err := database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT day, task, tier, calls, cached_hits, tokens_in, tokens_out
			FROM ai_usage
			WHERE day >= $1::date AND day <= $2::date
			ORDER BY day, task, tier`,
			from.UTC().Format(time.DateOnly), to.UTC().Format(time.DateOnly))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var day time.Time
			var line TaskUsage
			if err := rows.Scan(&day, &line.Task, &line.Tier, &line.Calls,
				&line.CachedHits, &line.TokensIn, &line.TokensOut); err != nil {
				return err
			}
			if len(days) == 0 || !days[len(days)-1].Day.Equal(day) {
				days = append(days, DayUsage{Day: day})
			}
			days[len(days)-1].Tasks = append(days[len(days)-1].Tasks, line)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return days, nil
}

// mergeDayCost prices [from, to)'s ai_call rows via rates.CostReport
// (ADR-0067, price-on-read) and attaches each (day, task) line's cost
// onto every tier row of that task/day in days, in place — the one join
// between token counts and priced cost, so the handler stays a pure
// wire mapping with no money computation of its own.
func mergeDayCost(ctx context.Context, rates *RateStore, days []DayUsage, from, to time.Time) error {
	// CostReport is [from, to) half-open on occurred_at, while usageDays
	// treats to as an inclusive calendar day (day <= to::date) — widen the
	// upper bound by a full day so a calendar-date to (midnight UTC)
	// still prices that whole day's calls instead of dropping them at the
	// boundary. Harmless when to already carries a time-of-day (the
	// UsageWindow default, to = now): it only pushes the cutoff into a
	// future that has no rows yet.
	costs, err := rates.CostReport(ctx, from, to.Add(24*time.Hour))
	if err != nil {
		return err
	}
	type dayTaskKey struct {
		day  string
		task string
	}
	costByDayTask := make(map[dayTaskKey]DayCost, len(costs))
	for _, c := range costs {
		costByDayTask[dayTaskKey{day: c.Day.UTC().Format(time.DateOnly), task: string(c.Task)}] = c
	}
	for i := range days {
		dayKey := days[i].Day.UTC().Format(time.DateOnly)
		for j := range days[i].Tasks {
			cost, ok := costByDayTask[dayTaskKey{day: dayKey, task: days[i].Tasks[j].Task}]
			if !ok {
				continue
			}
			days[i].Tasks[j].CostEstMicroUSD = cost.CostMicroUSD
			days[i].Tasks[j].UnpricedCalls = cost.UnpricedCalls
		}
	}
	return nil
}

// UsageWindow answers the AIRT-WIRE-1 defaults for an unbounded query:
// the first day of the current month through today.
func (m *Meter) UsageWindow() (from, to time.Time) {
	now := m.now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), now
}

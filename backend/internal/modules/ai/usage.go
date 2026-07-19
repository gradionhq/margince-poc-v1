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
type TaskUsage struct {
	Task       string
	Tier       string
	Calls      int
	CachedHits int
	TokensIn   int
	TokensOut  int
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
// (inclusive day bounds) plus the budget position. The closed RBAC
// object set carries no AI-runtime entry, so the AIRT-WIRE-1 admin
// surface is admitted through the automation-config write grant — held
// by exactly the admin/ops roles that own the workspace's operational
// configuration (the pipeline-config posture); agent principals are
// refused upstream by the contract's human-only marker.
func (m *Meter) UsageReport(ctx context.Context, budget BudgetPolicy, from, to time.Time) ([]DayUsage, BudgetStatus, error) {
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
	var days []DayUsage
	err = database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
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
		return nil, BudgetStatus{}, fmt.Errorf("ai: usage report: %w", err)
	}
	status := BudgetStatus{
		MonthlyTokens: monthly,
		SpentTokens:   spent,
		Band:          budgetBand(spent, monthly),
	}
	return days, status, nil
}

// UsageWindow answers the AIRT-WIRE-1 defaults for an unbounded query:
// the first day of the current month through today.
func (m *Meter) UsageWindow() (from, to time.Time) {
	now := m.now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), now
}

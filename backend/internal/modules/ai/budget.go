package ai

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ErrBudgetExhausted reports a non-interactive task refused at ≥100%
// budget utilization (§1.3): the caller queues it for next-cycle budget
// instead of running it degraded. Core CRM is never behind this error —
// only model calls are.
var ErrBudgetExhausted = errors.New("ai: workspace token budget exhausted; queue for next cycle")

// BudgetPolicy answers "how many tokens may this workspace burn per
// month". Injected so the composition layer can derive it from seat
// counts (09 §2.4: seats × 6M base × 2 safety) without this module
// reaching into identity's tables.
type BudgetPolicy interface {
	MonthlyTokenBudget(ctx context.Context, workspaceID ids.UUID) (int64, error)
}

// StaticBudget is the fixed fallback policy: the single-seat default
// until compose wires a live seat count.
type StaticBudget int64

// DefaultMonthlyTokens = 1 seat × 6M base × 2 safety factor.
const DefaultMonthlyTokens = StaticBudget(12_000_000)

func (b StaticBudget) MonthlyTokenBudget(context.Context, ids.UUID) (int64, error) {
	return int64(b), nil
}

// Utilization thresholds (§1.3, operational fill-in of the 09 §2.4
// ratified guardrail): soft-degrade band start and the hard cap.
const (
	degradeUtilization = 0.80
	queueUtilization   = 1.00
)

// premiumShareAlarmThreshold: a workspace whose premium-tier token
// share exceeds this over the trailing window gets flagged for a
// routing fix (§1.3) — the L2 analogue of "manual entry is a smell".
const premiumShareAlarmThreshold = 0.20

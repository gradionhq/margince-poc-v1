// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ErrBudgetDeferred identifies a background model call that its durable
// carrier must resume in the next budget window. The router owns the timing
// decision but never invents a generic job: the caller already owns the work.
var ErrBudgetDeferred = errors.New("ai: background task deferred until the next budget window")

// BudgetDeferralError carries the exact retry boundary to a background task's
// durable carrier. It is returned before a provider attempt or ai_call trace is
// created, so deferral is scheduling state rather than a failed model call.
type BudgetDeferralError struct {
	Task          Task
	NextAttemptAt time.Time
}

func (e *BudgetDeferralError) Error() string {
	return fmt.Sprintf("ai: task %s deferred until %s", e.Task, e.NextAttemptAt.Format(time.RFC3339))
}

func (e *BudgetDeferralError) Unwrap() error { return ErrBudgetDeferred }

// BudgetPolicy answers "how many tokens may this workspace burn per
// month". Injected so the composition layer can derive it from seat
// counts (09 §2.4: seats × 6M base × 2 safety) without this module
// reaching into identity's tables.
type BudgetPolicy interface {
	MonthlyTokenBudget(ctx context.Context, workspaceID ids.WorkspaceID) (int64, error)
}

// StaticBudget is the fixed fallback policy: the single-seat default
// until compose wires a live seat count.
type StaticBudget int64

// DefaultMonthlyTokens = 1 seat × 6M base × 2 safety factor.
const DefaultMonthlyTokens = StaticBudget(12_000_000)

func (b StaticBudget) MonthlyTokenBudget(context.Context, ids.WorkspaceID) (int64, error) {
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

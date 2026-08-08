// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// MCP-SESS-COST, joined up: the AI runtime knows what a call cost, the quota
// meter knows whose window to put it in, and neither may import the other.
//
// The spec words this quota as "tenant budget ÷ active sessions, soft". ADR-0092
// deletes the divisor's subject — there are no sessions once the registry goes —
// so the share is re-keyed exactly as the read bound was: the workspace's own
// budget, divided by the credentials sharing it.

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AgentTokenSpend adapts the quota meter to the AI runtime's token seam, so a
// served model call is charged to the agent that caused it.
//
// Exported because the two things it joins are assembled in cmd: the meter is
// built there (the raw-Redis dependency stays out of compose) and so is the
// model path. This is the one line that makes the join, and it is the whole of
// what cmd has to remember.
type AgentTokenSpend struct{ Meter *agentquota.Meter }

// SpendAgentTokens records tokens against the calling Passport's cost window.
// A human's model call records nothing — the meter's own governed check decides
// that, so this side never has to re-answer "which callers are metered".
func (s AgentTokenSpend) SpendAgentTokens(ctx context.Context, tokens int) error {
	return s.Meter.Consume(ctx, agentquota.Cost, tokens)
}

// budgetShareWindow is how much of the month one quota window covers. The
// workspace budget is monthly and the quota window is a rolling day, so a share
// of the budget has to be pro-rated to the window or the comparison is between
// two different spans — which would leave the counter warning about nothing.
const budgetShareWindowDays = 30

// shareCacheTTL bounds how stale the divisor may be. The share is a SOFT
// ceiling, so a passport minted five minutes ago not yet being in the divisor
// costs a slightly generous warning threshold and nothing else — where asking
// the database on every model call would put a query on the hot path of a
// counter that refuses nothing.
const shareCacheTTL = 5 * time.Minute

// passportShareCeiling answers how many model tokens ONE Passport may spend
// inside a quota window: the workspace's monthly budget, pro-rated to the
// window, divided by the live agent credentials sharing it.
//
// Dividing by the LIVE passports rather than by a constant is what keeps the
// spec's word "disproportionate" meaningful: one connected agent in a workspace
// legitimately has the whole share, and the tenth one does not get the same
// allowance the first had.
type passportShareCeiling struct {
	pool   *pgxpool.Pool
	budget ai.BudgetPolicy
	window time.Duration
	now    func() time.Time

	mu     sync.Mutex
	cached map[string]shareReading
}

type shareReading struct {
	tokens int
	at     time.Time
}

func newPassportShareCeiling(pool *pgxpool.Pool, window time.Duration) *passportShareCeiling {
	return &passportShareCeiling{
		pool: pool, budget: NewSeatBudget(pool), window: window, now: time.Now,
		cached: map[string]shareReading{},
	}
}

// TokensPerPassport answers this workspace's per-credential share, or 0 when it
// cannot be computed — which reads as "no ceiling" rather than "no headroom",
// because this counter warns and never refuses. A soft control that failed
// closed would raise its warning on every call during an outage, which is the
// fastest way to teach a reader to ignore it.
func (c *passportShareCeiling) TokensPerPassport(ctx context.Context) int {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return 0
	}
	key := wsID.String()
	c.mu.Lock()
	cached, hit := c.cached[key]
	c.mu.Unlock()
	if hit && c.now().Sub(cached.at) < shareCacheTTL {
		return cached.tokens
	}
	tokens := c.compute(ctx, ids.From[ids.WorkspaceKind](wsID))
	c.mu.Lock()
	c.cached[key] = shareReading{tokens: tokens, at: c.now()}
	c.mu.Unlock()
	return tokens
}

// compute reads the budget and the divisor. A workspace with no live passport
// divides by one rather than by zero: the answer is then the whole window's
// share, which is what the next credential to connect would get anyway.
func (c *passportShareCeiling) compute(ctx context.Context, wsID ids.WorkspaceID) int {
	monthly, err := c.budget.MonthlyTokenBudget(ctx, wsID)
	if err != nil || monthly <= 0 {
		return 0
	}
	live := c.livePassports(ctx)
	perWindow := float64(monthly) * (c.window.Hours() / (24 * budgetShareWindowDays))
	return int(perWindow / float64(live))
}

// livePassports counts the agent credentials sharing this workspace's budget.
// A failure answers one — the generous divisor — for the same reason the whole
// ceiling fails open: this counter's only effect is a sentence on an answer.
func (c *passportShareCeiling) livePassports(ctx context.Context) int {
	live := 1
	err := database.WithWorkspaceTx(ctx, c.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM passport WHERE revoked_at IS NULL AND expires_at > now()`).Scan(&n); err != nil {
			return err
		}
		if n > 1 {
			live = n
		}
		return nil
	})
	if err != nil {
		return 1
	}
	return live
}

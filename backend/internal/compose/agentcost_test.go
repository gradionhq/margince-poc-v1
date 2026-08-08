// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The arithmetic MCP-SESS-COST turns on. The budget is monthly and the window
// is a rolling day, so a share that skipped the pro-rating would compare a
// month's allowance against a day's spending and warn about nothing at all.
func TestTheShareIsAMonthlyBudgetProRatedToOneWindow(t *testing.T) {
	const monthly = 12_000_000 // ai.DefaultMonthlyTokens

	cases := []struct {
		name    string
		monthly int64
		live    int
		window  time.Duration
		want    int
	}{
		{"one credential, one day of a 30-day budget", monthly, 1, 24 * time.Hour, 400_000},
		{"four credentials share that day", monthly, 4, 24 * time.Hour, 100_000},
		{"an hour is a 24th of the day's share", monthly, 1, time.Hour, 16_666},
		{"no budget is no ceiling", 0, 1, 24 * time.Hour, 0},
		{"a negative budget is not a ceiling either", -5, 1, 24 * time.Hour, 0},
		{"no window is no ceiling", monthly, 1, 0, 0},
		// A zero divisor would panic on a float division that reads as harmless.
		{"no live credential answers no ceiling rather than dividing by zero", monthly, 0, 24 * time.Hour, 0},
	}
	for _, c := range cases {
		if got := shareOf(c.monthly, c.live, c.window); got != c.want {
			t.Errorf("%s: share is %d, want %d", c.name, got, c.want)
		}
	}
}

// The ceiling is CACHED per workspace, because it is consulted on the hot path
// of a counter that refuses nothing — and a soft control is not worth a database
// read per model call.
func TestTheShareIsCachedPerWorkspaceForTheCacheWindow(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	c := &passportShareCeiling{
		window: 24 * time.Hour, now: func() time.Time { return now },
		budget: ai.StaticBudget(12_000_000), pool: nil,
		cached: map[string]shareReading{},
	}
	ws := ids.New[ids.WorkspaceKind]().UUID
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	c.cached[ws.String()] = shareReading{tokens: 400_000, at: now}

	if got := c.TokensPerPassport(ctx); got != 400_000 {
		t.Errorf("a cached share read %d, want the cached 400000", got)
	}

	// Past the TTL the entry is recomputed. With no pool the divisor read
	// fails and answers one — the generous divisor — so the recomputed share is
	// the whole window's, which is a DIFFERENT number reached by the live path
	// rather than the cached one handed back.
	now = now.Add(shareCacheTTL + time.Minute)
	if got := c.TokensPerPassport(ctx); got != 400_000 {
		t.Errorf("a share past its TTL recomputed to %d, want the live 400000", got)
	}
	if c.cached[ws.String()].at != now {
		t.Error("the recomputed share was not written back, so every later call recomputes it too")
	}
}

// A ceiling with no budget policy composed answers no ceiling — it does not
// panic. Everything about this counter fails open, and the one thing it must
// never do is take a model call down with it.
func TestAShareWithNoBudgetPolicyIsNoCeilingRatherThanACrash(t *testing.T) {
	c := &passportShareCeiling{window: 24 * time.Hour, now: time.Now, cached: map[string]shareReading{}}
	ctx := principal.WithWorkspaceID(context.Background(), ids.New[ids.WorkspaceKind]().UUID)

	if got := c.TokensPerPassport(ctx); got != 0 {
		t.Errorf("a ceiling with no budget answered %d", got)
	}
}

// A call with no workspace bound has no budget to take a share of, and answers
// no ceiling rather than guessing at one.
func TestACallWithNoWorkspaceHasNoShare(t *testing.T) {
	c := &passportShareCeiling{window: 24 * time.Hour, now: time.Now, cached: map[string]shareReading{}}

	if got := c.TokensPerPassport(context.Background()); got != 0 {
		t.Errorf("a workspace-less call was given a share of %d", got)
	}
}

// The adapter charges the COST counter and nothing else. It is one line, and
// the line that matters is which counter it names: charging model tokens
// against reads would refuse an agent for spending its own budget.
func TestATokenSpendIsChargedAgainstTheCostCounter(t *testing.T) {
	meter := agentquota.Unmetered()

	if err := (AgentTokenSpend{Meter: meter}).SpendAgentTokens(context.Background(), 500); err != nil {
		t.Fatalf("charging an unmetered composition failed: %v", err)
	}
}

// EVERY REST outcome that reaches a handler is charged, and only those. This is
// the property three separate paths kept getting wrong: the plain admission was
// charged, the approved 🟡 retry was not, and update_record's field split
// returned early past the charge.
//
// Asserted on the PREDICATE rather than through the middleware, because what
// went wrong each time was the decision, not the plumbing: a path was added and
// nobody asked whether it ran a handler.
func TestEveryRestOutcomeThatRunsAHandlerIsCharged(t *testing.T) {
	withToken := httptest.NewRequest(http.MethodPatch, "/v1/deals/x", nil)
	withToken.Header.Set(approvalTokenHeader, "019fe200-0000-7000-8000-000000000000")
	without := httptest.NewRequest(http.MethodPatch, "/v1/deals/x", nil)

	cases := []struct {
		name string
		err  error
		req  *http.Request
		want bool
	}{
		{"an admitted call runs its handler", nil, without, true},
		{"an approved retry runs its handler too", apperrors.ErrRequiresApproval, withToken, true},
		{"a confirm-first refusal with no token stages and runs nothing", apperrors.ErrRequiresApproval, without, false},
		{"a scope refusal runs nothing", apperrors.ErrScopeExceeded, without, false},
		{"a quota refusal runs nothing", apperrors.ErrBudgetExceeded, withToken, false},
	}
	for _, c := range cases {
		if got := reachesAHandler(c.err, c.req); got != c.want {
			t.Errorf("%s: reachesAHandler = %v, want %v", c.name, got, c.want)
		}
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// recordingSpender is the quota meter as this module sees it.
type recordingSpender struct {
	tokens int
	calls  int
	err    error
}

func (r *recordingSpender) SpendAgentTokens(_ context.Context, tokens int) error {
	r.calls++
	r.tokens += tokens
	return r.err
}

func quietRouter() *Router {
	return &Router{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// The tokens a served call spent reach the agent's own counter. This is the
// whole of MCP-SESS-COST from this side: the workspace budget next door answers
// "is this installation spending too much" and cannot answer "is ONE connected
// agent taking a disproportionate share of it".
func TestAServedCallChargesItsTokensToTheAgentBehindIt(t *testing.T) {
	spend := &recordingSpender{}
	r := quietRouter().WithAgentTokenSpend(spend)

	r.spendAgentTokens(context.Background(), 1_250)

	if spend.tokens != 1_250 || spend.calls != 1 {
		t.Errorf("charged %d tokens over %d calls, want 1250 over 1", spend.tokens, spend.calls)
	}
}

// A router with no counter composed charges nothing — every role that serves no
// inbound agent is exactly that composition.
//
// Asserted as a DIFFERENCE against the same router that does charge, rather
// than as "it did not panic": absence of a crash is not evidence that the
// composition was honoured, and a version that charged some other counter
// instead would pass the weaker test.
func TestARouterWithNoAgentCounterChargesNothing(t *testing.T) {
	spend := &recordingSpender{}
	r := quietRouter().WithAgentTokenSpend(spend)
	r.spendAgentTokens(context.Background(), 500)
	if spend.calls != 1 {
		t.Fatalf("the baseline charge did not land (%d calls); the case below would prove nothing", spend.calls)
	}

	r.agentSpend = nil
	r.spendAgentTokens(context.Background(), 500)

	if spend.calls != 1 {
		t.Errorf("a router with no counter charged anyway (%d calls total)", spend.calls)
	}
}

// A call that spent nothing costs nothing. Worth pinning because the
// alternative — charging a floor per call — is the per-call metering this
// counter is not, and it would arrive by accident.
func TestACallThatSpentNoTokensIsNotCharged(t *testing.T) {
	spend := &recordingSpender{}
	r := quietRouter().WithAgentTokenSpend(spend)

	r.spendAgentTokens(context.Background(), 0)
	r.spendAgentTokens(context.Background(), -5)

	if spend.calls != 0 {
		t.Errorf("a zero-token call charged the counter %d times", spend.calls)
	}
}

// A counter that cannot be written NEVER fails the call. The provider has
// already been paid and the answer already exists; losing the accounting is a
// smaller loss than losing the answer, and the workspace budget one layer up is
// the control that actually acts on overspend.
func TestAnUnwritableCounterDoesNotFailAServedCall(t *testing.T) {
	spend := &recordingSpender{err: errors.New("redis is unreachable")}
	r := quietRouter().WithAgentTokenSpend(spend)

	r.spendAgentTokens(context.Background(), 900) // must not panic and must not propagate

	if spend.calls != 1 {
		t.Errorf("the charge was not attempted (%d calls)", spend.calls)
	}
}

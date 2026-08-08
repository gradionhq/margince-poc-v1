// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// MCP-SESS-COST from this side: what a model call costs the AGENT that caused
// it, as distinct from what it costs the workspace.
//
// The workspace's own budget is already metered next door (ai_usage, the §1.3
// utilization bands). That answers "is this installation spending too much" and
// cannot answer "is ONE connected agent spending a disproportionate share of
// it", which is the question api-rate-limits §2.2 asks. The two counters are
// different questions over the same tokens, so this records into the second
// rather than re-deriving it from the first.

import "context"

// AgentTokenSpender records model tokens against the calling agent's own
// per-Passport window. It is a narrow seam rather than the quota meter itself so
// this module stays unaware of how that window is keyed or stored; compose
// bridges the two.
//
// A call with no agent principal records nothing — the implementation decides
// that, not this module, because "which callers are governed" is the quota's own
// rule and a second copy of it here would be a second answer.
type AgentTokenSpender interface {
	SpendAgentTokens(ctx context.Context, tokens int) error
}

// WithAgentTokenSpend installs the per-Passport cost counter. A router without
// one meters the workspace and nothing else, which is every role that serves no
// inbound agents.
func (r *Router) WithAgentTokenSpend(spend AgentTokenSpender) *Router {
	r.agentSpend = spend
	return r
}

// spendAgentTokens charges one served call to the agent behind it.
//
// It NEVER fails the call, and that is the difference between this counter and
// every other one on the surface. MCP-SESS-COST is soft by the spec's own word:
// its purpose is that a disproportionate share becomes visible, and the control
// that acts on overspend is the workspace budget guardrail one layer up, which
// meters separately and is not weakened by a miss here. Failing a served model
// call because a soft counter could not be written would turn an accounting
// gap into a lost answer the caller already paid a provider for.
func (r *Router) spendAgentTokens(ctx context.Context, tokens int) {
	if r.agentSpend == nil || tokens <= 0 {
		return
	}
	if err := r.agentSpend.SpendAgentTokens(ctx, tokens); err != nil {
		r.log.WarnContext(ctx, "ai: recording an agent's token spend against its own share failed",
			"tokens", tokens, "err", err)
	}
}

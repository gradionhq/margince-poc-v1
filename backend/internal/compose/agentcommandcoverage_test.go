// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The completeness gate for the governance seam: every operation an agent can
// MUTATE anything through decodes into a typed command, and nothing else does.
//
// It replaces four family-shaped walks — one per operation family, each with
// its own filter (a route shape, a tier, a tool verb) and its own remembered
// count. Between them they happened to cover the whole surface, but a set of
// partial gates that unions to completeness is not a completeness gate: the
// filters were written independently, and a route none of them selected was
// covered by none of them. updateOfferTemplate spent two tasks in exactly that
// position, because one walk filtered on the method PATCH and its route is a
// PUT, while the walk that would otherwise have caught it stood down on the
// grounds that the first one had it.
//
// So there is one subject set here and no filter inside it: Access "tool" and
// a mutating method, which is precisely what the gate admits an agent to and
// therefore precisely what may reach staging. The per-family tests that remain
// assert something this one does not — decoding a real body, staging the right
// target — and each says so where it stands.

import (
	"strings"
	"testing"
)

// agentReachableMutations is the subject set, derived: every route an agent
// principal can reach with a mutating method, keyed by operationId.
//
// Keyed by OP rather than by route because that is the key restCommands is
// keyed by, and the gate below compares the two sets directly. The route comes
// along as the value so a failure names the place a reader has to go.
func agentReachableMutations() map[string]string {
	routes := make(map[string]string, len(agentPolicies))
	for route, pol := range agentPolicies {
		method, _, _ := strings.Cut(route, " ")
		if pol.Access != accessTool || !mutatingMethod(method) {
			continue
		}
		routes[pol.Op] = route
	}
	return routes
}

// The seam's whole claim, in one assertion: what an agent can mutate and what
// this door can describe are the same set.
//
// Both directions, because either alone is satisfied by a table that is wrong
// in the other. A missing entry is an operation whose staged approval nothing
// can say what it binds to — the refusal a human is asked to release names a
// target the door never resolved. A surplus entry is a decoder answering for an
// operation no agent reaches, which reads as coverage and is not: the same
// entry would go on looking correct after the contract retired the route it
// belongs to.
//
// The count is derived on both sides rather than written down. A remembered
// literal is a second source of truth about the contract, and it goes stale in
// the direction that reads as success: a route the contract adds fails a count
// of 69 with a message about arithmetic, where what actually happened is that
// an operation arrived with no governed call.
func TestEveryAgentReachableMutatingRouteDecodesIntoACommand(t *testing.T) {
	reachable := agentReachableMutations()
	if len(reachable) == 0 {
		t.Fatal("the policy table declares no agent-reachable mutating route at all — this gate checked nothing")
	}
	for op, route := range reachable {
		if _, described := restCommands[op]; !described {
			t.Errorf("%s (%s) is a mutating operation an agent can reach, and it decodes into no command — "+
				"nothing can say what an approval of it would bind to, so the call is refused at staging "+
				"rather than staged. Register it in restCommands (agentcommand.go).", route, op)
		}
	}
	for op := range restCommands {
		if _, reaches := reachable[op]; !reaches {
			t.Errorf("restCommands[%q] decodes an operation no agent can mutate through — it is either a "+
				"retired operationId or a route the contract no longer annotates for agents, and it will go "+
				"on reading as coverage for as long as it sits there", op)
		}
	}
}

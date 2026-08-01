// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A verb in the generated policy table with no registered tool is not refused:
// operationSpec synthesizes a spec for it at the write scope. That is right for
// an internal, reversible operation and wrong for anything leaving the
// workspace — an outbound verb admitted under `write` ignores the cap the
// granting human set on the passport.
//
// The maps below are a pin, not a description: a new synthesized verb fails
// this test until someone writes down why it may be one.

import (
	"sort"
	"testing"
)

// synthesizedVerbs maps each verb that legitimately resolves without a
// registered tool to the reason it may. An entry claims `write` is the right
// cap for it.
var synthesizedVerbs = map[string]string{
	"advance_project_phase": "internal state change on a project; reversible by advancing again",
	"draft_offer":           "regenerates a draft in place; nothing leaves the workspace",
	"relink_activity":       "moves an activity between records; internal and reversible",
	"render_offer":          "renders a document for the caller; no transmission",
	"share_record":          "grants and revokes access inside the workspace",
	"disqualify_lead":       "internal lifecycle change; the row survives disqualification",
}

// outboundHoles are synthesized verbs that DO leave the workspace and are
// admitted under `write` today. The value names the scope each should carry, so
// the pin reads as the debt it is rather than as approval. Closing one means
// registering a tool for it (as send_message was) and deleting its line.
var outboundHoles = map[string]string{
	"send_offer":           "send",
	"enrich":               "enrich",
	"connect_incumbent":    "write is likely right for authenticating a connection, but the tier and scope were never decided together — see DESIGN §3.5",
	"disconnect_incumbent": "as connect_incumbent",
	"reconcile_overlay":    "enrich — the sweep it queues calls the incumbent's API and spends its budget (overlay/reconcile.go:106)",
}

func TestEverySynthesizedVerbIsPinned(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	unexplained := []string{}
	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		if _, registered := registry.Spec(pol.Tool); registered {
			continue
		}
		if _, pinned := synthesizedVerbs[pol.Tool]; pinned {
			continue
		}
		if _, known := outboundHoles[pol.Tool]; known {
			continue
		}
		unexplained = append(unexplained, pol.Tool+" ("+route+")")
	}

	sort.Strings(unexplained)
	for _, verb := range unexplained {
		t.Errorf("%s has no registered tool and no pin: it is admitted at the write scope by "+
			"synthesis. Register a tool for it, or add it to synthesizedVerbs with the reason "+
			"`write` is the right cap.", verb)
	}
}

// The pins must not outlive what they describe. A verb that gains a tool, or
// leaves the contract, loses its line — otherwise the pin becomes a list of
// things that used to be true.
func TestThePinsDescribeVerbsThatStillExist(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	inTable := map[string]bool{}
	for _, pol := range agentPolicies {
		if pol.Access == accessTool {
			inTable[pol.Tool] = true
		}
	}

	for _, pins := range []map[string]string{synthesizedVerbs, outboundHoles} {
		for verb := range pins {
			if !inTable[verb] {
				t.Errorf("%s is pinned but no longer appears in the policy table; delete the pin", verb)
			}
			if _, registered := registry.Spec(verb); registered {
				t.Errorf("%s is pinned as synthesized but now has a registered tool; delete the pin", verb)
			}
		}
	}
}

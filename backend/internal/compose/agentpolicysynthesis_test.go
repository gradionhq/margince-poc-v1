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

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// synthesizedVerbs maps each verb that legitimately resolves without a
// registered tool to the reason it may. An entry claims `write` is the right
// cap for it.
var synthesizedVerbs = gatekit.Waive(map[string]string{
	"advance_project_phase": "internal state change on a project; reversible by advancing again",
	"draft_offer":           "regenerates a draft in place — no message is transmitted to a counterparty; the AI lane may send the deal's context to the configured model provider, but that is governed by model routing and budget, not the send scope",
	"relink_activity":       "moves an activity between records; internal and reversible",
	"render_offer":          "renders a document for the caller; no transmission",
	"disqualify_lead":       "internal lifecycle change; the row survives disqualification",
	"disconnect_incumbent":  "revokes the connection row, purges the mirror, and flips the workspace to native mode, all local; the sealed vault credential it deletes afterward is our own Postgres-backed store, not the incumbent — nothing here calls out. Contrast connect_incumbent (outboundHoles), which calls the incumbent's API and writes that credential in the first place",
})

// outboundHoles are synthesized verbs that DO leave the workspace and are
// admitted under `write` today. The value names the scope each should carry
// — or, where that has not been decided, says so and why — so the pin reads
// as the debt it is rather than as approval. Closing one means registering a
// tool for it (as send_message was) and deleting its line.
var outboundHoles = gatekit.Waive(map[string]string{
	// send_offer only flips the offer's status to sent, freezes fx_rate_to_base
	// and the buyer/issuer snapshots, and emits offer.sent
	// (deals/offer_lifecycle.go:39-96) — no delivery, no counterparty
	// transport. The contract's summary at api/crm.yaml (sendOffer) promises
	// sending "leaves the workspace"; the implementation does not perform
	// that leaving. It stays here because a delivery mechanism is the
	// intended shape (ADR context: an outbound send), not because today's
	// code egresses — this is a contract/implementation discrepancy to
	// reconcile upstream, not merely scope-registration debt.
	"send_offer": "send — pinned for what the contract promises, though the current implementation performs no delivery (see comment above)",
	"enrich": "enrich — scrapeCompany and deepReadCompany fetch " +
		"the target's own website through the web-read seam, and both " +
		"coldstart operations do the same before any record exists, so " +
		"the call egresses; nothing is transmitted to a counterparty, " +
		"which is why it still reads as an internal write today",
	"connect_incumbent": "write is likely right for authenticating a connection, but the tier and scope were never decided together",
	"reconcile_overlay": "enrich — the sweep it queues calls overlay.Reconcile's inc.Modified fetch against the incumbent's API and spends its budget",
})

// deadEndVerbs are synthesized verbs whose approved-and-redeemed agent call
// can never execute: the handler behind them requires a human principal, so
// staging one, having a human approve it, and redeeming it as the agent that
// staged it always ends in refusal at the store. They are pinned here —
// distinct from synthesizedVerbs (an internal `write` is the correct cap)
// and outboundHoles (egress admitted under the wrong scope) — because
// neither of those labels is true of them: the verb is reachable, resolves
// at `write` by synthesis, and yet no agent call through it can ever land.
var deadEndVerbs = gatekit.Waive(map[string]string{
	"share_record": "createRecordGrant/revokeRecordGrant (identity/grants.go:141,189) reject any principal.Actor whose Type is not PrincipalHuman. Redemption (agents.RedeemAndMark, compose/agentgate.go) marks the context as released but never changes the actor's type — the redeeming caller is still the staging agent — so an agent-staged, human-approved share_record is refused at redemption every time. It is not outbound (the grant never leaves the workspace) and not a legitimate write cap (no agent call through it can succeed), so it belongs in neither other map.",
})

func TestEverySynthesizedVerbIsPinned(t *testing.T) {
	// This sweep visits every synthesized verb in the policy table, so it is the
	// one place all three maps' staleness is owned. A pinned verb that left the
	// table, or that gained a registered tool, is skipped by the loop below and
	// so goes unmatched — this sweep reports it stale on its own.
	// TestThePinsDescribeVerbsThatStillExist names WHICH of the two happened,
	// sharpening the diagnosis rather than carrying the detection.
	defer synthesizedVerbs.AssertAllMatched(t)
	defer outboundHoles.AssertAllMatched(t)
	defer deadEndVerbs.AssertAllMatched(t)

	registry := NewRegistry(nil, SendPath{})

	checked := 0
	unexplained := []string{}
	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		if _, registered := registry.Spec(pol.Tool); registered {
			continue
		}
		checked++
		if synthesizedVerbs.Waived(t, pol.Tool) {
			continue
		}
		if outboundHoles.Waived(t, pol.Tool) {
			continue
		}
		if deadEndVerbs.Waived(t, pol.Tool) {
			continue
		}
		unexplained = append(unexplained, pol.Tool+" ("+route+")")
	}
	if checked == 0 {
		t.Fatal("no synthesized tool routes in the generated policy — the pin no longer covers anything")
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

	for _, pins := range []*gatekit.Waivers[string]{synthesizedVerbs, outboundHoles, deadEndVerbs} {
		for _, verb := range pins.Subjects() {
			if !inTable[verb] {
				t.Errorf("%s is pinned but no longer appears in the policy table; delete the pin", verb)
			}
			if _, registered := registry.Spec(verb); registered {
				t.Errorf("%s is pinned as synthesized but now has a registered tool; delete the pin", verb)
			}
		}
	}
}

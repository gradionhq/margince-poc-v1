// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A verb in the generated policy table with no registered tool is not refused:
// operationSpec synthesizes a spec for it at the cap the contract declares for
// it. Synthesis is therefore a gap in the TOOL surface, not in the governing —
// the verb is reachable over REST with no implementation to reach over MCP.
//
// The maps below are a pin, not a description: a new synthesized verb fails
// this test until someone writes down why it may be one.

import (
	"sort"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// synthesizedVerbs maps each verb that legitimately resolves without a
// registered tool to the reason it may. The cap is not this map's claim: the
// contract names a scope on every tool route and operationSpec admits the
// synthesized spec at exactly that scope. An entry records only that no tool
// is registered for the verb, and why that is acceptable.
var synthesizedVerbs = gatekit.Waive(map[string]string{
	"advance_project_phase": "internal state change on a project; reversible by advancing again",
	"draft_offer":           "regenerates a draft in place — no message is transmitted to a counterparty; the AI lane may send the deal's context to the configured model provider, but that is governed by model routing and budget, not the send scope",
	"relink_activity":       "moves an activity between records; internal and reversible",
	"render_offer":          "renders a document for the caller; no transmission",
	"disqualify_lead":       "internal lifecycle change; the row survives disqualification",
	"disconnect_incumbent":  "revokes the connection row, purges the mirror, and flips the workspace to native mode, all local; the sealed vault credential it deletes afterward is our own Postgres-backed store, not the incumbent — nothing here calls out",
	"send_offer": "no tool is registered for it; the contract declares the send cap, which is the strictest thing the act could need. " +
		"Today's handler only flips the offer's status, freezes fx_rate_to_base and the buyer/issuer snapshots, and emits " +
		"offer.sent (deals/offer_lifecycle.go:39-96) — there is no delivery transport yet, though the contract's sendOffer " +
		"summary already promises the send leaves the workspace. That is a contract/implementation discrepancy to reconcile " +
		"upstream, not a governing gap",
	"enrich": "no tool is registered for it; the contract declares the enrich cap and the act spends it — scrapeCompany and " +
		"deepReadCompany fetch the target's own website through the web-read seam, and both coldstart operations do the same " +
		"before any record exists, so the call reaches a third-party site",
	"connect_incumbent": "no tool is registered for it; the contract declares write, deliberately, even though sealing the " +
		"connection does call the incumbent's API (overlay/connection.go:314-323,341). The declared scope is the cap the act's " +
		"PURPOSE spends: enrich and send are for acts whose point IS the outbound call, while an act whose point is a durable " +
		"state change declares write even when it makes network calls to get there. connect seals a credential and flips " +
		"x_sor_mode; the fetches are incidental to that",
	"reconcile_overlay": "no tool is registered for it; the contract declares enrich and the act spends it — the sweep it queues " +
		"calls overlay.Reconcile's inc.Modified fetch against the incumbent's API and spends its budget",
})

// deadEndVerbs are synthesized verbs whose approved-and-redeemed agent call
// can never execute: the handler behind them requires a human principal, so
// staging one, having a human approve it, and redeeming it as the agent that
// staged it always ends in refusal at the store. They are pinned apart from
// synthesizedVerbs because that map's premise — the verb merely lacks a tool
// — is too kind to them: the verb is reachable, resolves by synthesis at the
// cap the contract declares, and yet no agent call through it can ever land.
var deadEndVerbs = gatekit.Waive(map[string]string{
	"share_record": "createRecordGrant/revokeRecordGrant (identity/grants.go:141,189) reject any principal.Actor whose Type is not PrincipalHuman. Redemption (agents.RedeemAndMark, compose/agentgate.go) marks the context as released but never changes the actor's type — the redeeming caller is still the staging agent — so an agent-staged, human-approved share_record is refused at redemption every time. The grant never leaves the workspace, so the write cap the contract declares is the right one; what is wrong is that no agent call through it can succeed at all, which is why it is pinned here rather than as an ordinary missing tool.",
})

func TestEverySynthesizedVerbIsPinned(t *testing.T) {
	// This sweep visits every synthesized verb in the policy table, so it is the
	// one place both maps' staleness is owned. A pinned verb that left the
	// table, or that gained a registered tool, is skipped by the loop below and
	// so goes unmatched — this sweep reports it stale on its own.
	// TestThePinsDescribeVerbsThatStillExist names WHICH of the two happened,
	// sharpening the diagnosis rather than carrying the detection.
	defer synthesizedVerbs.AssertAllMatched(t)
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
		t.Errorf("%s has no registered tool: the gate synthesizes a spec for it at the cap the "+
			"contract declares. Register a tool for it, or add it to synthesizedVerbs with the "+
			"reason the tool surface may keep missing it.", verb)
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

	for _, pins := range []*gatekit.Waivers[string]{synthesizedVerbs, deadEndVerbs} {
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

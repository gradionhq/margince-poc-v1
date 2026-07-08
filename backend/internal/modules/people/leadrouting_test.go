// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The pure routing decision (features/03 §3 AC-S5): rules outrank
// round-robin, caps outrank everything, and the rotation stays fair
// within ±1. The transactional shell around this function has its own
// integration suite; here the decision table itself is the spec.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func routingPool(n int) ([]ids.UserID, map[ids.UserID]bool) {
	owners := make([]ids.UserID, n)
	active := map[ids.UserID]bool{}
	for i := range owners {
		owners[i] = ids.New[ids.UserKind]()
		active[owners[i]] = true
	}
	return owners, active
}

func TestChooseOwnerRoundRobinRotatesFairlyWithinOne(t *testing.T) {
	owners, active := routingPool(3)
	cfg := RoutingConfig{Owners: owners}
	load := map[ids.UserID]int{}

	var sequence []ids.UserID
	for range 7 {
		chosen, reason, ok := chooseOwner(cfg, leadRoutingFacts{}, load, active)
		if !ok || reason != "round_robin" {
			t.Fatalf("expected a round_robin pick, got ok=%v reason=%q", ok, reason)
		}
		sequence = append(sequence, chosen)
		load[chosen]++
	}

	// The rotation order is pinned: pool order, wrapping.
	for i, chosen := range sequence {
		if chosen != owners[i%3] {
			t.Fatalf("assignment %d went to pool[%d], want pool[%d] — rotation order broke", i, indexOf(owners, chosen), i%3)
		}
	}
	// 7 across 3 owners: 3/2/2 — never more than ±1 apart.
	if load[owners[0]] != 3 || load[owners[1]] != 2 || load[owners[2]] != 2 {
		t.Fatalf("distribution %d/%d/%d, want 3/2/2", load[owners[0]], load[owners[1]], load[owners[2]])
	}
}

func indexOf(owners []ids.UserID, id ids.UserID) int {
	for i, o := range owners {
		if o == id {
			return i
		}
	}
	return -1
}

func TestChooseOwnerSkipsCappedOwnerAndStopsWhenAllAreFull(t *testing.T) {
	owners, active := routingPool(3)
	cfg := RoutingConfig{Owners: owners, CapPerOwner: 2}
	load := map[ids.UserID]int{owners[0]: 2, owners[1]: 1, owners[2]: 2}

	chosen, _, ok := chooseOwner(cfg, leadRoutingFacts{}, load, active)
	if !ok || chosen != owners[1] {
		t.Fatalf("the only under-cap owner is pool[1]; got pool[%d] ok=%v", indexOf(owners, chosen), ok)
	}

	load[owners[1]] = 2
	if _, _, ok := chooseOwner(cfg, leadRoutingFacts{}, load, active); ok {
		t.Fatal("every owner at cap must yield no assignment, never an over-cap one")
	}
}

func TestChooseOwnerRuleOutranksRoundRobinButNeverTheCap(t *testing.T) {
	owners, active := routingPool(2)
	ruleOwner := ids.New[ids.UserKind]()
	active[ruleOwner] = true
	cfg := RoutingConfig{
		Owners:      owners,
		CapPerOwner: 1,
		Rules:       []RoutingRule{{Field: "source", Equals: "webinar", OwnerID: ruleOwner}},
	}

	// The matching rule wins over the emptier pool (match is
	// case-insensitive: sources are free text).
	chosen, reason, ok := chooseOwner(cfg, leadRoutingFacts{Source: "Webinar"}, map[ids.UserID]int{}, active)
	if !ok || chosen != ruleOwner || reason != "rule:0:source" {
		t.Fatalf("webinar lead routed to %v via %q, want the rule owner via rule:0:source", chosen, reason)
	}

	// A non-matching lead ignores the rule.
	chosen, reason, ok = chooseOwner(cfg, leadRoutingFacts{Source: "cold_outbound"}, map[ids.UserID]int{}, active)
	if !ok || chosen != owners[0] || reason != "round_robin" {
		t.Fatalf("non-matching lead routed to %v via %q, want pool[0] via round_robin", chosen, reason)
	}

	// The rule owner at cap: the cap wins, the lead falls to the pool.
	chosen, reason, ok = chooseOwner(cfg, leadRoutingFacts{Source: "webinar"}, map[ids.UserID]int{ruleOwner: 1}, active)
	if !ok || chosen != owners[0] || reason != "round_robin" {
		t.Fatalf("capped rule owner must fall through to the pool; got %v via %q", chosen, reason)
	}
}

func TestChooseOwnerIgnoresInactiveOwners(t *testing.T) {
	owners, active := routingPool(2)
	active[owners[0]] = false // suspended/deactivated/archived: cannot take work
	cfg := RoutingConfig{Owners: owners}

	chosen, _, ok := chooseOwner(cfg, leadRoutingFacts{}, map[ids.UserID]int{}, active)
	if !ok || chosen != owners[1] {
		t.Fatalf("inactive pool[0] must be skipped; got pool[%d] ok=%v", indexOf(owners, chosen), ok)
	}
}

func TestParseRoutingConfigRoundTripsAndRejectsMalformedParams(t *testing.T) {
	owner := ids.New[ids.UserKind]()
	params, err := json.Marshal(map[string]any{
		"owners":        []string{owner.String()},
		"cap_per_owner": 3,
		"rules":         []map[string]any{{"field": "source", "equals": "webinar", "owner_id": owner.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseRoutingConfig(params)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Owners) != 1 || cfg.Owners[0] != owner || cfg.CapPerOwner != 3 ||
		len(cfg.Rules) != 1 || cfg.Rules[0].OwnerID != owner || !cfg.Configured() {
		t.Fatalf("config lost fields in decode: %+v", cfg)
	}

	empty, err := ParseRoutingConfig(nil)
	if err != nil || empty.Configured() {
		t.Fatalf("no params must decode as unconfigured (err=%v, cfg=%+v)", err, empty)
	}

	if _, err := ParseRoutingConfig(json.RawMessage(`{"owners": "not-a-list"`)); err == nil {
		t.Fatal("a malformed blob must fail the decode, not route on a half-read config")
	}
}

// The engine's field accessor must resolve exactly RoutableLeadFields —
// the closed set the agents catalog mirrors. A member the accessor does
// not handle (returns "") would silently never match a rule; a key the
// accessor handles but the set omits would let a config through that the
// editor 422s. This binds the switch to the declared vocabulary.
func TestFieldResolvesExactlyRoutableLeadFields(t *testing.T) {
	facts := leadRoutingFacts{Source: "s", CompanyName: "c", CandidateOrgKey: "k"}
	for _, name := range RoutableLeadFields {
		if facts.field(name) == "" {
			t.Errorf("routable field %q is not resolved by leadRoutingFacts.field", name)
		}
	}
	if facts.field("owner_id") != "" || facts.field("") != "" {
		t.Error("a field outside the routable set must resolve to \"\" (never matches)")
	}
}

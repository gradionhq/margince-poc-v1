// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// spec builds the two fields the derivation reads. Nothing else about a tool
// decides which quota it spends, which is the property under test.
func spec(scope principal.Scope, egress bool) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "t", Title: "T", Description: "d", Version: "1",
		RequiredScope: scope, Egress: egress,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

// The ordering defect this derivation exists to avoid: every egress tool on
// this surface also mutates, so a derivation that asks "does it write?" first
// charges send_email against the 200-call write quota and leaves the 20-call
// egress quota — the tightest one on the surface, guarding the exfiltration
// endpoint — permanently unspent. Written as a table because the whole point is
// that no tool NAME appears in the answer.
func TestAnEgressToolSpendsTheEgressQuotaEvenThoughItAlsoWrites(t *testing.T) {
	cases := []struct {
		name   string
		spec   mcp.ToolSpec
		expect Counter
	}{
		{"a send that also writes an activity", spec(principal.ScopeSend, true), Egress},
		{"a read-only tool", spec(principal.ScopeRead, false), Reads},
		{"an ordinary mutation", spec(principal.ScopeWrite, false), Writes},
		{"a draft, which the scope model does not call read-only", spec(principal.ScopeDraft, false), Writes},
		{"a read-scoped tool flagged egress anyway", spec(principal.ScopeRead, true), Egress},
	}
	for _, c := range cases {
		if got := CounterFor(c.spec); got != c.expect {
			t.Errorf("%s: charged %s, want %s", c.name, got, c.expect)
		}
	}
}

// Which quotas a human can widen mid-window IS the §2.4 ladder, and getting it
// backwards is the difference between a visible gated event and an unbounded
// one. Egress is the exfiltration endpoint and calls is the ceiling under which
// every other quota sits — releasing either would release the control itself.
func TestOnlyTheStepUpQuotasCanBeReleasedByAHuman(t *testing.T) {
	releasable := map[Counter]bool{
		Reads: true, Writes: true,
		Egress: false, Calls: false, Cost: false,
	}
	for c, want := range releasable {
		if got := c.Releasable(); got != want {
			t.Errorf("%s.Releasable() = %v, want %v", c, got, want)
		}
	}
}

// Cost refuses nothing — the spec's own word for it is "soft" — so it must not
// sit in the set the gate turns away callers on. A cost counter that reported
// itself governed would hard-stop an agent on a budget share, which no rung of
// the ladder asks for.
func TestOnlyTheHardQuotasGovernAdmission(t *testing.T) {
	for _, c := range []Counter{Reads, Writes, Egress, Calls} {
		if !c.Governed() {
			t.Errorf("%s does not govern admission, so nothing refuses on it", c)
		}
	}
	if Cost.Governed() {
		t.Error("cost governs admission; the spec makes it soft, and a refusal on it is a rung nobody wrote")
	}
}

// A zero or negative configured threshold would make every call of that kind
// refuse. Each one falls back to the spec's default INDEPENDENTLY, so an
// operator setting one counter does not silently unbound — or suspend — the
// other three.
func TestAnUnusableThresholdFallsBackWithoutDisturbingItsNeighbours(t *testing.T) {
	got := Limits{Reads: 0, Writes: -1, Egress: 5}.withDefaults()

	if got.Reads != DefaultReads {
		t.Errorf("a zero read limit resolved to %d, not the %d default", got.Reads, DefaultReads)
	}
	if got.Writes != DefaultWrites {
		t.Errorf("a negative write limit resolved to %d, not the %d default", got.Writes, DefaultWrites)
	}
	if got.Egress != 5 {
		t.Errorf("a configured egress limit of 5 resolved to %d", got.Egress)
	}
	if got.Calls != DefaultCalls {
		t.Errorf("an unset call limit resolved to %d, not the %d default", got.Calls, DefaultCalls)
	}
}

// The thresholds are the spec's, and they are security controls rather than
// performance knobs (§4.2 makes lowering one below its floor an ADR matter).
// Pinned so a change to any of them is a deliberate edit with a failing test in
// front of it rather than a tuning commit.
func TestTheDefaultThresholdsAreTheOnesTheSpecNames(t *testing.T) {
	l := Limits{}.withDefaults()
	for _, c := range []struct {
		counter Counter
		want    int
	}{{Reads, 2000}, {Writes, 200}, {Egress, 20}, {Calls, 1000}} {
		if got := l.of(c.counter); got != c.want {
			t.Errorf("%s defaults to %d, and api-rate-limits §2.2 says %d", c.counter, got, c.want)
		}
	}
}

// Cost has no configured threshold here on purpose: its ceiling is a share of
// the workspace's own AI budget, resolved per call. A Limits entry for it would
// be a second, deployment-wide answer to a question only the workspace can
// answer, and the two would disagree.
func TestCostHasNoDeploymentWideThreshold(t *testing.T) {
	got := Limits{}.withDefaults().of(Cost)
	if got != 0 {
		t.Errorf("cost resolved a fixed threshold of %d; its ceiling is the workspace's budget share", got)
	}
}

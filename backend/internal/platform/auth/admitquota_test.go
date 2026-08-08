// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// spentQuota is the meter as the gate sees it: a fixed answer per counter, so a
// test says what the meter concluded without standing up Redis to conclude it.
// It records which counters were ASKED, because half the properties here are
// about a counter never being consulted at all.
type spentQuota struct {
	exceeded map[agentquota.Counter]agentquota.Reading
	asked    []agentquota.Counter
}

func (s *spentQuota) Read(_ context.Context, c agentquota.Counter) agentquota.Reading {
	s.asked = append(s.asked, c)
	if reading, ok := s.exceeded[c]; ok {
		return reading
	}
	return agentquota.Reading{Counter: c, Limit: 100}
}

func (s *spentQuota) askedFor(c agentquota.Counter) bool {
	for _, asked := range s.asked {
		if asked == c {
			return true
		}
	}
	return false
}

// over builds a meter that reports one counter crossed and every other with
// headroom — the shape every rung of the ladder is tested against.
func over(c agentquota.Counter, observed, limit int) *spentQuota {
	return &spentQuota{exceeded: map[agentquota.Counter]agentquota.Reading{
		c: {Counter: c, Observed: observed, Limit: limit, Exceeded: true, Bucket: 7},
	}}
}

func quotaGate(q *spentQuota) *Gate {
	return NewGate(&stubAuthority{seat: principal.SeatFull}, WithQuota(q))
}

var (
	readSpec   = mcp.ToolSpec{Name: "search_records", RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute}
	writeSpec  = mcp.ToolSpec{Name: "update_record", RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute}
	egressSpec = mcp.ToolSpec{Name: "send_email", RequiredScope: principal.ScopeSend, Tier: mcp.TierAutoExecute, Egress: true}
)

// AC-MCP-1: an agent past MCP-SESS-READS is stepped up on its NEXT read call.
// The sentinel is the spec's own for MCP-SESS-* (interfaces.md §0,
// ErrBudgetExceeded), so a client branches on the registry rather than on
// prose.
func TestAReadIsRefusedOnceTheAgentHasPassedItsReadBound(t *testing.T) {
	gate := quotaGate(over(agentquota.Reads, 2001, 2000))

	_, err := gate.Admit(agentCtx(principal.ScopeRead), readSpec, noResolve)

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("a read past the bound → %v, want ErrBudgetExceeded", err)
	}
	// The numbers are what a human is asked to confirm against, so they have to
	// reach the caller rather than only the log.
	if msg := err.Error(); !strings.Contains(msg, "2001") || !strings.Contains(msg, "2000") {
		t.Errorf("the refusal does not say what was read against what limit: %q", msg)
	}
}

// Each rung of the §2.4 ladder refuses the call it is written for and leaves
// the others alone. A shared refusal would mean one busy counter took the whole
// surface down — and, in the other direction, that a heavy reader never reached
// the send ceiling that actually guards exfiltration.
func TestEachQuotaRefusesOnlyTheCallsItGoverns(t *testing.T) {
	cases := []struct {
		counter agentquota.Counter
		refused mcp.ToolSpec
		allowed []mcp.ToolSpec
	}{
		{agentquota.Reads, readSpec, []mcp.ToolSpec{writeSpec, egressSpec}},
		{agentquota.Writes, writeSpec, []mcp.ToolSpec{readSpec, egressSpec}},
		{agentquota.Egress, egressSpec, []mcp.ToolSpec{readSpec, writeSpec}},
	}
	for _, c := range cases {
		gate := quotaGate(over(c.counter, 500, 100))
		if _, err := gate.Admit(agentCtx(scopeFor(c.refused)), c.refused, noResolve); !errors.Is(err, apperrors.ErrBudgetExceeded) {
			t.Errorf("%s past its bound: %s → %v, want ErrBudgetExceeded", c.counter, c.refused.Name, err)
		}
		for _, allowed := range c.allowed {
			gate := quotaGate(over(c.counter, 500, 100))
			if _, err := gate.Admit(agentCtx(scopeFor(allowed)), allowed, noResolve); err != nil {
				t.Errorf("%s past its bound refused %s, which it does not govern: %v", c.counter, allowed.Name, err)
			}
		}
	}
}

// scopeFor gives the caller exactly the scope its tool needs, so a quota test
// never accidentally proves the scope check instead.
func scopeFor(spec mcp.ToolSpec) principal.Scope { return spec.RequiredScope }

// An egress tool is charged against EGRESS even though it also mutates. Proven
// through the gate rather than by calling the derivation, because the derivation
// being right is worth nothing if the gate consults a different one: a surface
// that asked "does it write?" first would leave the 20-call send ceiling
// permanently unspent while the 200-call write quota absorbed every send.
func TestASendIsJudgedAgainstTheSendCeilingAndNotTheWriteOne(t *testing.T) {
	quota := over(agentquota.Writes, 500, 200)

	if _, err := quotaGate(quota).Admit(agentCtx(principal.ScopeSend), egressSpec, noResolve); err != nil {
		t.Fatalf("a send was refused by the WRITE quota: %v", err)
	}
	if quota.askedFor(agentquota.Writes) {
		t.Error("the write quota was consulted for an egress-flagged tool; the send ceiling is the one that governs it")
	}
	if !quota.askedFor(agentquota.Egress) {
		t.Error("the egress quota was never consulted for an egress-flagged tool")
	}
}

// BYO-STEP-4's suspension is the ceiling every other quota sits under, so it is
// asked FIRST — and a suspended caller is refused for every verb rather than
// being told which of its allowances still has headroom.
func TestASuspendedPassportIsRefusedForEveryVerbAndLearnsNothingElse(t *testing.T) {
	for _, spec := range []mcp.ToolSpec{readSpec, writeSpec, egressSpec} {
		quota := over(agentquota.Calls, 1001, 1000)

		_, err := quotaGate(quota).Admit(agentCtx(scopeFor(spec)), spec, noResolve)

		var refusal *QuotaExceededError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s under suspension → %v, want a quota refusal", spec.Name, err)
		}
		if refusal.Reading.Counter != agentquota.Calls {
			t.Errorf("%s under suspension was refused on %s, not on the call ceiling", spec.Name, refusal.Reading.Counter)
		}
		if quota.askedFor(agentquota.CounterFor(spec)) {
			t.Errorf("a suspended caller learned the state of its %s allowance", agentquota.CounterFor(spec))
		}
	}
}

// The ladder's two halves must be tellable apart by a TYPE, not by prose: one
// refusal has somewhere to go — the surface stages the question for the human
// who lent the Passport — and the other does not. A caller matching on wording
// would eventually stage a release for a quota nothing can release.
func TestARefusalSaysWhetherAHumanCanAnswerIt(t *testing.T) {
	releasable := map[agentquota.Counter]bool{
		agentquota.Reads: true, agentquota.Writes: true,
		agentquota.Egress: false, agentquota.Calls: false,
	}
	for counter, want := range releasable {
		spec := readSpec
		switch counter {
		case agentquota.Writes:
			spec = writeSpec
		case agentquota.Egress:
			spec = egressSpec
		case agentquota.Reads, agentquota.Calls, agentquota.Cost:
		}
		_, err := quotaGate(over(counter, 500, 100)).Admit(agentCtx(scopeFor(spec)), spec, noResolve)

		var refusal *QuotaExceededError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s past its bound → %v, want a typed quota refusal", counter, err)
		}
		if refusal.Releasable() != want {
			t.Errorf("%s reports Releasable()=%v, want %v", counter, refusal.Releasable(), want)
		}
		if refusal.Reading.Bucket != 7 {
			t.Errorf("%s refusal carries window %d, not the one the meter read; a release would land in the wrong window",
				counter, refusal.Reading.Bucket)
		}
	}
}

// A hard stop and a step-up read differently to the agent on the other end,
// because its next move differs: one waits for a human, the other must stop
// asking until the window rolls. A client told "ask a human" about a send
// ceiling waits forever.
func TestAHardStopTellsTheAgentNoApprovalWillLiftIt(t *testing.T) {
	_, hard := quotaGate(over(agentquota.Egress, 21, 20)).Admit(agentCtx(principal.ScopeSend), egressSpec, noResolve)
	_, soft := quotaGate(over(agentquota.Reads, 2001, 2000)).Admit(agentCtx(principal.ScopeRead), readSpec, noResolve)

	if !strings.Contains(hard.Error(), "no approval lifts it") {
		t.Errorf("a hard stop does not say that no approval lifts it: %q", hard)
	}
	if !strings.Contains(soft.Error(), "releases the window") {
		t.Errorf("a releasable refusal does not say a release would end it: %q", soft)
	}
	// And it does NOT claim one has been asked for. This error is answered on
	// both doors and only the MCP one stages the question; promising a human is
	// looking would leave a REST caller waiting on an approval nobody created.
	if strings.Contains(soft.Error(), "has been asked") {
		t.Errorf("a bare quota refusal claims somebody was asked: %q", soft)
	}
}

// A caller who may not run the verb at all must not learn that a quota exists,
// let alone how much of it is spent. Scope is checked first, and the meter is
// never asked.
func TestAnOutOfScopeCallerNeverLearnsTheQuotaExists(t *testing.T) {
	quota := over(agentquota.Reads, 9999, 2000)

	_, err := quotaGate(quota).Admit(agentCtx(principal.ScopeWrite), readSpec, noResolve)

	if !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Fatalf("out of scope → %v, want ErrScopeExceeded", err)
	}
	if len(quota.asked) != 0 {
		t.Errorf("the meter was consulted %v for a caller who may not run the verb", quota.asked)
	}
}

// A read seat is refused for a write BEFORE any quota is consulted, so the two
// refusals cannot be confused: one is a licensing ceiling no approval lifts, the
// other a volume threshold a human can release.
func TestTheSeatCeilingIsAnsweredBeforeTheQuota(t *testing.T) {
	quota := over(agentquota.Writes, 500, 200)
	gate := NewGate(&stubAuthority{seat: principal.SeatRead}, WithQuota(quota))

	_, err := gate.Admit(agentCtx(principal.ScopeWrite), writeSpec, noResolve)

	if !errors.Is(err, apperrors.ErrSeatTierInsufficient) {
		t.Fatalf("a read seat running a write → %v, want ErrSeatTierInsufficient", err)
	}
	if len(quota.asked) != 0 {
		t.Errorf("the meter was consulted %v for a caller whose seat cannot mutate at all", quota.asked)
	}
}

// Under every bound, a call is admitted exactly as before. Stated so the control
// is proven to be a threshold rather than a refusal that happens to fire.
func TestACallUnderEveryBoundIsAdmitted(t *testing.T) {
	quota := &spentQuota{}

	if _, err := quotaGate(quota).Admit(agentCtx(principal.ScopeRead), readSpec, noResolve); err != nil {
		t.Fatalf("a read under every bound → %v, want admitted", err)
	}
	// WHICH two, not how many: a regression that asked Reads twice — dropping
	// the call ceiling entirely — would satisfy a count and lose the ceiling
	// every other quota sits under.
	if len(quota.asked) != 2 || quota.asked[0] != agentquota.Calls || quota.asked[1] != agentquota.Reads {
		t.Errorf("one read consulted %v; it owes the call ceiling FIRST and then its own counter", quota.asked)
	}
}

// A human never enters this path at all — the gate returns before any agent
// check — so a busy agent cannot lock its own operator out of the product.
func TestAHumanIsNeverRefusedByAQuota(t *testing.T) {
	quota := over(agentquota.Reads, 9999, 2000)
	ctx := principal.WithWorkspaceID(context.Background(), testWorkspace)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalHuman, ID: "human:rep"})

	if _, err := quotaGate(quota).Admit(ctx, readSpec, noResolve); err != nil {
		t.Fatalf("a human was refused by an agent quota: %v", err)
	}
	if len(quota.asked) != 0 {
		t.Errorf("the meter was consulted %v for a human", quota.asked)
	}
}

// A gate composed WITHOUT a meter does not enforce one. That is a real
// composition — the Surface-B runner and the workflow paths build one, and
// they run as the human or system that started them, whom the bounds do not
// govern anyway. Asserted so the nil is a decision rather than an accident
// nobody notices until an agent surface is built without a meter.
func TestAGateWithNoQuotaDoesNotEnforceOne(t *testing.T) {
	for _, spec := range []mcp.ToolSpec{readSpec, writeSpec, egressSpec} {
		if _, err := fullSeatGate().Admit(agentCtx(scopeFor(spec)), spec, noResolve); err != nil {
			t.Fatalf("a gate with no meter refused %s: %v", spec.Name, err)
		}
	}
}

// The REST door refuses on the SAME bound the MCP door charges. Without this a
// Passport could spend its window through /mcp and then keep reading the very
// same records through /v1 — one credential, two doors, one of them unbounded,
// which is exactly what ADR-0055 says must not be possible.
func TestTheRestReadPathRefusesOnTheSameBound(t *testing.T) {
	quota := over(agentquota.Reads, 2001, 2000)

	err := quotaGate(quota).AdmitRead(agentCtx(principal.ScopeRead))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("a REST agent read past the bound → %v, want ErrBudgetExceeded", err)
	}
	if len(quota.asked) != 1 || quota.asked[0] != agentquota.Reads {
		t.Errorf("a REST read consulted %v, want the read counter alone", quota.asked)
	}
}

// A REST GET is not a TOOL call, so the call ceiling is not applied to it.
// Charging it there would meter one credential against a ceiling written for a
// surface this request never touched, and the two doors would then disagree
// about what a call is.
func TestARestReadIsNotChargedAgainstTheToolCallCeiling(t *testing.T) {
	quota := over(agentquota.Calls, 5000, 1000)

	if err := quotaGate(quota).AdmitRead(agentCtx(principal.ScopeRead)); err != nil {
		t.Fatalf("a REST read was suspended by the TOOL call ceiling: %v", err)
	}
	if quota.askedFor(agentquota.Calls) {
		t.Error("the tool-call ceiling was consulted for a REST read")
	}
}

// Under the bound the REST read passes through untouched.
func TestARestReadUnderTheBoundIsAdmitted(t *testing.T) {
	if err := quotaGate(&spentQuota{}).AdmitRead(agentCtx(principal.ScopeRead)); err != nil {
		t.Fatalf("a REST read under the bound → %v, want admitted", err)
	}
}

// A human's REST read is never touched by the agent bound — their authority is
// RBAC at the store, and a busy agent must not lock its operator out of the UI.
func TestAHumansRestReadIsNeverRefusedByTheBound(t *testing.T) {
	quota := over(agentquota.Reads, 9999, 2000)
	ctx := principal.WithWorkspaceID(context.Background(), testWorkspace)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalHuman, ID: "human:rep"})

	if err := quotaGate(quota).AdmitRead(ctx); err != nil {
		t.Fatalf("a human's REST read was refused by the agent bound: %v", err)
	}
	if len(quota.asked) != 0 {
		t.Errorf("the meter was consulted %v for a human's REST read", quota.asked)
	}
}

// A gate with no meter composed admits, rather than failing closed on a
// dependency the deployment never declared.
func TestARestReadOnAGateWithNoBoundIsAdmitted(t *testing.T) {
	if err := fullSeatGate().AdmitRead(agentCtx(principal.ScopeRead)); err != nil {
		t.Fatalf("a REST read on a gate with no meter → %v", err)
	}
}

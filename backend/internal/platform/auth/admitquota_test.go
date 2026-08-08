// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/readmeter"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// spentBound is the read bound as the gate sees it: a fixed answer, so a test
// says what the meter concluded without standing up Redis to conclude it.
type spentBound struct {
	reading readmeter.Reading
	asked   int
}

func (s *spentBound) Read(context.Context) readmeter.Reading {
	s.asked++
	return s.reading
}

func boundGate(reading readmeter.Reading) (*Gate, *spentBound) {
	bound := &spentBound{reading: reading}
	return NewGate(&stubAuthority{seat: principal.SeatFull}, WithReadBound(bound)), bound
}

var (
	readSpec  = mcp.ToolSpec{Name: "search_records", RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute}
	writeSpec = mcp.ToolSpec{Name: "update_record", RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute}
)

// AC-MCP-1: an agent past MCP-SESS-READS is stepped up on its NEXT read call.
// The sentinel is the spec's own for MCP-SESS-* (interfaces.md §0,
// ErrBudgetExceeded), so a client branches on the registry rather than on
// prose.
func TestAReadIsRefusedOnceTheAgentHasPassedItsReadBound(t *testing.T) {
	gate, _ := boundGate(readmeter.Reading{Observed: 2001, Limit: 2000, Exceeded: true})

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

// The bound counts RECORDS READ. Refusing a write because reading was heavy
// would enforce a limit nobody wrote — and would take a confirm-first action's
// staging away for a reason unrelated to it.
func TestAWriteIsNotRefusedByTheReadBound(t *testing.T) {
	gate, bound := boundGate(readmeter.Reading{Observed: 9999, Limit: 2000, Exceeded: true})

	if _, err := gate.Admit(agentCtx(principal.ScopeWrite), writeSpec, noResolve); err != nil {
		t.Fatalf("a write was refused by the READ bound: %v", err)
	}
	if bound.asked != 0 {
		t.Error("the read bound was consulted for a write-scoped tool")
	}
}

// A caller who may not run the verb at all must not learn that a quota exists,
// let alone how much of it is spent. Scope is checked first, and the bound is
// never asked.
func TestAnOutOfScopeCallerNeverLearnsTheQuotaExists(t *testing.T) {
	gate, bound := boundGate(readmeter.Reading{Observed: 9999, Limit: 2000, Exceeded: true})

	_, err := gate.Admit(agentCtx(principal.ScopeWrite), readSpec, noResolve)

	if !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Fatalf("out of scope → %v, want ErrScopeExceeded", err)
	}
	if bound.asked != 0 {
		t.Error("the read bound was consulted for a caller who may not run the verb")
	}
}

// A read seat is refused for a write BEFORE the read bound is consulted, so
// the two refusals cannot be confused: one is a licensing ceiling no approval
// lifts, the other a volume threshold a human can release.
func TestTheSeatCeilingIsAnsweredBeforeTheReadBound(t *testing.T) {
	bound := &spentBound{reading: readmeter.Reading{Exceeded: true}}
	gate := NewGate(&stubAuthority{seat: principal.SeatRead}, WithReadBound(bound))

	_, err := gate.Admit(agentCtx(principal.ScopeWrite), writeSpec, noResolve)

	if !errors.Is(err, apperrors.ErrSeatTierInsufficient) {
		t.Fatalf("a read seat running a write → %v, want ErrSeatTierInsufficient", err)
	}
}

// Under the bound, a read is admitted exactly as before. Stated so the control
// is proven to be a threshold rather than a refusal that happens to fire.
func TestAReadUnderTheBoundIsAdmitted(t *testing.T) {
	gate, bound := boundGate(readmeter.Reading{Observed: 1999, Limit: 2000})

	if _, err := gate.Admit(agentCtx(principal.ScopeRead), readSpec, noResolve); err != nil {
		t.Fatalf("a read under the bound → %v, want admitted", err)
	}
	if bound.asked != 1 {
		t.Errorf("the bound was consulted %d times for one read", bound.asked)
	}
}

// A human never enters this path at all — the gate returns before any agent
// check — so a busy agent cannot lock its own operator out of the product.
func TestAHumanIsNeverRefusedByTheReadBound(t *testing.T) {
	gate, bound := boundGate(readmeter.Reading{Observed: 9999, Limit: 2000, Exceeded: true})
	ctx := principal.WithWorkspaceID(context.Background(), testWorkspace)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalHuman, ID: "human:rep"})

	if _, err := gate.Admit(ctx, readSpec, noResolve); err != nil {
		t.Fatalf("a human was refused by the agent read bound: %v", err)
	}
	if bound.asked != 0 {
		t.Error("the read bound was consulted for a human")
	}
}

// A gate composed WITHOUT a bound does not enforce one. That is a real
// composition — the Surface-B runner and the workflow paths build one, and
// they run as the human or system that started them, whom the bound does not
// govern anyway. Asserted so the nil is a decision rather than an accident
// nobody notices until an agent surface is built without a meter.
func TestAGateWithNoReadBoundDoesNotEnforceOne(t *testing.T) {
	if _, err := fullSeatGate().Admit(agentCtx(principal.ScopeRead), readSpec, noResolve); err != nil {
		t.Fatalf("a gate with no read bound refused a read: %v", err)
	}
}

// The REST door refuses on the SAME bound the MCP door charges. Without this a
// Passport could spend its window through /mcp and then keep reading the very
// same records through /v1 — one credential, two doors, one of them unbounded,
// which is exactly what ADR-0055 says must not be possible.
func TestTheRestReadPathRefusesOnTheSameBound(t *testing.T) {
	gate, bound := boundGate(readmeter.Reading{Observed: 2001, Limit: 2000, Exceeded: true})

	err := gate.AdmitRead(agentCtx(principal.ScopeRead))

	if !errors.Is(err, apperrors.ErrBudgetExceeded) {
		t.Fatalf("a REST agent read past the bound → %v, want ErrBudgetExceeded", err)
	}
	if bound.asked != 1 {
		t.Errorf("the bound was consulted %d times for one REST read", bound.asked)
	}
}

// Under the bound the REST read passes through untouched.
func TestARestReadUnderTheBoundIsAdmitted(t *testing.T) {
	gate, _ := boundGate(readmeter.Reading{Observed: 10, Limit: 2000})

	if err := gate.AdmitRead(agentCtx(principal.ScopeRead)); err != nil {
		t.Fatalf("a REST read under the bound → %v, want admitted", err)
	}
}

// A human's REST read is never touched by the agent bound — their authority is
// RBAC at the store, and a busy agent must not lock its operator out of the UI.
func TestAHumansRestReadIsNeverRefusedByTheBound(t *testing.T) {
	gate, bound := boundGate(readmeter.Reading{Observed: 9999, Limit: 2000, Exceeded: true})
	ctx := principal.WithWorkspaceID(context.Background(), testWorkspace)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalHuman, ID: "human:rep"})

	if err := gate.AdmitRead(ctx); err != nil {
		t.Fatalf("a human's REST read was refused by the agent bound: %v", err)
	}
	if bound.asked != 0 {
		t.Error("the bound was consulted for a human's REST read")
	}
}

// A gate with no bound composed admits, rather than failing closed on a
// dependency the deployment never declared.
func TestARestReadOnAGateWithNoBoundIsAdmitted(t *testing.T) {
	if err := fullSeatGate().AdmitRead(agentCtx(principal.ScopeRead)); err != nil {
		t.Fatalf("a REST read on a gate with no bound → %v", err)
	}
}

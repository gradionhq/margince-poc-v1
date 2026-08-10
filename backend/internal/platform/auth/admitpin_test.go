// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// A dynamic tier is resolved from a record, and the read that resolves it
// commits before the write it admits. Admit is the one place that knows both
// what was read and what the tier came out as, so it is the one place that can
// bind them — and being the ONE place is what keeps the two dispatch layers
// from each having to remember.
//
// The pin is a consequence of admitting at 🟢. It appears on nothing else: not
// on a static tier (nothing was read to decide it), not on a raised one (the
// approval carries its own pin, taken at the moment the human was shown the
// record), and not on a human (the gate does not govern them at all).

func version(v int64) *int64 { return &v }

// dynamicSpec is the one dynamic-tier tool on the surface, with its resolver
// stubbed to the verdict a case is about: what Admit does with the version is
// a property of the OUTCOME, not of how the resolver reached it.
func dynamicSpec(resolved mcp.RiskTier) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "advance_deal", RequiredScope: principal.ScopeWrite, Tier: mcp.TierDynamic,
		TierResolver: func(mcp.TierResolverInput) mcp.RiskTier { return resolved },
	}
}

func resolvesTo(in mcp.TierResolverInput) func() (mcp.TierResolverInput, error) {
	return func() (mcp.TierResolverInput, error) { return in, nil }
}

func TestAdmitPinsTheVersionADynamicTierWasReadFrom(t *testing.T) {
	spec := dynamicSpec(mcp.TierAutoExecute)
	resolve := resolvesTo(mcp.TierResolverInput{
		SourceStageSemantic: "open", TargetStageSemantic: "open", ObservedVersion: version(9),
	})

	admitted, err := fullSeatGate().Admit(agentCtx(principal.ScopeWrite), spec, resolve)
	if err != nil {
		t.Fatalf("the routine move was refused: %v", err)
	}
	pin, ok := AutoExecutePin(admitted)
	if !ok {
		t.Fatal("an auto-executed dynamic call carries no pin, so the write it admits binds nothing " +
			"to the record state the tier was decided from")
	}
	if pin != 9 {
		t.Errorf("pin = %d, want the 9 the tier was read at", pin)
	}
}

// A dynamic tool that cannot say which record its tier was read from does not
// get to run unattended.
//
// This is the seam's fail-CLOSED half, and without it the whole guard is
// advisory: a dynamic tool answering no version would be admitted at 🟢 and
// written with no pin — #614's exact shape, restored by omission. Raising is the
// right refusal because a resolver may only ever raise, and because the safe
// answer to "this server could not establish the record's state" is a human,
// not a write.
func TestADynamicTierThatNamesNoRecordVersionIsRaisedRatherThanRun(t *testing.T) {
	spec := dynamicSpec(mcp.TierAutoExecute)
	resolve := resolvesTo(mcp.TierResolverInput{SourceStageSemantic: "open", TargetStageSemantic: "open"})

	admitted, err := fullSeatGate().Admit(agentCtx(principal.ScopeWrite), spec, resolve)
	if !errors.Is(err, apperrors.ErrRequiresApproval) {
		t.Fatalf("admitted → %v, want confirm-first: a 🟢 write with nothing to bind is the "+
			"unpinned auto-execute this gate exists to end", err)
	}
	if pin, ok := AutoExecutePin(admitted); ok {
		t.Errorf("a refused call carries pin %d", pin)
	}
}

func TestAdmitPinsNothingItDidNotAdmitAtTheAutoExecuteTier(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx     context.Context
		spec    mcp.ToolSpec
		resolve func() (mcp.TierResolverInput, error)
	}{
		"a static tier read no record to decide itself": {
			agentCtx(principal.ScopeWrite),
			mcp.ToolSpec{Name: "create_record", RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute},
			resolvesTo(mcp.TierResolverInput{ObservedVersion: version(4)}),
		},
		"a raised tier is carried by the approval's own pin": {
			agentCtx(principal.ScopeWrite),
			dynamicSpec(mcp.TierConfirmationRequired),
			resolvesTo(mcp.TierResolverInput{ObservedVersion: version(4)}),
		},
		"a human does not ride the gate's tier model at all": {
			principal.WithActor(principal.WithWorkspaceID(context.Background(), testWorkspace),
				principal.Principal{Type: principal.PrincipalHuman, ID: "user:test"}),
			dynamicSpec(mcp.TierAutoExecute),
			resolvesTo(mcp.TierResolverInput{ObservedVersion: version(4)}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			admitted, _ := fullSeatGate().Admit(tc.ctx, tc.spec, tc.resolve)
			if pin, ok := AutoExecutePin(admitted); ok {
				t.Errorf("pinned version %d — a write conditioned on a version this call never "+
					"proved anything about fails for a reason nobody can act on", pin)
			}
		})
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The defect, written before the fix.
//
// #610 made the tier turn on BOTH endpoints, so a move that reopens a closed
// deal raises to confirm-first. It proves both endpoints open from a read that
// commits before the write does, and on the auto-execute path nothing binds the
// two: the deal can close in the window, and the 🟢 write then lands on a
// terminal deal — clearing closed_at, the lost reason and the FX rate frozen at
// close, with no human in the loop.
//
// The interleaving is what has to be tested, so the racing close happens at the
// one moment that reproduces it: inside the tier gate's own read. The provider
// below supplies the TIMING and nothing else — the version compare it then
// applies is the one deals.Store.AdvanceDeal applies through ApplyGuarded, and
// the integration lane drives that same sequence against real Postgres.

// closesUnderneath is a deal that a concurrent actor closes during the tier
// gate's read: the FIRST read answers the deal as the gate must see it (open,
// at the version it then holds) and, on its way out, moves the row to won and
// bumps the version. Every later read sees the closed deal, which is what the
// racing actor left behind.
type closesUnderneath struct {
	datasource.SystemOfRecordProvider
	openStage, wonStage ids.UUID

	version int64
	stage   ids.UUID
	raced   bool

	// gotIfVersion is what the write was conditioned on, and whether it was
	// conditioned at all — the whole question this file asks.
	gotIfVersion *int64
	wrote        bool
}

func (p *closesUnderneath) Read(context.Context, datasource.EntityRef) (datasource.Record, error) {
	rec := datasource.Record{
		Ref:     datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()},
		Fields:  json.RawMessage(`{"name":"Acme renewal","stage_id":"` + p.stage.String() + `"}`),
		Version: p.version,
	}
	if !p.raced {
		// The concurrent close, landing in the window between the gate's read
		// and the write it admits.
		p.raced = true
		p.stage, p.version = p.wonStage, p.version+1
	}
	return rec, nil
}

func (p *closesUnderneath) AdvanceDeal(_ context.Context, in datasource.AdvanceDealInput) (datasource.EntityRef, error) {
	p.gotIfVersion = in.IfVersion
	if in.IfVersion != nil && *in.IfVersion != p.version {
		// The store's own answer: Patch.ApplyGuarded refuses a write whose
		// pinned version is not the row's.
		return datasource.EntityRef{}, apperrors.ErrVersionSkew
	}
	p.wrote = true
	return datasource.EntityRef{Type: datasource.EntityDeal, ID: in.DealID}, nil
}

// agentInvoking is a full-seat agent principal with the scope a deal move
// needs — the caller the gate exists to bound.
func agentInvoking() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:pin", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeWrite),
	})
}

// racingRegistry registers one dynamic deal-move tool over a deal that closes
// during the tier read.
func racingRegistry(t *testing.T, build func(datasource.SystemOfRecordProvider, StageResolver) mcp.Tool) (*Registry, *closesUnderneath) {
	t.Helper()
	openStage, wonStage := ids.NewV7(), ids.NewV7()
	provider := &closesUnderneath{
		openStage: openStage, wonStage: wonStage,
		version: 7, stage: openStage,
	}
	stages := &reopenProbeStages{semantics: map[ids.UUID]string{openStage: "open", wonStage: "won"}}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	r.Register(build(provider, stages))
	return r, provider
}

func TestAConcurrentCloseCannotReopenADealThroughTheAutoExecutedMove(t *testing.T) {
	for name, tc := range map[string]struct {
		tool  string
		build func(datasource.SystemOfRecordProvider, StageResolver) mcp.Tool
	}{
		"advance_deal": {"advance_deal", func(p datasource.SystemOfRecordProvider, s StageResolver) mcp.Tool {
			return advanceDeal{p: p, stages: s}
		}},
		"progress_deal": {"progress_deal", func(p datasource.SystemOfRecordProvider, s StageResolver) mcp.Tool {
			return progressDeal{p: p, stages: s}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r, provider := racingRegistry(t, tc.build)
			args := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() +
				`","to_stage_id":"` + provider.openStage.String() + `"}`)

			_, err := r.Invoke(agentInvoking(), tc.tool, args)

			if provider.gotIfVersion == nil {
				t.Fatal("the write carried no version pin, so the tier gate's proof that both " +
					"endpoints were open bound nothing — a close landing in that window reopens the deal")
			}
			if *provider.gotIfVersion != 7 {
				t.Errorf("the write pinned version %d, want 7 — the version the tier decision was read from",
					*provider.gotIfVersion)
			}
			if !errors.Is(err, apperrors.ErrVersionSkew) {
				t.Errorf("the racing move answered %v, want version skew", err)
			}
			if provider.wrote {
				t.Error("the deal was reopened by a call whose 🟢 tier was decided before it closed")
			}
		})
	}
}

// The pin binds the read; it does not refuse the ordinary move. Without this a
// fix could pass the test above by refusing every unattended deal move, which
// would put a human in front of every routine stage change on the surface.
func TestAnUncontendedMoveBetweenOpenStagesStillRunsUnattended(t *testing.T) {
	openStage := ids.NewV7()
	provider := &closesUnderneath{
		// Both endpoints are the same open stage and the racing close never
		// fires, so this is the routine move with nothing contending.
		openStage: openStage, wonStage: openStage,
		version: 3, stage: openStage, raced: true,
	}
	stages := &reopenProbeStages{semantics: map[ids.UUID]string{openStage: "open"}}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	r.Register(advanceDeal{p: provider, stages: stages})

	args := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() +
		`","to_stage_id":"` + openStage.String() + `"}`)
	if _, err := r.Invoke(agentInvoking(), "advance_deal", args); err != nil {
		t.Fatalf("the routine open→open move was refused: %v", err)
	}
	if !provider.wrote {
		t.Fatal("the routine move did not reach the store")
	}
	if provider.gotIfVersion == nil || *provider.gotIfVersion != 3 {
		t.Errorf("the routine move pinned %v, want the observed version 3", provider.gotIfVersion)
	}
}

// A caller's own if_version may not disagree with what the gate read.
//
// The caller controls this argument, so a pin the gate never saw is a pin
// nothing proved: a FUTURE version defeats the guard outright — the deal closes
// in the window, the row lands on exactly the version the caller named, and the
// store's compare passes on a record the tier decision never described. A stale
// one is the ordinary optimistic-concurrency case and was already refused, but
// by the store rather than here.
//
// Both are one rule: on an auto-executed dynamic call the write is conditioned
// on the version the tier was resolved from, and a caller who names a different
// one is told so rather than served.
func TestACallerPinThatDisagreesWithTheGatesReadIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		callerPin int64
		// racing says whether a concurrent close lands in the window. The FUTURE
		// pin only bites when it does: the caller names the version the row will
		// have AFTER the close, so the store's compare passes on exactly the
		// record the tier decision does not describe.
		racing bool
	}{
		"a FUTURE version the gate never read":   {callerPin: 8, racing: true},
		"a stale version the caller still holds": {callerPin: 4, racing: false},
	} {
		t.Run(name, func(t *testing.T) {
			openStage, wonStage := ids.NewV7(), ids.NewV7()
			provider := &closesUnderneath{
				openStage: openStage, wonStage: wonStage,
				version: 7, stage: openStage, raced: !tc.racing,
			}
			stages := &reopenProbeStages{semantics: map[ids.UUID]string{
				openStage: "open", wonStage: "won",
			}}
			r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
			r.Register(advanceDeal{p: provider, stages: stages})

			args := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() +
				`","to_stage_id":"` + openStage.String() +
				`","if_version":` + strconv.FormatInt(tc.callerPin, 10) + `}`)
			if _, err := r.Invoke(agentInvoking(), "advance_deal", args); !errors.Is(err, apperrors.ErrVersionSkew) {
				t.Fatalf("answered %v, want version skew", err)
			}
			if provider.wrote {
				t.Error("the write ran on a version the tier decision never described — a caller " +
					"naming the version the racing close produces walks straight through the guard")
			}
		})
	}
}

// A caller that names the version the gate read is served, because the two
// agree about which record this is.
func TestACallerPinThatMatchesTheGatesReadIsHonoured(t *testing.T) {
	openStage := ids.NewV7()
	provider := &closesUnderneath{
		openStage: openStage, wonStage: openStage,
		version: 7, stage: openStage, raced: true,
	}
	stages := &reopenProbeStages{semantics: map[ids.UUID]string{openStage: "open"}}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	r.Register(advanceDeal{p: provider, stages: stages})

	args := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() +
		`","to_stage_id":"` + openStage.String() + `","if_version":7}`)
	if _, err := r.Invoke(agentInvoking(), "advance_deal", args); err != nil {
		t.Fatalf("a caller pinning the version the gate read was refused: %v", err)
	}
	if provider.gotIfVersion == nil || *provider.gotIfVersion != 7 {
		t.Errorf("the write pinned %v, want 7", provider.gotIfVersion)
	}
}

// The derived gate. A dynamic tier means the tool reads a record to decide
// whether it may run unattended; a tool that then writes without binding that
// read is #614. Deriving it from the registered set rather than naming the two
// tools is what makes the third dynamic tool meet it on the day it registers.
func TestEveryDynamicTierToolPinsTheReadItsTierTurnedOn(t *testing.T) {
	openStage := ids.NewV7()
	provider := &closesUnderneath{
		openStage: openStage, wonStage: openStage,
		version: 11, stage: openStage, raced: true,
	}
	stages := &reopenProbeStages{semantics: map[ids.UUID]string{openStage: "open"}}
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterCoreTools(r, provider, stages, nil, nil)

	args := json.RawMessage(`{"deal_id":"` + ids.NewV7().String() +
		`","to_stage_id":"` + openStage.String() + `"}`)

	var dynamic int
	for _, spec := range r.Specs() {
		if spec.Tier != mcp.TierDynamic {
			continue
		}
		dynamic++
		tool, ok := r.tools[spec.Name].(dynamicTool)
		if !ok {
			t.Errorf("%s declares a dynamic tier but cannot build a resolver input", spec.Name)
			continue
		}
		in, err := tool.ResolverInput(context.Background(), args)
		if err != nil {
			t.Errorf("%s: resolving: %v", spec.Name, err)
			continue
		}
		if in.ObservedVersion == nil {
			t.Errorf("%s decides its tier from a record it read and reports no version for it, "+
				"so an auto-executed write cannot be bound to what the gate saw", spec.Name)
			continue
		}
		if *in.ObservedVersion != 11 {
			t.Errorf("%s reported version %d, want the %d it read the record at",
				spec.Name, *in.ObservedVersion, 11)
		}
	}
	if dynamic == 0 {
		t.Fatal("no dynamic-tier tool was registered, so this gate judged nothing")
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package automation

// The match-time owner-permission gate (gate.go, AUTO-T06) proven over a
// real migrated Postgres: a human-authored automation whose owner has
// since lost the required permission must land 'blocked' with a reason,
// with the effect never applied — never a silent pass. fixtureResolver
// stands in for identity.Service (the real compose/workflows.go wiring):
// modules never import a sibling, test files included
// (TestNoSiblingModuleImports covers TestImports/XTestImports), so this
// package cannot reach for identity directly even in its own tests.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fixtureResolver is a fixed-answer authz.Resolver: enough to prove the
// gate's real claim/record wiring against Postgres without pulling in
// modules/identity's live-seat machinery, which this suite is not testing.
type fixtureResolver struct {
	rbac authz.RBAC
	err  error
}

func (r fixtureResolver) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return r.rbac, r.err
}

func (r fixtureResolver) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

var _ authz.Resolver = fixtureResolver{}

// gateTestEnvelope is one cg:workflows delivery every case in this suite
// fires against — only the resolver's answer and the seeded automation's
// owner_id vary between cases. scriptedWorkflow's default Plan
// (autofixture_integration_test.go) emits a create_task action, whose
// registry permission (catalog_actions.go) is the pinned "activity"/create
// this suite's resolvers are built to grant or withhold.
func gateTestEnvelope(ws ids.UUID) kevents.Envelope {
	return kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        scriptedTrigger,
		WorkspaceID: ws,
		OccurredAt:  time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC),
		Entity:      kevents.EntityRef{Type: "deal", ID: ids.NewV7()},
	}
}

// TestMatchTimeGateBlocksAFiringWhoseOwnerLostThePermission is AUTO-T06's
// AC verbatim: the owner's live RBAC no longer grants create_task's
// pinned "activity"/create permission, so the firing must land 'blocked'
// with a reason and the effect must never reach Apply.
func TestMatchTimeGateBlocksAFiringWhoseOwnerLostThePermission(t *testing.T) {
	fx := setupAutomationDB(t)
	applyCalls := 0
	handler := scriptedWorkflow{
		name: "owner_lost_permission",
		apply: func(ev workflow.Event) (workflow.RunResult, error) {
			applyCalls++
			return workflow.RunResult{}, nil
		},
	}
	fx.seedAutomationWithOwner(t, handler.name, fx.rep1)

	resolver := fixtureResolver{rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{}, // the owner's RBAC no longer grants anything
	}}}
	engine := NewWorkflowEngine(fx.pool, resolver)
	engine.RegisterWorkflow(handler)

	if err := engine.HandleEvent(context.Background(), gateTestEnvelope(fx.ws)); err != nil {
		t.Fatalf("HandleEvent err = %v, want nil — a gate block is a recorded outcome, not a dispatch failure", err)
	}

	runs := fx.runsByHandler(t)
	run, ok := runs[handler.name]
	if !ok {
		t.Fatal("no run row recorded for the blocked firing")
	}
	if run.status != "blocked" {
		t.Fatalf("run.status = %q, want %q", run.status, "blocked")
	}
	detail, err := parseRunDetail(run.detail)
	if err != nil {
		t.Fatalf("parsing run detail: %v", err)
	}
	if detail.Reason == "" || !strings.Contains(detail.Reason, "permit") {
		t.Errorf("run detail reason = %q, want it to name the owner's lost permission", detail.Reason)
	}
	if applyCalls != 0 {
		t.Errorf("Apply called %d times, want 0 — a blocked firing must never execute its effect", applyCalls)
	}
}

// TestMatchTimeGateAllowsAFiringWhoseOwnerStillHasThePermission is the
// healthy-path counterpart: the owner's live RBAC still grants the
// effect's permission, so the firing must proceed to Apply and land
// 'applied' exactly like today.
func TestMatchTimeGateAllowsAFiringWhoseOwnerStillHasThePermission(t *testing.T) {
	fx := setupAutomationDB(t)
	applyCalls := 0
	handler := scriptedWorkflow{
		name: "owner_ok",
		apply: func(ev workflow.Event) (workflow.RunResult, error) {
			applyCalls++
			return workflow.RunResult{Applied: []workflow.Action{{Kind: workflow.ActionCreateTask}}}, nil
		},
	}
	fx.seedAutomationWithOwner(t, handler.name, fx.rep1)

	resolver := fixtureResolver{rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"activity": {Create: true}},
	}}}
	engine := NewWorkflowEngine(fx.pool, resolver)
	engine.RegisterWorkflow(handler)

	if err := engine.HandleEvent(context.Background(), gateTestEnvelope(fx.ws)); err != nil {
		t.Fatalf("HandleEvent err = %v, want nil", err)
	}

	runs := fx.runsByHandler(t)
	run, ok := runs[handler.name]
	if !ok {
		t.Fatal("no run row recorded for the allowed firing")
	}
	if run.status != "applied" {
		t.Fatalf("run.status = %q, want %q", run.status, "applied")
	}
	if applyCalls != 1 {
		t.Errorf("Apply called %d times, want exactly 1 — an owner who still holds the permission must fire normally", applyCalls)
	}
}

// TestMatchTimeGateSkipsASeededAutomationWithNoOwner proves the NULL-owner
// (system-seed) skip: a resolver that errors on any call would fail this
// test if the gate ever touched it, so a passing 'applied' outcome proves
// the gate never called EffectiveRBAC for this firing.
func TestMatchTimeGateSkipsASeededAutomationWithNoOwner(t *testing.T) {
	fx := setupAutomationDB(t)
	applyCalls := 0
	handler := scriptedWorkflow{
		name: "seeded_no_owner",
		apply: func(ev workflow.Event) (workflow.RunResult, error) {
			applyCalls++
			return workflow.RunResult{Applied: []workflow.Action{{Kind: workflow.ActionCreateTask}}}, nil
		},
	}
	fx.seedAutomation(t, handler.name) // no owner_id: the system-seed shape

	resolver := fixtureResolver{err: errors.New("must never be called for a NULL-owner automation")}
	engine := NewWorkflowEngine(fx.pool, resolver)
	engine.RegisterWorkflow(handler)

	if err := engine.HandleEvent(context.Background(), gateTestEnvelope(fx.ws)); err != nil {
		t.Fatalf("HandleEvent err = %v, want nil", err)
	}

	runs := fx.runsByHandler(t)
	run, ok := runs[handler.name]
	if !ok {
		t.Fatal("no run row recorded for the seeded firing")
	}
	if run.status != "applied" {
		t.Fatalf("run.status = %q, want %q — a NULL-owner automation must run ungated", run.status, "applied")
	}
	if applyCalls != 1 {
		t.Errorf("Apply called %d times, want exactly 1", applyCalls)
	}
}

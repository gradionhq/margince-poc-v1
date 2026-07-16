// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// fakeAuthzResolver is a DB-free stand-in for shared/ports/authz.Resolver:
// it hands back a fixed RBAC/seat or a fixed error and counts calls, so a
// test can prove the gate skipped the resolver entirely (the NULL-owner
// case) rather than merely behaving as if it had. seat defaults to the
// zero value; every case that does not itself exercise the seat check
// stamps SeatFull so its RBAC-only assertions are unaffected by the seat
// check running ahead of it.
type fakeAuthzResolver struct {
	seat      principal.SeatType
	seatErr   error
	rbac      authz.RBAC
	err       error
	calls     int
	seatCalls int
}

func (f *fakeAuthzResolver) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	f.calls++
	return f.rbac, f.err
}

func (f *fakeAuthzResolver) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	f.seatCalls++
	return f.seat, f.seatErr
}

var _ authz.Resolver = (*fakeAuthzResolver)(nil)

// grantedEffect is a one-action effect naming create_task's executor —
// the pinned "activity"/create permission every case below checks
// against, since the exact action kind is incidental to these tests.
var grantedEffect = workflow.Effect{Actions: []workflow.Action{{Kind: workflow.ActionCreateTask}}}

// TestCheckOwnerPermissionOwnerAllowedProceeds is case (a): the owner's
// live RBAC still grants the effect's required permission, so the gate
// renders the zero decision (proceed) and the firing continues to Apply.
func TestCheckOwnerPermissionOwnerAllowedProceeds(t *testing.T) {
	resolver := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"activity": {Create: true}},
	}}}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — the owner holds the required permission")
	}
	if resolver.calls != 1 {
		t.Errorf("EffectiveRBAC called %d times, want exactly 1", resolver.calls)
	}
}

// TestCheckOwnerPermissionOwnerDeniedBlocks is case (b): the owner's live
// RBAC no longer grants the permission (AUTO-AC-10's "Passport no longer
// permits …" shape) — the gate must block with a human-readable reason,
// not silently pass.
func TestCheckOwnerPermissionOwnerDeniedBlocks(t *testing.T) {
	resolver := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{},
	}}}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil — a denial is a decision, not an error", err)
	}
	if !decision.blocked {
		t.Fatal("decision.blocked = false, want true — the owner's RBAC grants nothing")
	}
	if decision.reason == "" {
		t.Error("decision.reason is empty — a human reading run history needs to know why the firing was refused")
	}
}

// TestCheckOwnerPermissionOwnerGoneBlocks is case (c): the honest hard
// case shared/ports/authz.Resolver pins — a missing/archived/suspended
// owner resolves to ErrNotFound, "never to an empty-but-valid authority".
// The gate must translate that into a blocked decision, fail closed. The
// seat resolves fine here (SeatFull) so this exercises the RBAC-side
// ErrNotFound path specifically — the seat-side equivalent is
// TestCheckOwnerPermissionSeatGoneBlocks below.
func TestCheckOwnerPermissionOwnerGoneBlocks(t *testing.T) {
	resolver := &fakeAuthzResolver{seat: principal.SeatFull, err: apperrors.ErrNotFound}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil — a gone owner is a blocked decision, not a propagated error", err)
	}
	if !decision.blocked {
		t.Fatal("decision.blocked = false, want true — ErrNotFound means the owner is gone/archived/suspended")
	}
}

// TestCheckOwnerPermissionTransientErrorSurfacesForRetry is case (d): a
// resolver failure that is NOT ErrNotFound (DB down, network blip) is
// infrastructure trouble, not a permission answer — it must surface so
// runOne retries the firing later, never fabricate an allow or a
// permanent block.
func TestCheckOwnerPermissionTransientErrorSurfacesForRetry(t *testing.T) {
	transientErr := errors.New("authz: database is down")
	resolver := &fakeAuthzResolver{seat: principal.SeatFull, err: transientErr}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if !errors.Is(err, transientErr) {
		t.Fatalf("err = %v, want it to wrap the transient failure %v", err, transientErr)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — a transient failure must retry, never a fabricated denial")
	}
}

// TestCheckOwnerPermissionSkipsForANullOwner is case (e): a system-seeded
// automation (SeedStarterAutomationsTx stamps no owner_id) carries no
// human authority to re-check — the gate must skip BEFORE ever touching
// the resolver, proven here by a resolver that errors if called at all.
func TestCheckOwnerPermissionSkipsForANullOwner(t *testing.T) {
	resolver := &fakeAuthzResolver{err: errors.New("must never be called for a NULL owner")}
	ev := workflow.Event{OwnerID: ids.Nil, WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — a system-seeded (NULL owner) automation runs ungated")
	}
	if resolver.calls != 0 {
		t.Errorf("EffectiveRBAC called %d times, want 0 — a NULL owner must skip before reaching the resolver", resolver.calls)
	}
}

// TestCheckOwnerPermissionTargetScopedResolvesObjectFromTheActionsOwnTarget
// proves a target-scoped action (assign_owner) gates the object the ACTION
// itself writes (action.Target.Type) — never a fixed guess. Every shipped
// handler's Plan sets Target to the entity it fired on (people/
// leadrouting.go's ActionAssignOwner case), so this is the common,
// coinciding shape; the divergent-target case below is the one that
// actually exercises the distinction.
func TestCheckOwnerPermissionTargetScopedResolvesObjectFromTheActionsOwnTarget(t *testing.T) {
	entity := datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7(), Entity: entity}
	effect := workflow.Effect{Actions: []workflow.Action{{Kind: workflow.ActionAssignOwner, Target: entity}}}

	granted := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"lead": {Update: true}},
	}}}
	decision, err := checkOwnerPermission(context.Background(), granted, ev, effect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — the owner holds update on the action's own target (lead)")
	}

	wrongObject := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"deal": {Update: true}},
	}}}
	decision, err = checkOwnerPermission(context.Background(), wrongObject, ev, effect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !decision.blocked {
		t.Error("decision.blocked = false, want true — update on 'deal' must not satisfy an action targeting 'lead'")
	}
}

// TestCheckOwnerPermissionGatesTheActionsTargetNotTheTriggersEntity is
// FIX 3's RED→GREEN proof: a target-scoped action must be gated against
// what it actually WRITES (action.Target.Type), never the trigger's
// entity type, the moment the two diverge. No shipped handler produces
// this shape today (every Plan sets Target to ev.Entity), but a future
// one could, and the pre-fix gate resolved the object from ev.Entity —
// which this test pins as wrong by constructing a firing on a deal whose
// planned action's real target is a lead: an owner who can only update
// deals must NOT be allowed to fire an action that actually writes a
// lead.
func TestCheckOwnerPermissionGatesTheActionsTargetNotTheTriggersEntity(t *testing.T) {
	ev := workflow.Event{
		OwnerID:     ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Entity:      datasource.EntityRef{Type: datasource.EntityDeal, ID: ids.NewV7()},
	}
	leadTarget := datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()}
	effect := workflow.Effect{Actions: []workflow.Action{{Kind: workflow.ActionAssignOwner, Target: leadTarget}}}

	dealOnly := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"deal": {Update: true}},
	}}}
	decision, err := checkOwnerPermission(context.Background(), dealOnly, ev, effect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !decision.blocked {
		t.Fatal("decision.blocked = false, want true — a grant on 'deal' (the trigger's entity) must not authorize an action whose real target is 'lead'")
	}

	leadGrant := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"lead": {Update: true}},
	}}}
	decision, err = checkOwnerPermission(context.Background(), leadGrant, ev, effect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — the owner holds update on the action's real target (lead), regardless of the trigger's own entity (deal)")
	}
}

// TestCheckOwnerPermissionFailsClosedWithNoResolverComposed proves a
// wiring bug (an engine built with no authz.Resolver) cannot silently
// pass a human-authored firing — it must return an error rather than
// treat "no resolver" as "no check needed".
func TestCheckOwnerPermissionFailsClosedWithNoResolverComposed(t *testing.T) {
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), nil, ev, grantedEffect)
	if err == nil {
		t.Fatal("err = nil, want a non-nil composition error for a nil resolver")
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — this is a wiring failure, not a permission verdict")
	}
}

// TestCheckOwnerPermissionReadSeatOwnerBlocks is FIX 2's RED→GREEN proof
// (ADR-0055, admit.go's identical order): an owner downgraded to a READ
// seat must be blocked even while their role grants still say yes — the
// RBAC here grants everything, so a decision to proceed would mean the
// seat ceiling was never checked at all.
func TestCheckOwnerPermissionReadSeatOwnerBlocks(t *testing.T) {
	resolver := &fakeAuthzResolver{seat: principal.SeatRead, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"activity": {Create: true}},
	}}}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil — a read seat is a blocked decision, not an error", err)
	}
	if !decision.blocked {
		t.Fatal("decision.blocked = false, want true — a read-seat owner may never fire a mutating automation")
	}
	if decision.reason == "" {
		t.Error("decision.reason is empty — a human reading run history needs to know why the firing was refused")
	}
	if resolver.calls != 0 {
		t.Errorf("EffectiveRBAC called %d times, want 0 — the seat ceiling must block before RBAC is even consulted", resolver.calls)
	}
}

// TestCheckOwnerPermissionFullSeatOwnerProceedsPastTheSeatCheck proves the
// seat check itself runs (seatCalls == 1) and a full seat does not block —
// distinct from the RBAC-allowed case above, which does not observe
// seatCalls at all.
func TestCheckOwnerPermissionFullSeatOwnerProceedsPastTheSeatCheck(t *testing.T) {
	resolver := &fakeAuthzResolver{seat: principal.SeatFull, rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"activity": {Create: true}},
	}}}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — a full seat with the required grant must proceed")
	}
	if resolver.seatCalls != 1 {
		t.Errorf("SeatType called %d times, want exactly 1 — the seat ceiling must actually run, not be bypassed", resolver.seatCalls)
	}
}

// TestCheckOwnerPermissionSeatGoneBlocks mirrors
// TestCheckOwnerPermissionOwnerGoneBlocks for the seat side of the same
// authz.Resolver contract: SeatType resolving to ErrNotFound for a gone/
// archived/suspended owner is a real denial, and it must be caught before
// EffectiveRBAC ever runs.
func TestCheckOwnerPermissionSeatGoneBlocks(t *testing.T) {
	resolver := &fakeAuthzResolver{seatErr: apperrors.ErrNotFound}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if err != nil {
		t.Fatalf("err = %v, want nil — a gone owner's seat is a blocked decision, not a propagated error", err)
	}
	if !decision.blocked {
		t.Fatal("decision.blocked = false, want true — ErrNotFound on SeatType means the owner is gone/archived/suspended")
	}
	if resolver.calls != 0 {
		t.Errorf("EffectiveRBAC called %d times, want 0 — the seat check must fail closed before RBAC runs", resolver.calls)
	}
}

// TestCheckOwnerPermissionSeatTransientErrorSurfacesForRetry mirrors
// TestCheckOwnerPermissionTransientErrorSurfacesForRetry for SeatType: an
// infrastructure failure resolving the seat is not a permission answer —
// it must surface for the caller to retry, never a fabricated allow or
// permanent block.
func TestCheckOwnerPermissionSeatTransientErrorSurfacesForRetry(t *testing.T) {
	transientErr := errors.New("authz: database is down")
	resolver := &fakeAuthzResolver{seatErr: transientErr}
	ev := workflow.Event{OwnerID: ids.NewV7(), WorkspaceID: ids.NewV7()}

	decision, err := checkOwnerPermission(context.Background(), resolver, ev, grantedEffect)
	if !errors.Is(err, transientErr) {
		t.Fatalf("err = %v, want it to wrap the transient failure %v", err, transientErr)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — a transient seat-resolution failure must retry, never a fabricated denial")
	}
	if resolver.calls != 0 {
		t.Errorf("EffectiveRBAC called %d times, want 0 — a transient seat error must surface before RBAC runs", resolver.calls)
	}
}

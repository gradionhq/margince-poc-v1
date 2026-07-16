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
// it hands back a fixed RBAC or a fixed error and counts calls, so a test
// can prove the gate skipped the resolver entirely (the NULL-owner case)
// rather than merely behaving as if it had.
type fakeAuthzResolver struct {
	rbac  authz.RBAC
	err   error
	calls int
}

func (f *fakeAuthzResolver) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	f.calls++
	return f.rbac, f.err
}

func (f *fakeAuthzResolver) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	panic("fakeAuthzResolver: SeatType not stubbed — the match-time gate only ever calls EffectiveRBAC")
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
	resolver := &fakeAuthzResolver{rbac: authz.RBAC{Permissions: principal.Permissions{
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
	resolver := &fakeAuthzResolver{rbac: authz.RBAC{Permissions: principal.Permissions{
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
// The gate must translate that into a blocked decision, fail closed.
func TestCheckOwnerPermissionOwnerGoneBlocks(t *testing.T) {
	resolver := &fakeAuthzResolver{err: apperrors.ErrNotFound}
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
	resolver := &fakeAuthzResolver{err: transientErr}
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

// TestCheckOwnerPermissionTargetScopedResolvesObjectFromTheFiringEntity
// proves a target-scoped action (assign_owner) gates the object the
// trigger actually fired on (ev.Entity.Type) — never a fixed guess. The
// same RBAC that grants "lead" update must satisfy a firing on a lead and
// refuse one on a deal, or the gate would be checking the wrong object.
func TestCheckOwnerPermissionTargetScopedResolvesObjectFromTheFiringEntity(t *testing.T) {
	ev := workflow.Event{
		OwnerID:     ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Entity:      datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()},
	}
	effect := workflow.Effect{Actions: []workflow.Action{{Kind: workflow.ActionAssignOwner}}}

	granted := &fakeAuthzResolver{rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"lead": {Update: true}},
	}}}
	decision, err := checkOwnerPermission(context.Background(), granted, ev, effect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if decision.blocked {
		t.Error("decision.blocked = true, want false — the owner holds update on the firing entity (lead)")
	}

	wrongObject := &fakeAuthzResolver{rbac: authz.RBAC{Permissions: principal.Permissions{
		Objects: map[string]principal.ObjectGrant{"deal": {Update: true}},
	}}}
	decision, err = checkOwnerPermission(context.Background(), wrongObject, ev, effect)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !decision.blocked {
		t.Error("decision.blocked = false, want true — update on 'deal' must not satisfy a firing on 'lead'")
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

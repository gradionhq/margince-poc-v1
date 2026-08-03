// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// The approval fan-out gate, in the two dimensions that resolve without a
// database: which halves of the staged target the envelope carries, and how the
// target's TYPE is classified. Each shape has its own floor, and each
// classification has to mirror the read rule of the store that owns the target.
// The row-scoped branches' object-read hardening is covered in unit_test.go; the
// database-backed probes live in the compose integration lane.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// readerOf is a fan-out owner whose only capability is reading one object type.
// RowScopeAll is deliberate: no shape in this file may reach a row probe, and
// the widest scope is what would drive one into the nil pool if it did.
func readerOf(object string, grant principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:   principal.PrincipalHuman,
		UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{object: grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

// A staged CREATE names the type it will write and no row to point at, because
// the record does not exist yet. The fan-out floor is the object-read grant on
// that type: an owner who may read projects learns a project create was
// proposed, an owner who may not learns nothing. Delivering it to everyone
// would disclose the staged body; withholding it from everyone leaves the
// confirm-first create class silently unannounced.
//
// The nil pool proves no row probe is reached on this arm — were the shape
// answered through approvalTargetVisible, RowScopeAll would drive it into the
// pool and panic.
func TestApprovalForAStagedCreateDeliversOnTheObjectReadGrant(t *testing.T) {
	s := NewStore(nil, nil)
	targetType := "project"

	if ok, err := s.approvalShapeVisible(readerOf("project", principal.ObjectGrant{Read: true}), &targetType, nil); !ok || err != nil {
		t.Fatalf("staged project create to an owner holding project.read = (%v, %v), want (true, nil)", ok, err)
	}

	// The negative that matters: the create grant is not the read grant. An
	// owner who cannot read projects must not learn one was proposed.
	if ok, err := s.approvalShapeVisible(readerOf("project", principal.ObjectGrant{Create: true}), &targetType, nil); ok || err != nil {
		t.Fatalf("staged project create to an owner without project.read = (%v, %v), want (false, nil)", ok, err)
	}
}

// The two shapes that carry no target TYPE stay fail-closed, which is the
// ratified deferral: a target-LESS approval (every coldstart.* echo) and an id
// the envelope cannot name a table for are both unscopable, and an unscopable
// envelope is never fanned out workspace-wide.
func TestApprovalWithNoTargetTypeIsNotDelivered(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := readerOf("project", principal.ObjectGrant{Read: true})
	targetID := ids.NewV7()

	if ok, err := s.approvalShapeVisible(ctx, nil, nil); ok || err != nil {
		t.Errorf("target-less approval = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := s.approvalShapeVisible(ctx, nil, &targetID); ok || err != nil {
		t.Errorf("approval carrying an id with no type = (%v, %v), want (false, nil)", ok, err)
	}
}

// Every staged target type the fan-out can meet, and the rule it takes. The
// classification is the whole disclosure decision: an arm wider than the store
// owning the target sends a record's staged change over a webhook the API would
// refuse that owner, and a type with NO arm is an approval.requested silently
// dropped, so nobody learns authority is waiting.
func TestEveryApprovalTargetTakesItsOwningStoresRule(t *testing.T) {
	for _, c := range []struct {
		targetType string
		want       approvalTargetRule
		because    string
	}{
		{"person", targetRuleRowScoped, "person rows carry owner_id and the read path scopes them own/team/all"},
		{"project", targetRuleRowScoped, "project rows carry owner_id, exactly like their deal and lead neighbours"},
		{"list", targetRuleRowScoped, "collections' list reads ARE auth.EnsureVisible over `list`"},
		{"offer", targetRuleInheritedScope, "an offer carries no owner_id — its sensitivity is its parent deal's"},
		{"relationship", targetRuleInheritedScope, "an edge inherits the conjunction of its endpoints' scope"},
		{"saved_view", targetRuleOwnerOnly, "the saved-view store reads back on `id AND owner_id`, which own/team/all is wider than"},
		{"tag", targetRuleSharedConfig, "a tag carries no owner_id; its store's reads are object-gated and workspace-wide"},
		{"webhook_subscription", targetRuleSharedConfig, "subscription reads apply NO owner predicate — the object grant governs"},
		{"offer_template", targetRuleSharedConfig, "a template is workspace-shared branding config with no row scope"},
		{"fx_rate", targetRuleActingWorkspace, "a proposal for a brand-new currency pair has no rate row yet, so the target IS the workspace"},
		{"ai_model_rate", targetRuleActingWorkspace, "the model rate sheet is the same effective-dated, workspace-scoped shape"},
		{"chartreuse", targetRuleNone, "a type nobody classified must fail closed rather than borrow a neighbour's rule"},
	} {
		t.Run(c.targetType, func(t *testing.T) {
			if got := approvalTargetRuleFor(c.targetType); got != c.want {
				t.Errorf("approvalTargetRuleFor(%q) = %d, want %d — %s", c.targetType, got, c.want, c.because)
			}
			if got := ApprovalTargetClassified(c.targetType); got != (c.want != targetRuleNone) {
				t.Errorf("ApprovalTargetClassified(%q) = %v — the predicate the composition layer's parity "+
					"gate reads must report the rule this switch dispatches on", c.targetType, got)
			}
		})
	}
}

// The owner-only arm rides the SAME object-read floor every row-scoped arm
// applies; only the row half differs. A fan-out owner whose live role no longer
// grants saved_view.read must not learn a colleague's — or their own — view was
// staged for archive.
//
// The nil pool proves the short-circuit: were the object-read half dropped, the
// ownership probe would drive it into the pool and fail loudly instead of
// answering the clean deny below.
func TestAnOwnerOnlyApprovalTargetRequiresObjectReadCapability(t *testing.T) {
	s := NewStore(nil, nil)
	viewID := ids.NewV7()

	if ok, err := s.approvalTargetVisible(readerOf("saved_view", principal.ObjectGrant{Delete: true}), "saved_view", viewID); ok || err != nil {
		t.Fatalf("staged view archive to an owner without saved_view.read = (%v, %v), want (false, nil)", ok, err)
	}

	// The same owner WITH the read grant passes the object-read half and reaches
	// the ownership probe, which on a nil pool fails loudly — anything but the
	// clean deny above — proving the denial came from the missing grant.
	func() {
		//craft:ignore swallowed-errors recover's value is deliberately discarded: a nil-pool probe panic is itself proof the read gate admitted the call and reached the probe
		defer func() { _ = recover() }()
		ctx := readerOf("saved_view", principal.ObjectGrant{Read: true})
		if ok, err := s.approvalTargetVisible(ctx, "saved_view", viewID); !ok && err == nil {
			t.Fatal("with saved_view.read, approvalTargetVisible returned a clean deny — the read gate must " +
				"have admitted it and reached the ownership probe")
		}
	}()
}

// The rate-sheet arm, whose target is a WORKSPACE rather than a record: a
// proposal for a currency or model the sheet has never priced has no row, so the
// floor is the object-read grant plus the identity of the workspace named.
//
// Both halves are asserted because each alone is a real defect. Without the read
// grant a rep who owns a subscription would receive proposed FX and model pricing
// that no surface will show them; without the workspace check a proposal naming
// some other tenant's sheet would be announced as if it were this one's. The nil
// pool proves the arm reaches no table at all.
func TestARateSheetApprovalNeedsTheReadGrantAndThisWorkspace(t *testing.T) {
	s := NewStore(nil, nil)
	here := ids.NewV7()
	elsewhere := ids.NewV7()

	for _, targetType := range []string{"fx_rate", "ai_model_rate"} {
		t.Run(targetType, func(t *testing.T) {
			admin := principal.WithWorkspaceID(readerOf(targetType, principal.ObjectGrant{Read: true}), here)
			if ok, err := s.approvalTargetVisible(admin, targetType, here); !ok || err != nil {
				t.Errorf("proposal against this workspace, owner holding %s.read = (%v, %v), want (true, nil)",
					targetType, ok, err)
			}
			if ok, err := s.approvalTargetVisible(admin, targetType, elsewhere); ok || err != nil {
				t.Errorf("proposal naming ANOTHER workspace = (%v, %v), want (false, nil) — the accepted effect "+
					"writes to the acting workspace's sheet, not the claimed one", ok, err)
			}

			// A rep holds no grant on either sheet at all (the seeded RBAC gives
			// both to admin/ops alone), which is exactly why this arm carries the
			// read half its shared-config neighbours do not.
			rep := principal.WithWorkspaceID(readerOf("deal", principal.ObjectGrant{Read: true}), here)
			if ok, err := s.approvalTargetVisible(rep, targetType, here); ok || err != nil {
				t.Errorf("proposal to an owner without %s.read = (%v, %v), want (false, nil)", targetType, ok, err)
			}

			// No workspace bound is not "any workspace": it fails closed rather
			// than comparing the target against the nil UUID.
			unbound := readerOf(targetType, principal.ObjectGrant{Read: true})
			if ok, err := s.approvalTargetVisible(unbound, targetType, here); ok || err != nil {
				t.Errorf("proposal with no workspace bound = (%v, %v), want (false, nil)", ok, err)
			}
		})
	}
}

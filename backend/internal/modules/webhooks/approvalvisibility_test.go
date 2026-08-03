// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// The approval fan-out gate, in the three dimensions that resolve without a
// database: which halves of the staged target the envelope carries, how the
// target's TYPE is classified, and the object-read floor every classification
// rides. Each shape has its own floor, and each classification has to mirror the
// read rule of the store that owns the target. The direct-subject path's own
// object-read hardening is covered in unit_test.go; the database-backed row
// probes live in the compose integration lane.

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

// writerWithoutReadOn is a fan-out owner holding every grant on one object
// EXCEPT read, at the widest row scope — so nothing but the missing read grant
// can be what withholds a delivery from them.
func writerWithoutReadOn(object string) context.Context {
	return readerOf(object, principal.ObjectGrant{Create: true, Update: true, Delete: true})
}

// The object-READ grant on the staged target's own type is the floor under EVERY
// arm, and the subject set is this gate's own classification table rather than a
// list of the arms — an arm added later inherits the assertion instead of waiting
// for someone to extend a list. It is the same enumeration the composition
// layer's parity gate reads (ClassifiedApprovalTargetTypes).
//
// The hole it closes is the one the seeded roles hide: a role document granting
// `tag.delete` with `tag.read` false is valid, and the fan-out payload carries
// summary and edited_change to a subscription owner with no entry gate of its own
// — so a floor that held for the row-scoped arms and not for shared config made
// the WIDER disclosure the laxer surface.
//
// The nil pool is the assertion that the floor answers before any query is
// issued; an arm reached without it dereferences the pool, which is recovered
// here so the failure names the invariant instead of surfacing as a panic in
// whichever arm ran.
func TestEveryClassifiedApprovalTargetRidesTheObjectReadFloor(t *testing.T) {
	s := NewStore(nil, nil)
	target := ids.NewV7()

	for _, targetType := range ClassifiedApprovalTargetTypes() {
		t.Run(targetType, func(t *testing.T) {
			defer func() {
				if reached := recover(); reached != nil {
					t.Errorf("the gate reached its row arm without the object-read floor (%v) — the floor must "+
						"answer above every arm, so a new arm inherits it", reached)
				}
			}()
			ctx := principal.WithWorkspaceID(writerWithoutReadOn(targetType), target)
			ok, err := s.approvalTargetVisible(ctx, targetType, target)
			if err != nil {
				t.Fatalf("approvalTargetVisible: %v — a missing read grant is an ANSWER, not an error", err)
			}
			if ok {
				t.Errorf("an owner holding every %s grant EXCEPT read receives the staged change against one — "+
					"the fan-out would disclose a record the API refuses them", targetType)
			}
		})
	}
}

// The owner-only arm's row half is OWNERSHIP, and the floor admits before it
// rather than instead of it. The negative — no read grant, no delivery — is
// derived for every classified type by the gate above; what this asserts is that
// a grant-holding owner is not refused by the floor and does reach the ownership
// probe, so the deny above can only have come from the missing grant.
//
// The nil pool is the proof: reaching the probe fails loudly, which is anything
// but a clean deny.
func TestTheOwnerOnlyArmIsReachedWhenTheReadFloorAdmits(t *testing.T) {
	s := NewStore(nil, nil)
	viewID := ids.NewV7()

	//craft:ignore swallowed-errors recover's value is deliberately discarded: a nil-pool probe panic is itself proof the read gate admitted the call and reached the probe
	defer func() { _ = recover() }()
	ctx := readerOf("saved_view", principal.ObjectGrant{Read: true})
	if ok, err := s.approvalTargetVisible(ctx, "saved_view", viewID); !ok && err == nil {
		t.Fatal("with saved_view.read, approvalTargetVisible returned a clean deny — the read gate must " +
			"have admitted it and reached the ownership probe")
	}
}

// The rate-sheet arm, whose target is a WORKSPACE rather than a record: a
// proposal for a currency or model the sheet has never priced has no row, so its
// row half is the identity of the workspace named. A proposal naming some other
// tenant's sheet must not be announced as if it were this one's — the accepted
// effect writes to the acting workspace's sheet.
//
// The read half this arm also owes is derived for every classified type by the
// floor gate above; the nil pool proves this arm reaches no table at all.
func TestARateSheetApprovalIsAnnouncedOnlyForThisWorkspace(t *testing.T) {
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

			// No workspace bound is not "any workspace": it fails closed rather
			// than comparing the target against the nil UUID.
			unbound := readerOf(targetType, principal.ObjectGrant{Read: true})
			if ok, err := s.approvalTargetVisible(unbound, targetType, here); ok || err != nil {
				t.Errorf("proposal with no workspace bound = (%v, %v), want (false, nil)", ok, err)
			}
		})
	}
}

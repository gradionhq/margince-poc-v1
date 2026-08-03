// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

// The shape half of the approval fan-out gate: what an approval.requested
// envelope discloses depends on which halves of the staged target it carries,
// and each shape has its own floor. The row-scoped branches (and the
// object-read hardening they share) are covered in unit_test.go; the
// database-backed paths live in the compose integration lane.

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

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// A buyer principal is ATTRIBUTED and never ADMITTED: it names an external
// person in one Deal Room so the audit log can say who acted, and it carries no
// authority of its own. Every gate in this package must refuse it.
//
// The refusal has to hold for a reason stronger than "a buyer happens to hold
// no permissions". A test that hands the gate a zero Permissions value proves
// only that the zero value grants nothing — it passes identically for a
// connector, and it would keep passing if the buyer kind were deleted. So the
// buyer here is given MAXIMAL authority: every object, every action, row scope
// all, and the admin role key. What refuses it is the kind, or nothing does.
package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// buyerObjects are the objects a buyer would reach for if a gate let it: the
// deal its room hangs off, and the records that deal names.
var buyerObjects = []string{"deal", "person", "organization", "activity"}

// overreachingBuyer is a buyer principal carrying authority no real one is ever
// minted with. Nothing constructs this in production — it exists so the tests
// below fail when a gate consults the permissions instead of the kind.
func overreachingBuyer() principal.Principal {
	objects := make(map[string]principal.ObjectGrant, len(buyerObjects))
	for _, object := range buyerObjects {
		objects[object] = principal.ObjectGrant{
			Create: true, Read: true, Update: true, Delete: true,
		}
	}
	return principal.Principal{
		Type:     principal.PrincipalBuyer,
		ID:       "buyer:0199a1f4-0000-7000-8000-000000000001",
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			Objects:  objects,
			RowScope: principal.RowScopeAll,
		},
	}
}

func TestBuyerHoldsNoObjectGrantEvenWhenItCarriesOne(t *testing.T) {
	ctx := principal.WithActor(context.Background(), overreachingBuyer())
	for _, object := range buyerObjects {
		for _, action := range []principal.Action{
			principal.ActionRead, principal.ActionCreate,
			principal.ActionUpdate, principal.ActionDelete,
		} {
			err := Require(ctx, object, action)
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("Require(buyer, %s, %s) = %v, want ErrPermissionDenied",
					object, action, err)
			}
		}
	}
}

func TestBuyerIsNotUnboundedEvenAtRowScopeAll(t *testing.T) {
	// Unbounded answers "does this actor see every row". A buyer must never
	// reach it — including one whose Permissions claim RowScopeAll, because a
	// buyer's row scope is its room and nothing in Permissions describes that.
	if Unbounded(overreachingBuyer()) {
		t.Error("Unbounded(buyer with RowScopeAll) = true, want false")
	}
}

func TestHumanOnlyOperationsRefuseABuyerAndStillAdmitAHuman(t *testing.T) {
	// The refusal arm. RequireHuman was written as "refuse agents", and a buyer
	// is not an agent, so without its own clause every human-only sheet in the
	// product would admit one.
	buyer := principal.WithActor(context.Background(), overreachingBuyer())
	if err := RequireHuman(buyer); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("RequireHuman(buyer) = %v, want ErrPermissionDenied", err)
	}

	// The admit arm, without which a clause broadened to refuse EVERYONE would
	// leave this test green over a dead product.
	human := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:someone",
	})
	if err := RequireHuman(human); err != nil {
		t.Fatalf("RequireHuman(human) = %v, want nil", err)
	}
}

func TestBuyerReadsNoIdentityTableInFull(t *testing.T) {
	// readsEveryRow answers "does this read carry no owner predicate", and its
	// identity-table arm is true for ANY actor — which for a buyer would mean
	// every person, organization, lead, deal and project in the installation.
	// The kind is answered before the table is.
	for _, table := range []string{"person", "organization", "lead", "deal", "project"} {
		if readsEveryRow(overreachingBuyer(), table) {
			t.Errorf("readsEveryRow(buyer, %s) = true, want false", table)
		}
	}
}

func TestBuyerIsRefusedByAdminOnlyOperations(t *testing.T) {
	// RequireAdmin reads RoleKeys, so a buyer carrying the admin key is the
	// case that matters: the kind must refuse before the role key is consulted.
	ctx := principal.WithActor(context.Background(), overreachingBuyer())
	if err := RequireAdmin(ctx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("RequireAdmin(buyer holding the admin role key) = %v, want ErrPermissionDenied", err)
	}
}

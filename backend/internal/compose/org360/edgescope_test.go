// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func graphReader(objects map[string]principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, Objects: objects, RowScope: principal.RowScopeAll,
		},
	})
}

// The graph's group loop decides between naming a group in groups_omitted and
// failing the whole card by asking errors.Is on this exact sentinel, so it has
// to arrive unwrapped. Holding BOTH endpoint grants is the point: that is
// precisely the situation the edge grant exists for.
func TestEdgeScopeRefusesACallerHoldingOnlyTheEndpoints(t *testing.T) {
	ctx := graphReader(map[string]principal.ObjectGrant{
		"person": {Read: true}, "organization": {Read: true}, "deal": {Read: true},
	})
	clause, err := edgeScope(ctx, "r", func(any) int { return 1 })
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("edgeScope(endpoints only) = %v, want ErrPermissionDenied unwrapped", err)
	}
	if clause != "" {
		t.Errorf("a refused caller was handed a clause: %q", clause)
	}
}

func TestEdgeScopeAnswersScopeAllWhenNothingBoundsTheCaller(t *testing.T) {
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:test",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
	clause, err := edgeScope(ctx, "r", func(any) int { return 1 })
	if err != nil {
		t.Fatalf("edgeScope(system) = %v, want admission", err)
	}
	if clause != scopeAll {
		t.Errorf("edgeScope(unbounded) = %q, want %q — the statements interpolate this, and an empty "+
			"string does not compile", clause, scopeAll)
	}
}

// contactDealRoles swallows the refusal into an EMPTY MAP rather than an error,
// because `deal_roles` is required on every contact card and contactsSection
// normalises a missing entry to []. The roster still renders; the roles are
// simply not there. It needs no transaction, which is the proof that the
// refusal is resolved before any statement.
func TestContactDealRolesYieldsAnEmptyMapWithoutTheEdgeGrant(t *testing.T) {
	ctx := graphReader(map[string]principal.ObjectGrant{"deal": {Read: true}})
	roles, err := contactDealRoles(ctx, nil, ids.From[ids.OrganizationKind](ids.NewV7()),
		[]ids.PersonID{ids.From[ids.PersonKind](ids.NewV7())})
	if err != nil {
		t.Fatalf("contactDealRoles(no edge grant) = %v, want an empty map — a required field must not "+
			"go absent and take the card down", err)
	}
	if roles == nil {
		t.Error("contactDealRoles returned a nil map: contactsSection would then leave deal_roles nil, " +
			"which marshals as null on a required field")
	}
	if len(roles) != 0 {
		t.Errorf("a refused caller got %d contacts' roles", len(roles))
	}
}

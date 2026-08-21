// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A buyer principal exists to be ATTRIBUTED, never to be admitted. It names an
// external person in one Deal Room so the audit log can say who acted; it
// carries no seat, no RBAC grant and no row scope, and every general-purpose
// gate in this package must refuse it on that basis alone.
//
// These tests are the proof that the kind is inert here. The Deal Room's own
// public store methods admit a buyer through the room predicate they carry —
// nothing in platform/auth does, and a future change that made one of these
// pass would hand an external visitor the run of the workspace.
func buyerCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalBuyer,
		ID:   "buyer:0199a1f4-0000-7000-8000-000000000001",
	})
}

func TestBuyerHoldsNoObjectGrant(t *testing.T) {
	// The zero Permissions value grants nothing, and a buyer is never given
	// another. Require must refuse every object it is asked about rather than
	// falling through the system principal's unconditional pass above it.
	for _, object := range []string{"deal", "person", "organization", "activity"} {
		for _, action := range []principal.Action{
			principal.ActionRead, principal.ActionCreate,
			principal.ActionUpdate, principal.ActionDelete,
		} {
			err := Require(buyerCtx(), object, action)
			if !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("Require(buyer, %s, %s) = %v, want ErrPermissionDenied",
					object, action, err)
			}
		}
	}
}

func TestBuyerIsNotUnbounded(t *testing.T) {
	// Unbounded answers "does this actor see every row". It is true for the
	// system principal, and a buyer must never be folded into that clause: a
	// row-scope predicate that resolved to "all" for a buyer would serve one
	// room's visitor the whole estate.
	buyer := principal.Principal{Type: principal.PrincipalBuyer, ID: "buyer:x"}
	if Unbounded(buyer) {
		t.Error("Unbounded(buyer) = true, want false — a buyer sees no CRM row")
	}
}

func TestBuyerIsRefusedByHumanOnlyOperations(t *testing.T) {
	// RequireHuman refuses agents. A buyer is not an agent, so without its own
	// clause every human-only sheet in the product would have admitted one.
	if err := RequireHuman(buyerCtx()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("RequireHuman(buyer) = %v, want ErrPermissionDenied", err)
	}
}

func TestBuyerIsNotAttributedToTheInstallation(t *testing.T) {
	// The whole point of the kind: a buyer's action is theirs, not the
	// system's. AuthzRule returning "system" here would put the installation's
	// name on a person's decision in an append-only ledger.
	buyer := principal.Principal{Type: principal.PrincipalBuyer, ID: "buyer:x"}
	if rule := AuthzRule(buyer, "deal", "update"); rule == "system" {
		t.Error(`AuthzRule(buyer) = "system", want anything else — a buyer is not the installation`)
	}
}

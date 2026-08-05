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

func TestRequireHumanRejectsOnlyAgents(t *testing.T) {
	// The agent (Passport) principal is refused whatever its authority — the
	// human-only sheet must never answer an agent bearer, even one minted by
	// an admin with the object grant.
	agentCtx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:test",
	})
	if err := RequireHuman(agentCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("RequireHuman(agent) = %v, want ErrPermissionDenied", err)
	}

	// Human, connector and system are not agents and pass.
	for _, typ := range []principal.PrincipalType{
		principal.PrincipalHuman, principal.PrincipalConnector, principal.PrincipalSystem,
	} {
		ctx := principal.WithActor(context.Background(), principal.Principal{Type: typ, ID: "id"})
		if err := RequireHuman(ctx); err != nil {
			t.Errorf("RequireHuman(%s) = %v, want nil", typ, err)
		}
	}
}

func TestRequireHumanNeedsAnActor(t *testing.T) {
	// A missing actor is a programming error (middleware always binds one),
	// surfaced as an error rather than a silent pass.
	if err := RequireHuman(context.Background()); err == nil {
		t.Fatal("RequireHuman(no actor) = nil, want an error")
	}
}

// grantedCtx binds a human holding exactly one object grant.
func grantedCtx(object string, g principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
		Permissions: principal.Permissions{
			RoleKeys: []string{"fixture"},
			Objects:  map[string]principal.ObjectGrant{object: g},
		},
	})
}

// RequireAny and UpsertAction are the two halves of the rate-sheet admission
// pair: the upfront half admits a principal who could write the sheet at all,
// the specific half decides which grant the write it turned out to be needs.
// Holding one grant must therefore pass the first check and still be refused
// the write the other grant governs — the whole point of the split.
func TestRequireAnyAdmitsEitherGrantAndUpsertActionSeparatesThem(t *testing.T) {
	const object = "fx_rate"
	cases := map[string]struct {
		grant                   principal.ObjectGrant
		admitted                bool
		mayInsert, mayOverwrite bool
	}{
		"create only": {grant: principal.ObjectGrant{Create: true}, admitted: true, mayInsert: true},
		"update only": {grant: principal.ObjectGrant{Update: true}, admitted: true, mayOverwrite: true},
		"create and update": {
			grant:    principal.ObjectGrant{Create: true, Update: true},
			admitted: true, mayInsert: true, mayOverwrite: true,
		},
		"read only": {grant: principal.ObjectGrant{Read: true}},
	}
	// denied asserts the 403 sentinel every object refusal answers with.
	denied := func(t *testing.T, what string, err error, want bool) {
		t.Helper()
		switch {
		case want && err != nil:
			t.Errorf("%s = %v, want admitted", what, err)
		case !want && !errors.Is(err, apperrors.ErrPermissionDenied):
			t.Errorf("%s = %v, want ErrPermissionDenied", what, err)
		}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := grantedCtx(object, tc.grant)
			denied(t, "RequireAny(create|update)",
				RequireAny(ctx, object, principal.ActionCreate, principal.ActionUpdate), tc.admitted)
			denied(t, "Require(UpsertAction(inserting))",
				Require(ctx, object, UpsertAction(false)), tc.mayInsert)
			denied(t, "Require(UpsertAction(replacing))",
				Require(ctx, object, UpsertAction(true)), tc.mayOverwrite)
		})
	}
}

func TestRequireAnyAdmitsTheSystemPrincipal(t *testing.T) {
	// Approval-effect writes apply under the system principal, which holds no
	// role at all: RequireAny must short-circuit exactly like Require, or the
	// rate-refresh apply would deny itself.
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "agent:rate-refresh",
	})
	if err := RequireAny(ctx, "fx_rate", principal.ActionCreate, principal.ActionUpdate); err != nil {
		t.Fatalf("RequireAny(system) = %v, want nil", err)
	}
}

func TestRequireAnyNeedsAnActor(t *testing.T) {
	err := RequireAny(context.Background(), "fx_rate", principal.ActionCreate)
	if err == nil {
		t.Fatal("RequireAny(no actor) = nil, want an error")
	}
}

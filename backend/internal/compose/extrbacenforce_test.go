// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The declared RBAC object, ENFORCED.
//
// Registering an object into the vocabulary is half a capability: it lets a
// role document grant it and /me report the holder's grant. Before this, that
// was ALL it did — the core gate decides scope ∧ seat ∧ tier ∧ quota and knows
// nothing about objects, so `x-rbac-object` gated nothing and a screen that
// hid its controls on the /me answer was hiding them from a principal who
// could still reach the same operation through the agent.
//
// The check lives in extensionTool.Handle, which is the ONE place the two
// surfaces converge: a mounted REST route and an MCP tools/call both arrive
// through Registry.Invoke. Each test below therefore names WHICH path it
// drives, and the pair is the point — a check that closed only one of them
// would look identical from either side alone.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// grantingSeat is fullSeat with an object grant: the authority the gate
// re-derives at admission for an AGENT principal, which is what lands on the
// context the handler reads. Grants are held here rather than stamped on the
// principal precisely because that is the live path for an agent — a test that
// stamped Permissions directly would pass even if the gate overwrote them,
// which it does.
//
// It does NOT overwrite them for a human: Gate.Admit returns early for a
// non-agent, so a human's grants are whatever the request's cookie resolve
// loaded. TestTheObjectBindsAHumanPrincipalToo covers that path, and it stamps
// its principal directly because that is how the real one arrives.
type grantingSeat struct{ grant principal.ObjectGrant }

func (s grantingSeat) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: principal.Permissions{
		RoleKeys: []string{"test"},
		Objects:  map[string]principal.ObjectGrant{noteObject: s.grant},
	}}, nil
}

func (grantingSeat) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

// noteObject and the verb below mirror what crm-demo's fragment declares: an
// object in the unit's namespace, and the one action the route needs.
const noteObject = "ext_demo_note"

// gatedVerb is unitVerb plus the declared grant.
func gatedVerb(tool string, action extension.RbacAction) extension.Verb {
	v := unitVerb("demo", tool, extension.TierAutoExecute, extension.ScopeRead)
	v.RbacObject = noteObject
	v.RbacAction = action
	return v
}

// gatedRegistry composes one handler-bearing tool gated on noteObject and
// registers it behind a gate holding grant. It reports whether the handler ran,
// because "denied" and "ran and returned nothing" are the two outcomes this
// file has to tell apart.
func gatedRegistry(t *testing.T, action extension.RbacAction, grant principal.ObjectGrant) (*agents.Registry, *bool) {
	t.Helper()
	ran := false
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "list_notes",
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				ran = true
				return json.RawMessage(`{"notes":[]}`), nil
			},
		}},
	}}, []extension.Verb{gatedVerb("list_notes", action)})
	if err != nil {
		t.Fatal(err)
	}
	r := agents.NewRegistry(nil, auth.NewGate(grantingSeat{grant: grant}))
	for _, tool := range tools {
		r.Register(tool)
	}
	return r, &ran
}

// extAgentCtx is a read-scoped agent principal — one the gate ADMITS, so anything
// that refuses below refused on the object and not on scope, seat or tier.
func extAgentCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
}

// TestTheDeclaredRbacObjectIsEnforcedOnTheAgentPath — the MCP/tools/call side.
func TestTheDeclaredRbacObjectIsEnforcedOnTheAgentPath(t *testing.T) {
	t.Run("a principal without the grant is refused", func(t *testing.T) {
		r, ran := gatedRegistry(t, extension.RbacRead, principal.ObjectGrant{})
		_, err := r.Invoke(extAgentCtx(), "list_notes", json.RawMessage(`{}`))
		if !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied — the scope admitted, so only the object can refuse", err)
		}
		if *ran {
			t.Error("the handler ran anyway: the refusal has to land BEFORE a live Runtime is minted")
		}
	})

	t.Run("a principal holding the grant is served", func(t *testing.T) {
		r, ran := gatedRegistry(t, extension.RbacRead, principal.ObjectGrant{Read: true})
		if _, err := r.Invoke(extAgentCtx(), "list_notes", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("a granted principal must be served: %v", err)
		}
		if !*ran {
			t.Error("the handler did not run")
		}
	})

	t.Run("the grant is required for the DECLARED action, not any action", func(t *testing.T) {
		// A principal granted delete on the object, calling a route that
		// declares create. A check that asked "any write verb" — the shape
		// available without x-rbac-action — admits this, and the screen that
		// hides Add on `create` would then be lying in the other direction.
		r, ran := gatedRegistry(t, extension.RbacCreate, principal.ObjectGrant{Read: true, Delete: true})
		_, err := r.Invoke(extAgentCtx(), "list_notes", json.RawMessage(`{}`))
		if !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		if *ran {
			t.Error("a delete grant satisfied a create route")
		}
	})
}

// TestTheDeclaredRbacObjectIsEnforcedOnTheRestPath drives the MOUNTED ROUTE,
// through the same handler the SPA reaches, and asserts the refusal arrives as
// a 403 rather than as an opaque 500.
//
// It is the second half of the pair: the route hands the body to Invoke, so it
// rides the identical check — and this is what proves that claim rather than
// restating it.
func TestTheDeclaredRbacObjectIsEnforcedOnTheRestPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant principal.ObjectGrant
		want  int
	}{
		{"no grant", principal.ObjectGrant{}, http.StatusForbidden},
		{"the declared grant", principal.ObjectGrant{Read: true}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := gatedRegistry(t, extension.RbacRead, tc.grant)
			verb := gatedVerb("list_notes", extension.RbacRead)
			mux := http.NewServeMux()
			if _, err := MountExtensionRoutes(mux, []extension.Verb{verb},
				map[string]bool{verbKey(verb.Unit, "list_notes"): true}, r.Invoke); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, verb.ServedPath(), strings.NewReader(`{}`))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req.WithContext(extAgentCtx()))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestAToolDeclaringNoObjectIsUnaffected: de, yogi and crm-hello own no
// records and declare no object, so nothing about them may become
// conditional on a grant nobody can hold. Without this, the natural
// implementation — require the object always — would refuse every unit tool in
// the tree.
func TestAToolDeclaringNoObjectIsUnaffected(t *testing.T) {
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: "give_quote",
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		}},
	}}, []extension.Verb{unitVerb("demo", "give_quote", extension.TierAutoExecute, extension.ScopeRead)})
	if err != nil {
		t.Fatal(err)
	}
	r := agents.NewRegistry(nil, auth.NewGate(grantingSeat{}))
	for _, tool := range tools {
		r.Register(tool)
	}
	if _, err := r.Invoke(extAgentCtx(), "give_quote", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("an objectless tool must serve unchanged: %v", err)
	}
}

// TestTheObjectBindsAHumanPrincipalToo, which is not a detail: Gate.Admit
// returns early for a non-agent — "their authority is their RBAC, enforced at
// the store" — so a check placed INSIDE the gate would leave every human
// caller of an extension route ungated. The SPA's caller is a human.
//
// It also means the human path gets no re-derivation from Admit: the grants
// this reads are the ones the request already carries. That is current — the
// cookie resolve loads them per request — but it is a different mechanism from
// the agent path's, and the two are worth not confusing.
func TestTheObjectBindsAHumanPrincipalToo(t *testing.T) {
	r, ran := gatedRegistry(t, extension.RbacRead, principal.ObjectGrant{})
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:t", UserID: ids.NewV7(),
	})
	_, err := r.Invoke(ctx, "list_notes", json.RawMessage(`{}`))
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied for a human holding no grant", err)
	}
	if *ran {
		t.Error("the handler ran for an ungranted human")
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The SECRETS operations' grant, enforced — R1.
//
// extrbacenforce_test.go proves the mechanism over a note-shaped fixture: read
// and create, one object, refused on both transports. That mechanism was already
// sound when the UAT re-run found R1. The defect was that crm-demo's three
// SECRETS operations declared no object at all, so the mechanism had nothing to
// enforce and any authenticated seat — read-only included — could replace the
// installation's signing key on either transport, after which every signature it
// produced was made with the attacker's key.
//
// The fix has two halves and they are tested in two places, because neither
// place can see the other:
//
//   - the DECLARATION rule, in pkg/extension: Verb.Validate now refuses a
//     mutating operation that names no RBAC object, at generation and at boot.
//     That is the half that closes the class for every future unit.
//   - the BEHAVIOUR, here: the declared pair crm-demo now carries is actually
//     refused for a read-only seat, on the agent path AND on the mounted route.
//
// A note on what this file does NOT do: it does not read ComposedVerbs(). That
// set is a boot binding written by RegisterExtensions at a role main, so it is
// empty in this lane, and a test that looked there would SKIP — a vacuous green
// over exactly the defect it exists for.

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

// objectSeat is grantingSeat over an arbitrary object map, so a test can hold
// the grant shape a REAL seat holds rather than one object's worth.
type objectSeat struct {
	objects map[string]principal.ObjectGrant
}

func (s objectSeat) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: principal.Permissions{
		RoleKeys: []string{"test"}, Objects: s.objects,
	}}, nil
}

func (objectSeat) SeatType(context.Context, ids.UUID, ids.UUID) (principal.SeatType, error) {
	return principal.SeatFull, nil
}

// signingKeyVerb is crm-demo's store-signing-key declaration, spelled out.
//
// NOT read from ComposedVerbs(): the composed set is a BOOT binding written by
// RegisterExtensions at a role main, so it is empty in this lane and a test that
// looked there would skip — a vacuous green over exactly the defect it exists
// for. The declaration side is pinned where it can be pinned: Verb.Validate now
// refuses a mutating operation that names no object at all (pkg/extension), and
// crm-demo's own fragment carries these values. What THIS file adds is the
// behaviour: that the declared pair is actually refused, on both transports, for
// the seat that overwrote the key.
// unitVerbBare, NOT unitVerb: the shared builder auto-declares an object for a
// mutating scope (see extensiontools_test.go), which would make this file unable
// to express the shape it exists to refuse — and would quietly absorb any
// mutation of the pair below.
func signingKeyVerb() extension.Verb {
	v := unitVerbBare("crm-demo", "demo_store_signing_key", extension.TierAutoExecute, extension.ScopeWrite)
	v.Route = "/ext/crm-demo/signing-key"
	v.RbacObject = "ext_crm_demo_signing_key"
	v.RbacAction = extension.RbacUpdate
	return v
}

// signingKeyReadVerb is the same object's read side — status and signature both
// gate on it — used to show the fix is a narrowing rather than a lockout.
func signingKeyReadVerb(tool string) extension.Verb {
	v := unitVerbBare("crm-demo", tool, extension.TierAutoExecute, extension.ScopeRead)
	v.RbacObject = "ext_crm_demo_signing_key"
	v.RbacAction = extension.RbacRead
	return v
}

// registryForComposedVerb registers ONE handler behind the live declaration of
// verb, so everything the gate and the object check read — tier, scope, object,
// action — is the composed set's own and only the behavior is the test's.
func registryForComposedVerb(t *testing.T, verb extension.Verb, seat objectSeat) (*agents.Registry, *bool) {
	t.Helper()
	ran := false
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: verb.Unit, Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: verb.Tool,
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				ran = true
				return json.RawMessage(`{"stored":true}`), nil
			},
		}},
	}}, []extension.Verb{verb})
	if err != nil {
		t.Fatalf("composing %s: %v", verb.Tool, err)
	}
	r := agents.NewRegistry(nil, auth.NewGate(seat))
	for _, tool := range tools {
		r.Register(tool)
	}
	return r, &ran
}

// readOnlySeatCtx is the principal the UAT re-run attacked with: a HUMAN, since
// that is what the SPA's cookie session produces and what actually overwrote the
// key. A human's grants are read off the context — Gate.Admit returns early for
// a non-agent — so they are stamped here, which is how the real one arrives.
func readOnlySeatCtx(objects map[string]principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:readonly", UserID: ids.NewV7(),
		Permissions: principal.Permissions{RoleKeys: []string{"readonly"}, Objects: objects},
	})
}

// agentSeatCtx is the same seat's passport reaching the agent transport. Read
// scope, so anything refused below refused on the OBJECT — the store verb
// declares write, and a scope refusal would prove nothing about the grant.
func agentSeatCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:readonly", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
	})
}

// TestAReadOnlySeatCannotReplaceTheComposedSigningKey is R1, on both transports.
//
// The seat holds `read` on the signing-key object and not `update` — which is
// exactly what a read-only role looks like after the fix, and what the UAT's
// read-only user held when it overwrote the installation's credential and every
// subsequent signature came out of the attacker's key.
//
// Both arms are needed and neither substitutes for the other: the UAT
// demonstrated the overwrite through the REST route AND through a tools/call on
// the same passport, and a check that closed only one would look identical from
// the other side.
func TestAReadOnlySeatCannotReplaceTheComposedSigningKey(t *testing.T) {
	verb := signingKeyVerb()
	readOnly := map[string]principal.ObjectGrant{verb.RbacObject: {Read: true}}

	t.Run("the agent path refuses", func(t *testing.T) {
		r, ran := registryForComposedVerb(t, verb, objectSeat{objects: readOnly})
		_, err := r.Invoke(agentSeatCtx(), verb.Tool, json.RawMessage(`{"key":"AGENT-RO-OVERWRITE-ATTEMPT"}`))
		if !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		if *ran {
			t.Error("the handler ran: the key would already be replaced by the time anything refused")
		}
	})

	t.Run("the REST route refuses", func(t *testing.T) {
		r, ran := registryForComposedVerb(t, verb, objectSeat{objects: readOnly})
		mux := http.NewServeMux()
		if _, err := MountExtensionRoutes(mux, []extension.Verb{verb},
			map[string]bool{verbKey(verb.Unit, verb.Tool): true}, r.Invoke); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(verb.Method, verb.ServedPath(),
			strings.NewReader(`{"key":"READONLY-SEAT-OVERWROTE-THIS"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(readOnlySeatCtx(readOnly)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body)
		}
		if *ran {
			t.Error("the handler ran behind a non-200")
		}
	})

	// The positive control, without which a blanket refusal passes both arms
	// above and the capability is simply dead.
	t.Run("a seat holding update is served", func(t *testing.T) {
		granted := map[string]principal.ObjectGrant{verb.RbacObject: {Read: true, Update: true}}
		r, ran := registryForComposedVerb(t, verb, objectSeat{objects: granted})
		if _, err := r.Invoke(agentSeatCtx(), verb.Tool, json.RawMessage(`{"key":"k"}`)); err != nil {
			t.Fatalf("a granted seat must be served: %v", err)
		}
		if !*ran {
			t.Error("the handler did not run")
		}
	})

	// And the read-only seat keeps what read-only means: it may still ask
	// whether a key is stored and still verify a signature. The fix is a
	// narrowing of one operation, not a lockout of the card.
	for _, tool := range []string{"demo_signing_key_status", "demo_sign_payload"} {
		t.Run("the read-only seat still reaches "+tool, func(t *testing.T) {
			readVerb := signingKeyReadVerb(tool)
			r, ran := registryForComposedVerb(t, readVerb, objectSeat{objects: readOnly})
			if _, err := r.Invoke(agentSeatCtx(), tool, json.RawMessage(`{"payload":"p"}`)); err != nil {
				t.Fatalf("a read grant must still serve %s: %v", tool, err)
			}
			if !*ran {
				t.Errorf("the handler for %s did not run", tool)
			}
		})
	}
}

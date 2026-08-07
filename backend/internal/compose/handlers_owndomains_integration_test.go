// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The /capture/email-domains HTTP handlers over a real pool (CAP-WIRE-2a,
// ADR-0082/A127). Thin transport, so what is proven here is the transport's
// own decisions — the wire shape, the human-only gate, and the refusals the
// handler makes BEFORE the store is reached. The RBAC and audit behaviour
// belong to the store's own integration test.
//
// The human-only gate earns a case of its own on every write: this set decides
// what mail the CRM may hold at all, and an agent widening it would suppress
// correspondence no mailbox ever offers again.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ownDomainCtx binds an actor of the given kind carrying the capture_settings
// grant the store gates on.
func ownDomainCtx(e *integration.Env, kind principal.PrincipalType, grant principal.ObjectGrant) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	id := ids.NewV7()
	prefix := "human:"
	if kind == principal.PrincipalAgent {
		prefix = "agent:"
	}
	return principal.WithActor(ctx, principal.Principal{
		Type: kind, ID: prefix + id.String(), UserID: id,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"capture_settings": grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

func ownDomainAdmin(e *integration.Env) context.Context {
	return ownDomainCtx(e, principal.PrincipalHuman, principal.ObjectGrant{Read: true, Update: true})
}

func TestOwnDomainHandlersRoundTripTheRegistry(t *testing.T) {
	e := integration.Setup(t)
	h := ownDomainHandlers{store: capture.NewOwnDomainStore(e.Pool)}

	// An empty registry is an empty list, never a null — a client rendering the
	// screen must not have to tell "none registered" from "no answer".
	listed := listOwnDomains(ownDomainAdmin(e), t, h)
	if listed.Data == nil {
		t.Fatal("GET returned a null list; an unregistered workspace has zero domains, not an absent answer")
	}

	// An admin adding a domain IS the human vouching for it, so it comes back
	// verified and sourced to the admin rather than as a candidate.
	rec := callOwnDomains(ownDomainAdmin(e), h.CreateWorkspaceEmailDomain,
		http.MethodPost, strings.NewReader(`{"domain":"Gradion.DE"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201 (body %s)", rec.Code, rec.Body)
	}
	var created crmcontracts.WorkspaceEmailDomain
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decoding the created domain: %v (body %s)", err, rec.Body)
	}
	if created.Domain != "gradion.de" {
		t.Errorf("stored domain = %q, want the normalized gradion.de — matching is done on the stored form", created.Domain)
	}
	if !created.Verified {
		t.Error("an admin-added domain came back unverified; the admin saying so is what verification means here")
	}

	// The list now carries it, and the wire shape survives the round trip.
	listed = listOwnDomains(ownDomainAdmin(e), t, h)
	if len(listed.Data) != 1 || listed.Data[0].Domain != "gradion.de" {
		t.Fatalf("list = %+v, want exactly the added gradion.de", listed.Data)
	}

	// Removal empties it again.
	delRec := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/capture/email-domains/gradion.de", nil).
		WithContext(ownDomainAdmin(e))
	h.DeleteWorkspaceEmailDomain(delRec, delReq, "gradion.de")
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204 (body %s)", delRec.Code, delRec.Body)
	}
	listed = listOwnDomains(ownDomainAdmin(e), t, h)
	if len(listed.Data) != 0 {
		t.Fatalf("list after delete = %+v, want empty", listed.Data)
	}
}

// The refusals the TRANSPORT owns, each answered before the store is reached.
func TestOwnDomainHandlersRefuseBadInputAndAgents(t *testing.T) {
	e := integration.Setup(t)
	h := ownDomainHandlers{store: capture.NewOwnDomainStore(e.Pool)}
	admin := ownDomainAdmin(e)
	agent := ownDomainCtx(e, principal.PrincipalAgent, principal.ObjectGrant{Read: true, Update: true})

	for _, tc := range []struct {
		name string
		ctx  context.Context
		body string
		want int
	}{
		{"a body that is not JSON", admin, `{"domain":`, http.StatusUnprocessableEntity},
		{"a domain that is not one", admin, `{"domain":"not a domain"}`, http.StatusUnprocessableEntity},
		{"an empty domain", admin, `{"domain":""}`, http.StatusUnprocessableEntity},
		{"an agent widening the set", agent, `{"domain":"gradion.de"}`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/capture/email-domains",
				strings.NewReader(tc.body)).WithContext(tc.ctx)
			h.CreateWorkspaceEmailDomain(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("POST status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}

	// The delete side carries the same human-only gate; a set that only writes
	// are gated on one verb is gated on neither in practice.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/capture/email-domains/gradion.de", nil).
		WithContext(agent)
	h.DeleteWorkspaceEmailDomain(rec, req, "gradion.de")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent DELETE status = %d, want 403 — narrowing the set is a human decision too", rec.Code)
	}
}

// callOwnDomains drives one handler and hands back the recorder, so each caller
// decodes into the concrete wire type the operation actually answers with.
func callOwnDomains(ctx context.Context, handler func(http.ResponseWriter, *http.Request),
	method string, body io.Reader,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v1/capture/email-domains", body)
	rec := httptest.NewRecorder()
	handler(rec, req.WithContext(ctx))
	return rec
}

// listOwnDomains reads the registry through the handler, failing the test if
// the status or the body is not the one the surface promises.
func listOwnDomains(ctx context.Context, t *testing.T, h ownDomainHandlers) crmcontracts.WorkspaceEmailDomainListResponse {
	t.Helper()
	rec := callOwnDomains(ctx, h.ListWorkspaceEmailDomains, http.MethodGet, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out crmcontracts.WorkspaceEmailDomainListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding the list response: %v (body %s)", err, rec.Body)
	}
	return out
}

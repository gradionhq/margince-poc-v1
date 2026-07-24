// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The standing IMAP connect's refusal ladder, no database in sight: signed
// out is 401, a signed-in human is granted the connector's read scope
// (no egress to the tenant-supplied host), a malformed body is 422, and the
// probe's own refusals map to their statuses. Everything here returns
// before the registry, so the branches are provable as pure transport.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// imapConnectBody is the typed wire fixture for the connect request — the
// fields stay compile-time aligned with the contract's imap block.
type imapConnectBody struct {
	Imap *imapCredsBody `json:"imap,omitempty"`
}

type imapCredsBody struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// postIMAPConnect routes the request through the real generated mux (a stub
// registry keeps this DB-free) rather than calling the handler directly —
// calling past the mux is exactly what let the shadowed-route defect survive
// review, so every refusal case here proves reachability too.
func postIMAPConnect(ctx context.Context, t *testing.T, h connectorHandlers, body imapConnectBody) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/imap/connect", bytes.NewReader(payload))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	srv := Server{connectorHandlers: h}
	mux := crmcontracts.HandlerFromMuxWithBaseURL(srv, chi.NewRouter(), "/v1")
	mux.ServeHTTP(rec, req)
	return rec
}

func imapConnectCtx(t *testing.T, scopes ...principal.Scope) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type:   principal.PrincipalHuman,
		UserID: ids.NewV7(),
		Scopes: principal.NewScopeSet(scopes...),
	})
}

func TestStandingIMAPConnectRefusals(t *testing.T) {
	probeCalls := 0
	h := connectorHandlers{
		// A nil-pool registry: the refusal branches under test all return
		// before any persistence, so wired() passes without a database.
		registry: NewCaptureRegistry(nil, nil),
		imapAuthenticate: func(context.Context, connector.AuthRequest) (connector.Auth, error) {
			probeCalls++
			return nil, imap.ErrLoginRejected
		},
	}
	creds := imapConnectBody{Imap: &imapCredsBody{
		Host: "mail.example", Port: 993, Username: "a@b.example", Secret: "s",
	}}

	t.Run("signed out is 401", func(t *testing.T) {
		if rec := postIMAPConnect(context.Background(), t, h, creds); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("a signed-in human with no passport scopes is granted read, not refused", func(t *testing.T) {
		// A cookie session carries RBAC but no passport scopes; the handler
		// grants the connector's read scope from the human's authority (as the
		// OAuth callback does), so a real human is NOT scope-refused. An empty
		// body reaches the credential check (422) — proving the request passed
		// the scope gate, and without any dial.
		rec := postIMAPConnect(imapConnectCtx(t /* no scopes, like a real session */), t, h, imapConnectBody{})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422 (passed the scope gate, missing creds)", rec.Code)
		}
		if probeCalls != 0 {
			t.Fatal("no credential block was sent, yet the probe ran")
		}
	})

	// A realistic signed-in human session carries no passport scopes.
	authed := imapConnectCtx(t)

	t.Run("a missing credential block is 422", func(t *testing.T) {
		if rec := postIMAPConnect(authed, t, h, imapConnectBody{}); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("a rejected login is 422", func(t *testing.T) {
		if rec := postIMAPConnect(authed, t, h, creds); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		if probeCalls != 1 {
			t.Fatalf("probe ran %d times, want exactly once", probeCalls)
		}
	})

	t.Run("an unreachable server is 502", func(t *testing.T) {
		h.imapAuthenticate = func(context.Context, connector.AuthRequest) (connector.Auth, error) {
			return nil, imap.ErrUnreachable
		}
		if rec := postIMAPConnect(authed, t, h, creds); rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
	})
}

func TestStandingIMAPConnectFailureMapping(t *testing.T) {
	// A realistic signed-in human session carries no passport scopes.
	authed := imapConnectCtx(t)
	creds := imapConnectBody{Imap: &imapCredsBody{
		Host: "mail.example", Port: 993, Username: "a@b.example", Secret: "s",
	}}

	t.Run("an unclassified probe error is an opaque 500", func(t *testing.T) {
		h := connectorHandlers{
			registry: NewCaptureRegistry(nil, nil),
			imapAuthenticate: func(context.Context, connector.AuthRequest) (connector.Auth, error) {
				return nil, errors.New("something provider-shaped and internal")
			},
		}
		rec := postIMAPConnect(authed, t, h, creds)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "provider-shaped") {
			t.Fatal("the provider's raw error must never reach the client")
		}
	})

	t.Run("a persist failure is 500, never a silent success", func(t *testing.T) {
		// A vault-less registry cannot seal the credential — the probe
		// succeeded but the store must refuse loudly.
		h := connectorHandlers{
			registry: NewCaptureRegistry(nil, nil),
			imapAuthenticate: func(_ context.Context, req connector.AuthRequest) (connector.Auth, error) {
				return connector.Auth(req.Payload), nil
			},
		}
		if rec := postIMAPConnect(authed, t, h, creds); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (unsealable credential)", rec.Code)
		}
	})
}

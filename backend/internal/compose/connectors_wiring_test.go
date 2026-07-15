// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// fakeVault is a non-nil keyvault.Vault for wiring tests; WithGmailCapture only
// checks it's present, never calls it.
type fakeVault struct{}

func (fakeVault) Put(context.Context, ids.WorkspaceID, []byte) (keyvault.Ref, error) {
	return "", nil
}

func (fakeVault) Get(context.Context, ids.WorkspaceID, keyvault.Ref) ([]byte, error) {
	return nil, nil
}
func (fakeVault) Delete(context.Context, ids.WorkspaceID, keyvault.Ref) error { return nil }
func (fakeVault) Health(context.Context) error                                { return nil }

// recordingOAuth notes whether the token exchange ran, so a CSRF test can tell
// "blocked before the exchange" from "passed the gate and reached it".
type recordingOAuth struct{ exchanged bool }

func (o *recordingOAuth) AuthCodeURL(state, _ string) string { return "https://auth?state=" + state }

func (o *recordingOAuth) Exchange(context.Context, string, string) (string, error) {
	o.exchanged = true
	return "refresh", nil
}

func (o *recordingOAuth) AccessToken(context.Context, string) (string, error) { return "access", nil }

type stubGmailAPI struct{}

func (stubGmailAPI) Profile(context.Context, string) (string, string, error) {
	return "owner@example.com", "1", nil
}
func (stubGmailAPI) ListRecent(context.Context, string, int) ([]string, error) { return nil, nil }
func (stubGmailAPI) History(context.Context, string, string) ([]string, string, error) {
	return nil, "1", nil
}
func (stubGmailAPI) GetRaw(context.Context, string, string) ([]byte, error) { return nil, nil }

// The account-linking-CSRF defence: the callback must have the oauth_csrf
// cookie matching the nonce in the signed state before it exchanges the code.
func TestCallbackRequiresMatchingCSRFCookie(t *testing.T) {
	signer := newStateSigner([]byte("0123456789abcdef0123456789abcdef"))
	oauth := &recordingOAuth{}
	h := connectorHandlers{
		registry:      capture.NewRegistry(nil, nil, nil, nil),
		oauth:         oauth,
		gmailAPI:      stubGmailAPI{},
		signer:        signer,
		publicBaseURL: "https://app.test",
		apiBaseURL:    "https://api.test",
	}
	const nonce = "csrf-nonce-value"
	state := signer.sign(connectState{
		Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"),
		User:      ids.MustParse("22222222-2222-2222-2222-222222222222"),
		Provider:  "gmail",
		Nonce:     nonce,
	}, time.Now().Add(time.Hour))
	code := "the-code"
	params := crmcontracts.ConnectorOAuthCallbackParams{Code: &code, State: state}

	// (a) No cookie → blocked before any token exchange.
	rec := httptest.NewRecorder()
	h.ConnectorOAuthCallback(rec, httptest.NewRequest(http.MethodGet, "/cb", nil), "gmail", params)
	if oauth.exchanged {
		t.Fatal("token exchange ran without a matching oauth_csrf cookie (CSRF gate bypassed)")
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/activation?connect=error" {
		t.Errorf("no-cookie Location = %q, want the error landing", loc)
	}

	// (b) Matching cookie → passes the gate and reaches the exchange.
	oauth.exchanged = false
	req := httptest.NewRequest(http.MethodGet, "/cb", nil)
	req.AddCookie(&http.Cookie{Name: oauthCSRFCookie, Value: nonce, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	h.ConnectorOAuthCallback(httptest.NewRecorder(), req, "gmail", params)
	if !oauth.exchanged {
		t.Fatal("a matching oauth_csrf cookie should let the flow reach the token exchange")
	}
}

func TestGmailConfigGating(t *testing.T) {
	full := GmailConfig{ClientID: "id", ClientSecret: "sec", StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: "https://app"}
	cases := []struct {
		name             string
		cfg              GmailConfig
		canSync, connect bool
	}{
		{"full", full, true, true},
		{"sync only (no state/url)", GmailConfig{ClientID: "id", ClientSecret: "sec"}, true, false},
		{"missing secret", GmailConfig{ClientID: "id"}, false, false},
		{"empty", GmailConfig{}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.cfg.canSync() != c.canSync {
				t.Errorf("canSync = %v, want %v", c.cfg.canSync(), c.canSync)
			}
			if c.cfg.canConnect() != c.connect {
				t.Errorf("canConnect = %v, want %v", c.cfg.canConnect(), c.connect)
			}
		})
	}
}

func TestGmailPollRegistryNilWhenUnconfigured(t *testing.T) {
	if reg := GmailPollRegistry(nil, nil, GmailConfig{}); reg != nil {
		t.Errorf("unconfigured GmailPollRegistry = %v, want nil (poll skipped)", reg)
	}
	if reg := GmailPollRegistry(nil, nil, GmailConfig{ClientID: "id", ClientSecret: "sec"}); reg == nil {
		t.Error("configured GmailPollRegistry returned nil, want a registry with gmail registered")
	}
}

func TestWithGmailCaptureWiresOrSkips(t *testing.T) {
	full := GmailConfig{ClientID: "id", ClientSecret: "sec", StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: "https://app"}

	// Fully configured + a vault → the connector transport is wired.
	var s Server
	s.vault = fakeVault{}
	WithGmailCapture(full)(&s, nil)
	if !s.wired() {
		t.Error("WithGmailCapture(full) with a vault did not wire the connector handlers")
	}

	// Fully configured but NO vault → no-op (can't seal the refresh token).
	var s2 Server
	WithGmailCapture(full)(&s2, nil)
	if s2.wired() {
		t.Error("WithGmailCapture without a vault should be a no-op")
	}

	// Missing the state key → no-op, surface stays its 501.
	var s3 Server
	s3.vault = fakeVault{}
	WithGmailCapture(GmailConfig{ClientID: "id", ClientSecret: "sec"})(&s3, nil)
	if s3.wired() {
		t.Error("WithGmailCapture without a state key/base URL should be a no-op")
	}
}

func TestConnectionStatusAndContractMapping(t *testing.T) {
	if got := connectionStatus("active"); got != crmcontracts.Connected {
		t.Errorf("active → %q, want connected", got)
	}
	if got := connectionStatus("revoked"); got != crmcontracts.Disconnected {
		t.Errorf("revoked → %q, want disconnected", got)
	}
	if got := connectionStatus("error"); got != crmcontracts.Error {
		t.Errorf("error → %q, want error", got)
	}

	id := ids.MustParse("11111111-1111-1111-1111-111111111111")
	c := toContractConnection(capture.ConnectionView{
		ID: id, Connector: "gmail", Status: "active",
		Cursor: []byte(`{"history_id":"7"}`), Scopes: []string{"read"},
	})
	if c.Provider != "gmail" || c.Status != crmcontracts.Connected || c.SyncCursor == nil || *c.SyncCursor != `{"history_id":"7"}` {
		t.Errorf("mapping wrong: %+v", c)
	}
	// A row with no scopes maps to an empty slice, never null.
	if c2 := toContractConnection(capture.ConnectionView{Connector: "gmail", Status: "active"}); c2.Scopes == nil {
		t.Error("nil scopes should map to an empty slice")
	}
}

func TestListAndDisconnectNotImplementedWhenUnwired(t *testing.T) {
	var h connectorHandlers // no registry
	for _, tc := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"list", h.ListConnectors},
		{"disconnect", func(w http.ResponseWriter, r *http.Request) { h.DisconnectConnector(w, r, "gmail") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, httptest.NewRequest(http.MethodGet, "/v1/connectors", nil))
			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s unwired = %d, want 501", tc.name, rec.Code)
			}
		})
	}
}

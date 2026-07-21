// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture/graph"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// graphWiredHandlers is wiredHandlers (the Gmail app) plus the Microsoft app,
// the both-providers deployment the dispatch has to keep apart.
func graphWiredHandlers() connectorHandlers {
	h := wiredHandlers()
	h.graphOAuth = graph.NewOAuth(graph.OAuthConfig{ClientID: "ms-cid", ClientSecret: "ms-sec", Scopes: []string{"offline_access", "Mail.Read"}})
	h.graphAPI = graph.NewAPI(nil, "")
	return h
}

func TestConnectGraphReturnsMicrosoftAuthorizeURL(t *testing.T) {
	h := graphWiredHandlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/graph/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "graph")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var resp crmcontracts.ConnectConnectorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AuthorizeUrl == nil {
		t.Fatal("authorize_url missing")
	}
	u, err := url.Parse(*resp.AuthorizeUrl)
	if err != nil {
		t.Fatalf("authorize_url not a URL: %v", err)
	}
	if u.Host != "login.microsoftonline.com" {
		t.Errorf("authorize host = %q, want login.microsoftonline.com", u.Host)
	}
	// The redirect_uri points back at OUR graph callback, and the state binds
	// the provider so the gmail callback can't complete a graph flow.
	if got := u.Query().Get("redirect_uri"); got != "https://api.test/v1/connectors/graph/callback" {
		t.Errorf("redirect_uri = %q, want the graph callback", got)
	}
	st, err := h.signer.verify(u.Query().Get("state"), time.Now())
	if err != nil {
		t.Fatalf("authorize_url state does not verify: %v", err)
	}
	if st.Provider != "graph" || st.User != ids.MustParse("22222222-2222-2222-2222-222222222222") {
		t.Errorf("state = %+v, want graph + the acting user", st)
	}
}

func TestConnectGraphNotImplementedWhenOnlyGmailWired(t *testing.T) {
	h := wiredHandlers() // gmail app only
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/graph/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "graph")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when the Microsoft app is not wired", rec.Code)
	}
}

func TestGcalConnectsAlongsideGraph(t *testing.T) {
	// gcal rides the same Google OAuth app as gmail; wiring the Microsoft
	// (graph) app alongside must not disturb it — the calendar flow still
	// returns its signed authorize URL.
	h := graphWiredHandlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/gcal/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "gcal")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for gcal (body %s)", rec.Code, rec.Body)
	}
}

func TestGraphCallbackRejectsGmailState(t *testing.T) {
	// A state signed for gmail must not complete the graph flow: the provider
	// is bound into the signed tuple.
	h := graphWiredHandlers()
	state := h.signer.sign(connectState{
		Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"),
		User:      ids.MustParse("22222222-2222-2222-2222-222222222222"),
		Provider:  "gmail",
		Nonce:     "n",
	}, time.Now().Add(time.Minute))
	code := "the-code"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/graph/callback", nil)
	req.AddCookie(&http.Cookie{Name: oauthCSRFCookie, Value: "n", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})

	h.ConnectorOAuthCallback(rec, req, "graph", crmcontracts.ConnectorOAuthCallbackParams{State: state, Code: &code})

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/#/onboarding/connect/error" {
		t.Errorf("Location = %q, want the error landing (provider mismatch)", loc)
	}
}

func TestGraphCallbackNotImplementedWhenUnwired(t *testing.T) {
	h := wiredHandlers() // gmail only — the graph surface keeps its 501
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/graph/callback", nil)

	h.ConnectorOAuthCallback(rec, req, "graph", crmcontracts.ConnectorOAuthCallbackParams{})

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestGraphConfigGating(t *testing.T) {
	full := GraphConfig{ClientID: "id", ClientSecret: "sec", StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: "https://app"}
	cases := []struct {
		name             string
		cfg              GraphConfig
		canSync, connect bool
	}{
		{"full", full, true, true},
		{"sync only (no state/url)", GraphConfig{ClientID: "id", ClientSecret: "sec"}, true, false},
		{"missing secret", GraphConfig{ClientID: "id"}, false, false},
		{"empty", GraphConfig{}, false, false},
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

func TestWithGraphCaptureWiresSharesOrSkips(t *testing.T) {
	gmailFull := GmailConfig{ClientID: "id", ClientSecret: "sec", StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: "https://app"}
	graphFull := GraphConfig{ClientID: "ms-id", ClientSecret: "ms-sec", StateKey: "0123456789abcdef0123456789abcdef", PublicBaseURL: "https://app"}

	// Both apps configured → one shared registry carrying both connectors.
	var s Server
	s.vault = fakeVault{}
	WithGmailCapture(gmailFull)(&s, nil)
	WithGraphCapture(graphFull)(&s, nil)
	if s.graphOAuth == nil {
		t.Fatal("WithGraphCapture(full) with a vault did not wire the graph OAuth app")
	}
	names := registeredNames(s.connectorHandlers.registry.Connectors())
	if !names["gmail"] || !names["graph"] {
		t.Errorf("shared registry connectors = %v, want gmail AND graph registered", names)
	}

	// Graph-only deployment → WithGraphCapture builds the registry itself.
	var s2 Server
	s2.vault = fakeVault{}
	WithGraphCapture(graphFull)(&s2, nil)
	if s2.connectorHandlers.registry == nil || s2.graphOAuth == nil {
		t.Fatal("graph-only WithGraphCapture should build the connect registry")
	}
	if s2.publicBaseURL != "https://app" {
		t.Errorf("graph-only wiring publicBaseURL = %q, want the configured one", s2.publicBaseURL)
	}

	// No vault → no-op (can't seal the refresh token).
	var s3 Server
	WithGraphCapture(graphFull)(&s3, nil)
	if s3.graphOAuth != nil {
		t.Error("WithGraphCapture without a vault should be a no-op")
	}

	// Missing the state key → no-op, surface stays its 501.
	var s4 Server
	s4.vault = fakeVault{}
	WithGraphCapture(GraphConfig{ClientID: "ms-id", ClientSecret: "ms-sec"})(&s4, nil)
	if s4.graphOAuth != nil {
		t.Error("WithGraphCapture without a state key/base URL should be a no-op")
	}
}

func TestCaptureSyncRegistryRegistersConfiguredProviders(t *testing.T) {
	both := CaptureSyncRegistry(nil, nil,
		GmailConfig{ClientID: "id", ClientSecret: "sec"},
		GraphConfig{ClientID: "ms-id", ClientSecret: "ms-sec"})
	names := registeredNames(both.Connectors())
	if !names["imap"] || !names["gmail"] || !names["graph"] {
		t.Errorf("connectors = %v, want imap+gmail+graph", names)
	}

	bare := CaptureSyncRegistry(nil, nil, GmailConfig{}, GraphConfig{})
	names = registeredNames(bare.Connectors())
	if !names["imap"] || names["gmail"] || names["graph"] {
		t.Errorf("connectors = %v, want imap only when neither app is configured", names)
	}
}

func registeredNames(descs []connector.Descriptor) map[string]bool {
	names := make(map[string]bool, len(descs))
	for _, d := range descs {
		names[d.Name] = true
	}
	return names
}

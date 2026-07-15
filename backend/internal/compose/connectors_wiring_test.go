// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestGmailConfigGating(t *testing.T) {
	full := GmailConfig{ClientID: "id", ClientSecret: "sec", StateKey: "k", PublicBaseURL: "https://app"}
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
	// Fully configured → the connector transport is wired.
	var s Server
	WithGmailCapture(GmailConfig{ClientID: "id", ClientSecret: "sec", StateKey: "k", PublicBaseURL: "https://app"})(&s, nil)
	if !s.wired() {
		t.Error("WithGmailCapture(full) did not wire the connector handlers")
	}

	// Missing the state key → no-op, surface stays its 501.
	var s2 Server
	WithGmailCapture(GmailConfig{ClientID: "id", ClientSecret: "sec"})(&s2, nil)
	if s2.wired() {
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

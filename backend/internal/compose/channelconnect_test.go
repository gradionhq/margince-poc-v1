// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The wiring assertion the channel surface needs and no store test can make:
// that the GENERATED router actually routes /v1/channel-connections into
// capture's handlers. Server's ServerInterface assertion (server.go) already
// makes a missing handler a compile error, but it cannot tell a handler from a
// same-signature placeholder — driving the real route table can.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// TestChannelConnectionRoutesReachCaptureHandlers drives the generated route
// table with a Server whose channel store is unwired. The handlers answer that
// state with their own 503; a 501 would mean the route never reached them.
func TestChannelConnectionRoutesReachCaptureHandlers(t *testing.T) {
	// No middleware: this test is about the generated route table, and the
	// admission/idempotency layers need a database that has nothing to say
	// about routing.
	api := crmcontracts.HandlerWithOptions(Server{}, crmcontracts.ChiServerOptions{BaseURL: "/v1"})

	const someID = "0193c9a0-0000-7000-8000-000000000001"
	for _, tc := range []struct{ name, method, path, body string }{
		{"list", http.MethodGet, "/v1/channel-connections", ""},
		{"connect", http.MethodPost, "/v1/channel-connections", `{"provider":"telegram","botToken":"1:x"}`},
		{"replace token", http.MethodPatch, "/v1/channel-connections/" + someID, `{"botToken":"1:x"}`},
		{"disconnect", http.MethodDelete, "/v1/channel-connections/" + someID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))

			if rec.Code == http.StatusNotImplemented {
				t.Fatalf("%s %s answered 501 — the operation is declared in the contract but no handler is registered for it", tc.method, tc.path)
			}
			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s %s answered 404 — the route is not in the generated table", tc.method, tc.path)
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503 from the unwired channel handlers (body: %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "channel_connections_not_configured") {
				t.Errorf("the 503 did not come from the channel handlers: %s", rec.Body.String())
			}
		})
	}
}

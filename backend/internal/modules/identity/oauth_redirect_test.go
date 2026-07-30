// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import "testing"

func TestRedirectURIMatchingIsPortAgnosticForLoopbackOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		registered, presented string
		want                  bool
	}{
		// Claude Code declares http://localhost/callback and connects from an
		// ephemeral port; RFC 8252 §7.3 requires the port be ignored.
		"loopback, ephemeral port":     {"http://localhost/callback", "http://localhost:3118/callback", true},
		"loopback, ip literal":         {"http://127.0.0.1/callback", "http://127.0.0.1:51872/callback", true},
		"loopback, registered w/ port": {"http://127.0.0.1:8080/callback", "http://127.0.0.1:9999/callback", true},
		"loopback, different path":     {"http://localhost/callback", "http://localhost:3118/other", false},
		"loopback, different scheme":   {"http://localhost/callback", "https://localhost/callback", false},
		// A public host must still match exactly — port-agnostic matching
		// there would widen the redirect surface.
		"public host, port differs":   {"https://claude.ai/api/mcp/auth_callback", "https://claude.ai:8443/api/mcp/auth_callback", false},
		"public host, exact":          {"https://claude.ai/api/mcp/auth_callback", "https://claude.ai/api/mcp/auth_callback", true},
		"public host, different host": {"https://claude.ai/cb", "https://evil.example/cb", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := redirectURIMatches(tc.registered, tc.presented); got != tc.want {
				t.Fatalf("redirectURIMatches(%q, %q) = %v, want %v", tc.registered, tc.presented, got, tc.want)
			}
		})
	}
}

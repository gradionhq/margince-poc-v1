// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestGuardedClientRefusesPrivateAddress pins the SSRF guarantee: the
// production delivery client must refuse to dial a private/loopback
// target, so a tenant-supplied webhook URL cannot become a probe of the
// deployment's own network. This is why the delivery integration tests
// inject a loopback-permitting client instead — netguard blocks the
// 127.0.0.1 an httptest receiver listens on, by design.
func TestGuardedClientRefusesPrivateAddress(t *testing.T) {
	client := NewGuardedClient()
	for _, target := range []string{
		"https://127.0.0.1/hook",
		"https://169.254.169.254/latest/meta-data", // cloud metadata endpoint
		"https://10.0.0.5/hook",
	} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("building request for %s: %v", target, err)
		}
		resp, err := client.Do(req)
		if err == nil {
			//craft:ignore swallowed-errors test cleanup of an unexpected success response; the assertion below is the real check
			_ = resp.Body.Close()
			t.Errorf("delivery client dialed %s — the SSRF guard did not refuse a private address", target)
			continue
		}
		if !strings.Contains(err.Error(), "non-public") {
			t.Errorf("dial of %s failed with %q, want a netguard non-public refusal", target, err)
		}
	}
}

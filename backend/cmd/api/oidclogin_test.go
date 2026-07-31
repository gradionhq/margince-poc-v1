// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// The federated flow's browser binding is a host-scoped cookie: the SPA starts
// the flow with a relative navigation (so the cookie lands on the app's host)
// and the provider returns to the callback on the api's host. When those hosts
// differ, every sign-in dies as `expired` — and `make dev` cannot catch it,
// because there both bases are localhost and only the ports differ. So the
// boot refuses it.
func TestFederatedSignInRefusesToBootAcrossHosts(t *testing.T) {
	deployCfg := oidcTestConfig(t)

	_, err := oidcLoginOptions(deployCfg, "https://app.example.com", "https://api.example.com", io.Discard)
	if err == nil {
		t.Fatal("a split-host deployment booted; every sign-in would have failed as expired")
	}
	// The operator has to be able to act on it, so the message names both
	// hosts and the way out.
	for _, want := range []string{"app.example.com", "api.example.com", "host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Same host, different ports and schemes, is fine: cookies are scoped by
	// host alone, which is exactly why `make dev` (:8080 app, :18080 api)
	// works.
	if _, err := oidcLoginOptions(deployCfg, "http://localhost:8080", "http://localhost:18080", io.Discard); err != nil {
		t.Fatalf("same-host different-port deployment refused: %v", err)
	}
	// And the api base defaulting to the app's own base is the single-origin
	// production shape.
	if _, err := oidcLoginOptions(deployCfg, "https://crm.example.com", "", io.Discard); err != nil {
		t.Fatalf("single-origin deployment refused: %v", err)
	}
}

func TestFederatedSignInNeedsAPublicBaseURL(t *testing.T) {
	// The redirect target is derived from configuration, never from a request
	// Host, so without a base there is nothing to register at the provider.
	if _, err := oidcLoginOptions(oidcTestConfig(t), "", "", io.Discard); err == nil {
		t.Fatal("auth.oidc booted with no --public-base-url")
	}
}

// TestPasswordSwitchIsWiredWhateverTheOIDCPosture pins that the password
// posture reaches the surface even when no provider is configured — the
// deployment's answer is carried by an option, not by a validator that ran at
// boot and was then forgotten.
func TestPasswordSwitchIsWiredWhateverTheOIDCPosture(t *testing.T) {
	opts, err := oidcLoginOptions(deployconfig.Config{Version: 1}, "", "", io.Discard)
	if err != nil {
		t.Fatalf("oidcLoginOptions: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("options = %d, want exactly the password-login option", len(opts))
	}
}

// oidcTestConfig is a complete, valid `auth.oidc` block over a real secret
// file — the secret is a file reference by rule (OPS-CFG-3), so the test
// writes one rather than pretending otherwise.
func oidcTestConfig(t *testing.T) deployconfig.Config {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "google-client-secret")
	if err := os.WriteFile(secret, []byte("the-client-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := deployconfig.Parse([]byte("version: 1\nauth:\n  oidc:\n" +
		"    enabled: true\n    issuer: https://accounts.google.com\n" +
		"    client_id: margince.apps.googleusercontent.com\n" +
		"    client_secret_file: " + secret + "\n"))
	if err != nil {
		t.Fatalf("parsing the fixture configuration: %v", err)
	}
	return cfg
}

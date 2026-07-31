// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The boot flags this process refuses to guess at. A configuration value that
// the code would reject later has to be rejected HERE, while an operator is
// still watching a terminal, rather than on the first request that needs it.

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
)

const testDSN = "postgres://localhost/margince_test"

// The access-token TTL: unset keeps the mint's own default, a flag or its env
// equivalent sets it, and the flag wins over the environment (the usual
// precedence — an explicit argument beats an inherited one).
func TestOAuthAccessTokenTTLIsReadFromTheFlagAndTheEnvironment(t *testing.T) {
	t.Run("unset is zero, which means the passport default", func(t *testing.T) {
		cfg, err := parseAPIFlags([]string{"--dsn", testDSN})
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if cfg.oauthAccessTokenTTL != 0 {
			t.Errorf("oauthAccessTokenTTL = %s, want 0 (unconfigured)", cfg.oauthAccessTokenTTL)
		}
	})

	t.Run("the flag sets it", func(t *testing.T) {
		cfg, err := parseAPIFlags([]string{"--dsn", testDSN, "--oauth-access-token-ttl", "15m"})
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if cfg.oauthAccessTokenTTL != 15*time.Minute {
			t.Errorf("oauthAccessTokenTTL = %s, want 15m", cfg.oauthAccessTokenTTL)
		}
	})

	t.Run("the environment sets it", func(t *testing.T) {
		t.Setenv("MARGINCE_OAUTH_ACCESS_TOKEN_TTL", "30m")
		cfg, err := parseAPIFlags([]string{"--dsn", testDSN})
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if cfg.oauthAccessTokenTTL != 30*time.Minute {
			t.Errorf("oauthAccessTokenTTL = %s, want the env value 30m", cfg.oauthAccessTokenTTL)
		}
	})

	t.Run("the flag beats the environment", func(t *testing.T) {
		t.Setenv("MARGINCE_OAUTH_ACCESS_TOKEN_TTL", "30m")
		cfg, err := parseAPIFlags([]string{"--dsn", testDSN, "--oauth-access-token-ttl", "15m"})
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if cfg.oauthAccessTokenTTL != 15*time.Minute {
			t.Errorf("oauthAccessTokenTTL = %s, want the flag's 15m", cfg.oauthAccessTokenTTL)
		}
	})
}

// A TTL the passport mint would refuse, or a value that is not a duration at
// all, must fail the boot — the alternative is a handshake failing in
// production with nobody watching.
func TestAnUnusableOAuthAccessTokenTTLFailsTheBoot(t *testing.T) {
	t.Run("past the mint's ceiling", func(t *testing.T) {
		over := (identity.MaxOAuthAccessTokenTTL + time.Hour).String()
		if _, err := parseAPIFlags([]string{"--dsn", testDSN, "--oauth-access-token-ttl", over}); err == nil {
			t.Fatalf("a TTL of %s was accepted, want a boot error naming the ceiling", over)
		}
	})

	t.Run("negative", func(t *testing.T) {
		if _, err := parseAPIFlags([]string{"--dsn", testDSN, "--oauth-access-token-ttl", "-1m"}); err == nil {
			t.Fatal("a negative TTL was accepted, want a boot error")
		}
	})

	t.Run("an env value that is not a duration", func(t *testing.T) {
		t.Setenv("MARGINCE_OAUTH_ACCESS_TOKEN_TTL", "fifteen minutes")
		if _, err := parseAPIFlags([]string{"--dsn", testDSN}); err == nil {
			t.Fatal("a malformed env duration was ignored, want a boot error rather than a silent default")
		}
	})
}

// The connector publishes --public-base-url verbatim as its OAuth audience, its
// RFC 9728 protected-resource document and its advertised MCP URL. A value that
// is wrong in any of those ways must fail while an operator is watching, not
// boot a connector advertising somewhere nobody can reach.
func TestValidatePublicBaseURLRefusesAnythingButABareOrigin(t *testing.T) {
	for _, ok := range []string{
		"https://crm.example.com",
		"http://localhost:8080",
		// A trailing slash means the same origin, and the /mcp derivation trims
		// it, so it is accepted rather than nitpicked.
		"https://crm.example.com/",
	} {
		if err := validatePublicBaseURL(ok); err != nil {
			t.Errorf("validatePublicBaseURL(%q) = %v, want nil", ok, err)
		}
	}

	for _, bad := range []struct{ name, raw string }{
		{"no scheme", "crm.example.com"},
		{"a scheme nothing dereferences", "ftp://crm.example.com"},
		{"no host", "https://"},
		// The MCP resource is this value + "/mcp", so a path here publishes
		// something like https://host/base/mcp while the route is at /mcp.
		{"a path", "https://crm.example.com/base"},
		{"a query", "https://crm.example.com?x=1"},
		{"a fragment", "https://crm.example.com#f"},
		// ":8080" is a non-empty authority that names no host — Host keeps the
		// port, so only Hostname() catches it.
		{"a hostless authority", "http://:8080"},
		// url.Parse decodes as it goes, so both of these arrive as a path that
		// trims to empty while the RAW value is what gets published.
		{"a repeated separator", "https://crm.example.com//"},
		{"an encoded separator", "https://crm.example.com/%2F"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			err := validatePublicBaseURL(bad.raw)
			if err == nil {
				t.Fatalf("validatePublicBaseURL(%q) = nil, want a refusal", bad.raw)
			}
			// The operator has to be able to fix it from the message alone.
			if !strings.Contains(err.Error(), bad.raw) {
				t.Errorf("refusal %q does not quote the offending value", err)
			}
		})
	}
}

// An origin carrying userinfo is refused WITHOUT quoting it. Every other
// refusal quotes the value so an operator can fix it, but this one would copy a
// password into the boot error and every log line that carries it.
func TestValidatePublicBaseURLRefusesUserinfoWithoutEchoingIt(t *testing.T) {
	const secret = "s3cr3t-password"
	err := validatePublicBaseURL("https://admin:" + secret + "@crm.example.com")
	if err == nil {
		t.Fatal("an origin with userinfo was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the refusal leaked the credential: %q", err)
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Errorf("refusal %q does not say what is wrong", err)
	}
}

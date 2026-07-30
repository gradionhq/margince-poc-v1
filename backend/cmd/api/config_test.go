// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The boot flags this process refuses to guess at. A configuration value that
// the code would reject later has to be rejected HERE, while an operator is
// still watching a terminal, rather than on the first request that needs it.

import (
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

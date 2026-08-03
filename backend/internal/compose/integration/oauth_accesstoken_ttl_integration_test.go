// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The operator's access-token lifetime (--oauth-access-token-ttl). A connector's
// access token is a passport, and a passport defaults to 30 days where connector
// norms are minutes plus refresh — this is the knob that closes that gap without
// a code change, and it has to reach BOTH mints of a connection's life, because
// a 15-minute token that every rotation re-issues for 30 days is not a
// 15-minute token.

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// accessTokenLifetime reads the RFC 6749 §5.1 expires_in a token response
// carries — the number a client actually schedules its renewal from.
func accessTokenLifetime(t *testing.T, body map[string]any) time.Duration {
	t.Helper()
	seconds, ok := body["expires_in"].(float64)
	if !ok {
		t.Fatalf("token response carries no expires_in: %v", body)
	}
	return time.Duration(seconds) * time.Second
}

// A configured TTL shortens the code exchange's passport AND every rotation's.
func TestAConfiguredAccessTokenTTLShortensBothMintsOfAConnection(t *testing.T) {
	const configured = 15 * time.Minute
	o := setupOAuthWith(t, compose.WithOAuthAccessTokenTTL(configured))

	code := o.authorize(t, url.Values{"scope": {"read write offline_access"}})
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("code exchange → %d %v", status, body)
	}
	// A lower bound too: a TTL the mint silently dropped would read as 30 days,
	// and one it mangled to zero would read as an already-expired token.
	if got := accessTokenLifetime(t, body); got > configured || got < configured-time.Minute {
		t.Fatalf("the exchanged access token lives %s, want the configured %s", got, configured)
	}
	refresh, _ := body["refresh_token"].(string)
	if refresh == "" {
		t.Fatalf("offline_access exchange returned no refresh_token: %v", body)
	}

	status, renewed := o.renew(t, refresh, nil)
	if status != http.StatusOK {
		t.Fatalf("renewal → %d %v", status, renewed)
	}
	if got := accessTokenLifetime(t, renewed); got > configured || got < configured-time.Minute {
		t.Fatalf("the rotated access token lives %s, want the configured %s: a rotation must not restore the default", got, configured)
	}
}

// Unset is the posture every deployment ran before the flag existed: the
// handshake keeps minting the 30-day passport, so adding the knob changes
// nothing until an operator sets it.
func TestAnUnconfiguredAccessTokenTTLKeepsThePassportDefault(t *testing.T) {
	o := setupOAuth(t)

	code := o.authorize(t, url.Values{"scope": {"read write offline_access"}})
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("code exchange → %d %v", status, body)
	}
	if got := accessTokenLifetime(t, body); got < 29*24*time.Hour {
		t.Fatalf("the exchanged access token lives %s, want the unchanged 30-day default", got)
	}
}

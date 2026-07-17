// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAud = "https://api.test/hooks/gmail/push"
	testSA  = "gmail-push@margince.iam.gserviceaccount.com"
	testKID = "test-key-1"
)

// oidcTestRig serves a JWKS for one RSA key and mints signed tokens against
// it. certsHits counts how many times the /certs endpoint was actually hit,
// for asserting refresh-throttling behavior.
type oidcTestRig struct {
	key       *rsa.PrivateKey
	srv       *httptest.Server
	certsHits atomic.Int32
}

func newOIDCTestRig(t *testing.T) *oidcTestRig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rig := &oidcTestRig{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/certs", func(w http.ResponseWriter, _ *http.Request) {
		rig.certsHits.Add(1)
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{
				{"kid": testKID, "kty": "RSA", "alg": "RS256", "use": "sig", "n": n, "e": e},
			},
		})
	})
	rig.srv = httptest.NewServer(mux)
	t.Cleanup(rig.srv.Close)
	return rig
}

func (r *oidcTestRig) jwksURL() string { return r.srv.URL + "/certs" }

func (r *oidcTestRig) certsHitCount() int32 { return r.certsHits.Load() }

// mint builds a signed JWT. Pass kid="" to sign without a kid header; alg
// overrides RS256; claims are merged over a valid default.
func (r *oidcTestRig) mint(t *testing.T, kid, alg string, claims map[string]any) string {
	t.Helper()
	if alg == "" {
		alg = "RS256"
	}
	header := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	base := map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            testAud,
		"email":          testSA,
		"email_verified": true,
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Add(-time.Minute).Unix(),
	}
	for k, v := range claims {
		base[k] = v
	}
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := seg(header) + "." + seg(base)
	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, r.key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestVerifier(rig *oidcTestRig) *googleOIDCVerifier {
	return newGoogleOIDCVerifier(rig.jwksURL(), testAud, testSA).withHTTPClient(rig.srv.Client())
}

func TestOIDCVerifyAcceptsValidToken(t *testing.T) {
	rig := newOIDCTestRig(t)
	tok := rig.mint(t, testKID, "RS256", nil)
	if err := newTestVerifier(rig).Verify(context.Background(), tok); err != nil {
		t.Fatalf("Verify(valid) = %v, want nil", err)
	}
}

func TestOIDCVerifyRejects(t *testing.T) {
	rig := newOIDCTestRig(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	cases := []struct {
		name string
		tok  func() string
	}{
		{"empty", func() string { return "" }},
		{"not-three-segments", func() string { return "a.b" }},
		{"alg-none", func() string { return rig.mint(t, testKID, "none", nil) }},
		{"unknown-kid", func() string { return rig.mint(t, "nope", "RS256", nil) }},
		{"wrong-aud", func() string { return rig.mint(t, testKID, "RS256", map[string]any{"aud": "https://evil.test"}) }},
		{"wrong-email", func() string { return rig.mint(t, testKID, "RS256", map[string]any{"email": "attacker@evil.test"}) }},
		{"email-unverified", func() string { return rig.mint(t, testKID, "RS256", map[string]any{"email_verified": false}) }},
		{"wrong-iss", func() string { return rig.mint(t, testKID, "RS256", map[string]any{"iss": "https://evil.test"}) }},
		{"expired", func() string {
			return rig.mint(t, testKID, "RS256", map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
		}},
		{"future-iat", func() string {
			return rig.mint(t, testKID, "RS256", map[string]any{"iat": time.Now().Add(time.Hour).Unix()})
		}},
		{"bad-signature", func() string {
			// A token signed by a DIFFERENT key but advertising the served kid.
			evil := &oidcTestRig{key: other, srv: rig.srv}
			return evil.mint(t, testKID, "RS256", nil)
		}},
	}
	v := newTestVerifier(rig)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Verify(context.Background(), tc.tok()); err == nil {
				t.Fatalf("Verify(%s) = nil, want an error", tc.name)
			}
		})
	}
}

// TestOIDCVerifyHonorsInjectedClock exercises the withClock test seam: the
// same token accepted "now" is rejected once the injected clock is moved
// past exp+skew, and rejected again when moved before iat-skew.
func TestOIDCVerifyHonorsInjectedClock(t *testing.T) {
	rig := newOIDCTestRig(t)
	iat := time.Now()
	exp := iat.Add(time.Hour)
	tok := rig.mint(t, testKID, "RS256", map[string]any{
		"iat": iat.Unix(),
		"exp": exp.Unix(),
	})

	atIssue := newTestVerifier(rig).withClock(func() time.Time { return iat })
	if err := atIssue.Verify(context.Background(), tok); err != nil {
		t.Fatalf("Verify(at issue) = %v, want nil", err)
	}

	longAfterExpiry := newTestVerifier(rig).withClock(func() time.Time { return exp.Add(time.Hour) })
	if err := longAfterExpiry.Verify(context.Background(), tok); err == nil {
		t.Fatal("Verify(long after exp) = nil, want an error")
	}

	longBeforeIssue := newTestVerifier(rig).withClock(func() time.Time { return iat.Add(-time.Hour) })
	if err := longBeforeIssue.Verify(context.Background(), tok); err == nil {
		t.Fatal("Verify(long before iat) = nil, want an error")
	}
}

// TestOIDCVerifyThrottlesJWKSRefresh proves the cross-call refresh bound: an
// unauthenticated caller can force a cache miss on every request just by
// sending a never-seen kid (no valid signature required to reach the key
// lookup), so refreshes must be throttled regardless of how many distinct
// kids arrive within the cooldown.
func TestOIDCVerifyThrottlesJWKSRefresh(t *testing.T) {
	rig := newOIDCTestRig(t)
	now := time.Now()
	v := newTestVerifier(rig).withClock(func() time.Time { return now })

	tok1 := rig.mint(t, "unknown-kid-1", "RS256", nil)
	tok2 := rig.mint(t, "unknown-kid-2", "RS256", nil)

	if err := v.Verify(context.Background(), tok1); err == nil {
		t.Fatal("Verify(unknown kid 1) = nil, want an error")
	}
	if err := v.Verify(context.Background(), tok2); err == nil {
		t.Fatal("Verify(unknown kid 2) = nil, want an error")
	}
	if got := rig.certsHitCount(); got != 1 {
		t.Fatalf("certs hits after two distinct unknown kids within cooldown = %d, want 1", got)
	}

	// Advance the injected clock past the cooldown: the next unknown-kid
	// attempt must trigger a second fetch.
	now = now.Add(jwksRefreshCooldown)
	tok3 := rig.mint(t, "unknown-kid-3", "RS256", nil)
	if err := v.Verify(context.Background(), tok3); err == nil {
		t.Fatal("Verify(unknown kid 3) = nil, want an error")
	}
	if got := rig.certsHitCount(); got != 2 {
		t.Fatalf("certs hits after cooldown elapsed = %d, want 2", got)
	}

	// The accept path for a real, cached kid still works after all this.
	valid := rig.mint(t, testKID, "RS256", nil)
	if err := v.Verify(context.Background(), valid); err != nil {
		t.Fatalf("Verify(valid) = %v, want nil", err)
	}
}

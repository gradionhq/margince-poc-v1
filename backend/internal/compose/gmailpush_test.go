// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The push handler's authentication layering, no database in sight: a wrong
// shared token is 403 before anything else; with a push identity configured,
// a missing or forged OIDC bearer is 401; a valid bearer clears the gate and
// the request proceeds into body handling (asserted via 400 on a garbage
// body — past authentication, before any DB touch).

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const pushTestToken = "push-secret" // test fixture, not a credential

func newOIDCPushHandler(rig *oidcTestRig) *gmailPushHandler {
	return &gmailPushHandler{
		token:    pushTestToken,
		verifier: newTestVerifier(rig),
		log:      slog.New(slog.DiscardHandler),
	}
}

func postPush(h *gmailPushHandler, token, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gmail-push?token="+token, strings.NewReader("not json"))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGmailPushOIDCGate(t *testing.T) {
	rig := newOIDCTestRig(t)
	h := newOIDCPushHandler(rig)

	t.Run("wrong shared token is 403 before the OIDC check", func(t *testing.T) {
		if rec := postPush(h, "wrong", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("missing bearer is 401", func(t *testing.T) {
		if rec := postPush(h, pushTestToken, ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("token signed for another audience is 401", func(t *testing.T) {
		forged := rig.mint(t, testKID, "", map[string]any{"aud": "https://elsewhere.example/hook"})
		if rec := postPush(h, pushTestToken, forged); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
	t.Run("a valid bearer clears the gate", func(t *testing.T) {
		// Past authentication the garbage body answers 400 — proof the OIDC
		// check passed without needing a database behind the handler.
		if rec := postPush(h, pushTestToken, rig.mint(t, testKID, "", nil)); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (authenticated, bad body)", rec.Code)
		}
	})
	t.Run("no push identity keeps the token-only contract", func(t *testing.T) {
		tokenOnly := &gmailPushHandler{token: pushTestToken, log: slog.New(slog.DiscardHandler)}
		if rec := postPush(tokenOnly, pushTestToken, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (no OIDC demanded)", rec.Code)
		}
	})
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":  "abc",
		"Bearer  abc": "abc",
		"bearer abc":  "",
		"abc":         "",
		"":            "",
	}
	for header, want := range cases {
		if got := bearerToken(header); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The push handler's authentication layering, no database in sight: a wrong
// shared token is 401 (the chassis's uniform admission failure, design
// §6.5 — Pub/Sub simply re-mints and retries, exactly as it already does
// for a missing OIDC bearer); with a push identity configured, a missing or
// forged OIDC bearer is also 401; a valid bearer clears the gate and the
// request proceeds into body handling, asserted via the chassis's 2xx
// Poison response on a garbage body — a malformed body is not something
// Pub/Sub should ever retry, so a 4xx here would make it redeliver a
// poison payload forever.

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const pushTestToken = "push-secret" // test fixture, not a credential

func newOIDCPushHandler(rig *oidcTestRig) http.Handler {
	spec := gmailPushSpec(nil, nil, pushTestToken, newTestVerifier(rig), slog.New(slog.DiscardHandler))
	return Webhook(spec, slog.New(slog.DiscardHandler))
}

func postPush(h http.Handler, token, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gmail?token="+token, strings.NewReader("not json"))
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

	t.Run("wrong shared token is 401, same as any other chassis admission failure", func(t *testing.T) {
		if rec := postPush(h, "wrong", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
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
	t.Run("a valid bearer clears the gate; the malformed body that follows is poison, not retried", func(t *testing.T) {
		// Past authentication, the garbage body must NOT invite Pub/Sub to
		// redeliver — the same bytes would fail identically forever, so the
		// chassis answers 2xx (Poison) instead of a 4xx.
		rec := postPush(h, pushTestToken, rig.mint(t, testKID, "", nil))
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("status = %d, want 2xx (authenticated, poison body)", rec.Code)
		}
	})
	t.Run("no push identity keeps the token-only contract; a poison body still isn't retried", func(t *testing.T) {
		tokenOnly := Webhook(gmailPushSpec(nil, nil, pushTestToken, nil, slog.New(slog.DiscardHandler)), slog.New(slog.DiscardHandler))
		rec := postPush(tokenOnly, pushTestToken, "")
		if rec.Code < 200 || rec.Code >= 300 {
			t.Fatalf("status = %d, want 2xx (no OIDC demanded, poison body)", rec.Code)
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

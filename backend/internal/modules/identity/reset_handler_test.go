// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The reset handlers' pre-database refusals: an unwired mailer answers the
// explicit 501 (and capabilities say so), throttles fire before any work,
// and malformed input is the caller's fault — all provable with a zero
// Service, no database round-trip.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type nopMailer struct{}

func (nopMailer) Send(_ context.Context, _, _, _ string) error { return nil }

func post(h http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestOnlyTheEmailedHalfOfRecoveryNeedsAMailer(t *testing.T) {
	h := NewHandlers(&Service{})
	// Asking for a token by email needs a mailer: without one there is
	// genuinely nothing to send.
	if rec := post(h.RequestPasswordReset, "/v1/auth/forgot-password", `{"email":"a@b.test"}`); rec.Code != http.StatusNotImplemented {
		t.Fatalf("forgot-password without mailer = %d, want 501", rec.Code)
	}
	// Redeeming a token you already hold needs only the token. This endpoint
	// once 501'd here too, which made an admin-issued link unredeemable on the
	// very installations it exists for — and would strand an already-delivered
	// link if an operator changed the mail settings inside its seven-day life.
	// An unknown token is refused on its merits (401), never on configuration.
	// Asserting the exact status, not merely "anything but 501": a 500 or an
	// accidental success would both satisfy a not-501 check while meaning the
	// endpoint is broken. An unknown token earns the same neutral 401 it earns
	// on a mailer-wired installation.
	if rec := post(h.ResetPassword, "/v1/auth/reset-password", `{"token":"x","new_password":"twelve chars!"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reset-password with an unknown token and no mailer = %d, want 401 — redemption must not depend on a delivery channel", rec.Code)
	}
}

func TestCapabilitiesReflectTheWiredMailer(t *testing.T) {
	h := NewHandlers(&Service{})
	rec := httptest.NewRecorder()
	h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"password_reset":false`) {
		t.Fatalf("unwired capabilities = %s, want password_reset:false", rec.Body)
	}

	h = h.WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")
	rec = httptest.NewRecorder()
	h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"password_reset":true`) {
		t.Fatalf("wired capabilities = %s, want password_reset:true", rec.Body)
	}
}

func TestResetRequestRefusesMalformedInput(t *testing.T) {
	h := NewHandlers(&Service{}).WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")
	if rec := post(h.RequestPasswordReset, "/v1/auth/forgot-password", `{`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body = %d, want 422", rec.Code)
	}
	if rec := post(h.RequestPasswordReset, "/v1/auth/forgot-password", `{"email":"not-an-email"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid email = %d, want 422", rec.Code)
	}
}

func TestResetRedeemRefusesMalformedInput(t *testing.T) {
	h := NewHandlers(&Service{}).WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")
	if rec := post(h.ResetPassword, "/v1/auth/reset-password", `{"token":"","new_password":"twelve chars!"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty token = %d, want 422", rec.Code)
	}
	if rec := post(h.ResetPassword, "/v1/auth/reset-password", `{"token":"x","new_password":"short"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short password = %d, want 422", rec.Code)
	}
}

func TestResetThrottlesFireBeforeAnyWork(t *testing.T) {
	h := NewHandlers(&Service{}).WithPasswordReset(nopMailer{}).WithPasswordLinkBase("https://crm.example.test")
	// Drain the per-IP window (30/hour); the zero Service proves no
	// database is touched on the refused path.
	var last int
	for range 40 {
		rec := post(h.ResetPassword, "/v1/auth/reset-password", `{"token":"x","new_password":"short"}`)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("41st attempt = %d, want 429", last)
	}
	if rec := post(h.RequestPasswordReset, "/v1/auth/forgot-password", `{"email":"a@b.test"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("forgot-password over the shared IP window = %d, want 429", rec.Code)
	}
}

func TestResetRequestWithoutWorkspaceIsANeutralNoOp(t *testing.T) {
	// Pre-bootstrap there is no account to reset: the service answers the
	// same empty mint an unknown address gets, never an error.
	raw, err := (&Service{}).CreatePasswordReset(context.Background(), "a@b.test")
	if err != nil || raw != "" {
		t.Fatalf("pre-bootstrap mint = (%q, %v), want the neutral no-op", raw, err)
	}
}

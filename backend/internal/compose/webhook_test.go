// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The chassis's admission and response discipline, provider-agnostic: a
// fixed test secret stands in for either provider's real one, and each test
// asserts one rule from design §6.5 — the method guard, the no-detail
// secret rejection, and the two Disposition outcomes that give a poison
// payload and a transient fault opposite HTTP treatment on purpose.

package compose

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const webhookTestSecret = "chassis-secret" // test fixture, not a credential

// webhookTestSpec wires a fixed secret and MaxBody so each test only needs
// to supply the Handle behaviour it is asserting.
func webhookTestSpec(t *testing.T, handle func(ctx context.Context, r *http.Request, body []byte) (Disposition, error)) WebhookSpec {
	t.Helper()
	return WebhookSpec{
		Provider: "test",
		MaxBody:  1 << 10,
		Secret: func(r *http.Request) (string, string) {
			return webhookTestSecret, r.URL.Query().Get("secret")
		},
		Handle:   handle,
		OnAccept: http.StatusOK,
	}
}

func webhookTestHandler(t *testing.T, handle func(ctx context.Context, r *http.Request, body []byte) (Disposition, error)) http.Handler {
	t.Helper()
	return Webhook(webhookTestSpec(t, handle), slog.New(slog.DiscardHandler))
}

func TestWebhookRejectsNonPostWith405(t *testing.T) {
	h := webhookTestHandler(t, func(context.Context, *http.Request, []byte) (Disposition, error) {
		t.Fatal("Handle must not run for a non-POST request")
		return Accepted, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/webhooks/test?secret="+webhookTestSecret, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestWebhookRejectsAWrongSecretWithoutABody(t *testing.T) {
	h := webhookTestHandler(t, func(context.Context, *http.Request, []byte) (Disposition, error) {
		t.Fatal("Handle must not run once the secret comparison fails")
		return Accepted, nil
	})

	// A wrong secret must answer identically regardless of the request
	// body's length — nothing here narrows it down to a body-inspection
	// bug rather than the secret check itself.
	for name, requestBody := range map[string]string{
		"empty body": "",
		"short body": "x",
		"large body": strings.Repeat("a", 900),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/webhooks/test?secret=wrong", strings.NewReader(requestBody))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("body = %q, want empty — a wrong secret must name no connection ids", rec.Body.String())
			}
		})
	}
}

func TestWebhookReturnsSuccessForAPoisonPayload(t *testing.T) {
	h := webhookTestHandler(t, func(context.Context, *http.Request, []byte) (Disposition, error) {
		return Poison, errors.New("malformed payload")
	})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/test?secret="+webhookTestSecret, strings.NewReader("garbage"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// A poison payload must NOT invite redelivery: the provider gets the
	// same 2xx it would for success, because the same bytes would fail
	// identically every time.
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("status = %d, want 2xx", rec.Code)
	}
}

func TestWebhookReturns500ForATransientFault(t *testing.T) {
	h := webhookTestHandler(t, func(context.Context, *http.Request, []byte) (Disposition, error) {
		return Transient, errors.New("database unreachable")
	})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/test?secret="+webhookTestSecret, strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// A transient fault MUST invite redelivery: only 500 tells the
	// provider to retry the same delivery later.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

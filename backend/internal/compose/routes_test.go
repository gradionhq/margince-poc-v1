// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequireMetricsTokenUnconfiguredAnswers404 pins the fail-closed
// default: an operator who never set --metrics-token gets a 404, the same
// answer an unrelated unmounted route would give, not a 401 that confirms a
// protected endpoint exists at this path.
func TestRequireMetricsTokenUnconfiguredAnswers404(t *testing.T) {
	called := false
	h := requireMetricsToken("", func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if called {
		t.Fatal("the wrapped handler ran with no token configured")
	}
}

// TestRequireMetricsTokenRejectsMissingOrWrongBearer covers both ways an
// unauthenticated or mistaken caller can reach a configured endpoint.
func TestRequireMetricsTokenRejectsMissingOrWrongBearer(t *testing.T) {
	cases := []struct {
		name string
		auth string
	}{
		{"no header", ""},
		{"wrong token", "Bearer not-the-secret"},
		{"missing Bearer prefix", "s3cr3t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			h := requireMetricsToken("s3cr3t", func(http.ResponseWriter, *http.Request) { called = true })

			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if called {
				t.Fatal("the wrapped handler ran without a matching bearer token")
			}
		})
	}
}

// TestRequireMetricsTokenAcceptsMatchingBearer is the positive control: the
// gate must not refuse the credential it was configured to accept.
func TestRequireMetricsTokenAcceptsMatchingBearer(t *testing.T) {
	called := false
	h := requireMetricsToken("s3cr3t", func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !called {
		t.Fatal("the wrapped handler did not run with a matching bearer token")
	}
}

// TestRequireMetricsTokenAcceptsCaseInsensitiveScheme pins RFC 7235 §2.1:
// the auth-scheme token is case-insensitive, so "bearer"/"BEARER" must be
// accepted exactly like "Bearer" — only the credential itself is case-
// sensitive.
func TestRequireMetricsTokenAcceptsCaseInsensitiveScheme(t *testing.T) {
	for _, scheme := range []string{"bearer", "BEARER", "BeArEr"} {
		t.Run(scheme, func(t *testing.T) {
			called := false
			h := requireMetricsToken("s3cr3t", func(http.ResponseWriter, *http.Request) { called = true })

			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.Header.Set("Authorization", scheme+" s3cr3t")
			rec := httptest.NewRecorder()
			h(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if !called {
				t.Fatalf("the wrapped handler did not run for scheme %q", scheme)
			}
		})
	}
}

// TestWithMetricsTokenSetsServerField pins the Option's one job: it must
// not silently miswire onto the wrong field or require a pool.
func TestWithMetricsTokenSetsServerField(t *testing.T) {
	var s Server
	WithMetricsToken("s3cr3t")(&s, nil)
	if s.metricsToken != "s3cr3t" {
		t.Fatalf("metricsToken = %q, want %q", s.metricsToken, "s3cr3t")
	}
}

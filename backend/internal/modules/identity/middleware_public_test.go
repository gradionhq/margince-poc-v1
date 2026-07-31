// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssistantProfileIsExplicitlyPublic(t *testing.T) {
	if !isPublicRequest(httptest.NewRequest(http.MethodGet, "/v1/assistant/profile", nil)) {
		t.Fatal("assistant profile must pass the session gate for the login presence")
	}
	if isPublicRequest(httptest.NewRequest(http.MethodPost, "/v1/assistant/profile", nil)) {
		t.Fatal("assistant profile must expose GET anonymously, not every method on its path")
	}
}

// The federated sign-in exemption widens the anonymous surface, so its edges
// are pinned rather than assumed: exactly the two GET navigations under one
// provider segment, and nothing deeper, adjacent, or on another method.
func TestFederatedSignInExemptionIsExactlyTheTwoNavigations(t *testing.T) {
	public := map[string]string{
		http.MethodGet + " /v1/auth/oidc/google/start":    "the human clicks this before any session exists",
		http.MethodGet + " /v1/auth/oidc/google/callback": "the provider redirects here with no session cookie",
		// An unconfigured key still reaches the handler, which answers 404.
		// Refusing it HERE instead would 401 and disclose the difference.
		http.MethodGet + " /v1/auth/oidc/microsoft/start": "an unconfigured provider is the handler's 404, not the gate's 401",
	}
	for request, why := range public {
		method, path, _ := strings.Cut(request, " ")
		if !isPublicRequest(httptest.NewRequest(method, path, nil)) {
			t.Errorf("%s must be session-less: %s", request, why)
		}
	}

	gated := map[string]string{
		http.MethodPost + " /v1/auth/oidc/google/start":            "the flow is a navigation; a mutation must not inherit the exemption",
		http.MethodGet + " /v1/auth/oidc/google/start/extra":       "a deeper path must not inherit the exemption",
		http.MethodGet + " /v1/auth/oidc/google/admin/callback":    "a nested segment is not one provider segment",
		http.MethodGet + " /v1/auth/oidc//start":                   "an empty provider segment addresses no provider",
		http.MethodGet + " /v1/auth/oidc/google/token":             "only start and callback are session-less",
		http.MethodGet + " /v1/auth/oidc/google":                   "a provider alone is not an operation",
		http.MethodGet + " /v1/auth/oidcx/google/start":            "the prefix must match exactly",
		http.MethodGet + " /v1/auth/oidc/google/start/../../../me": "path traversal must not reach a gated route",
	}
	for request, why := range gated {
		method, path, _ := strings.Cut(request, " ")
		if isPublicRequest(httptest.NewRequest(method, path, nil)) {
			t.Errorf("%s must require a session: %s", request, why)
		}
	}
}

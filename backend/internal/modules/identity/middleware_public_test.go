// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"net/http"
	"net/http/httptest"
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

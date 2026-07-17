// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Account recovery (A74/ADR-0056, UI-gated by the A107 capabilities
// probe): the forgot/reset password pair. Explicit 501 until the flow is
// wired end to end — the capabilities response reports password_reset
// false for exactly as long as these stay stubs, so the login UI never
// renders a link this surface cannot honor.

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// RequestPasswordReset implements (POST /auth/forgot-password).
func (h Handlers) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	httperr.NotImplemented(w, r, "RequestPasswordReset")
}

// ResetPassword implements (POST /auth/reset-password).
func (h Handlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	httperr.NotImplemented(w, r, "ResetPassword")
}

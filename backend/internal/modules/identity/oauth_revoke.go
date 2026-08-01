// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// RFC 7009 revocation: a client handing back a credential and ending the
// connection from its own side, rather than waiting for the human to reach
// for the Settings screen.

import (
	"errors"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// oauthRevoke serves POST /oauth/revoke. No session is expected — a client
// revoking on shutdown, or because a human clicked "disconnect" inside the
// client rather than in Settings, has none — so the workspace binds the same
// way register and token do (host/subdomain).
func (h Handlers) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	err := h.svc.revokeToken(r.Context(), revokeTokenInput{
		Token:         token,
		TokenTypeHint: r.PostForm.Get("token_type_hint"),
	})
	// RFC 7009 §2.2: the server answers 200 whether a token was actually
	// revoked or was never valid to begin with — anything else would turn
	// this endpoint into an oracle for whether a token string is real.
	// database.ErrNoWorkspace (a host that names no installation) is that
	// same non-disclosure case, not a caller mistake; only a genuine store
	// failure reaches the client as one.
	if err != nil && !errors.Is(err, database.ErrNoWorkspace) {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

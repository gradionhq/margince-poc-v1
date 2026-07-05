// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The token endpoint: the authorization-code + PKCE exchange that ends
// the A2 handshake. The token minted here IS an Agent Seat Passport —
// there is no separate OAuth token store to drift out of sync with
// passport revocation.

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

var (
	errCodeSpent        = errors.New("oauth: code spent")
	errGrantMismatch    = errors.New("oauth: grant mismatch")
	errAudienceMismatch = errors.New("oauth: audience mismatch")
)

func (h Handlers) oauthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code")
		return
	}
	code := r.PostForm.Get("code")
	verifier := r.PostForm.Get("code_verifier")
	if code == "" || verifier == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}

	userID, workspaceID, scopes, err := h.redeemAuthCode(r, code, verifier)
	switch {
	case errors.Is(err, errCodeSpent):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code is unknown, expired, or already used")
		return
	case errors.Is(err, errGrantMismatch):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "the code, client, redirect_uri and verifier do not match the authorization")
		return
	case errors.Is(err, errAudienceMismatch):
		oauthError(w, http.StatusBadRequest, "invalid_target", "the token's audience does not match the authorization")
		return
	case err != nil:
		httperr.Write(w, r, err)
		return
	}

	label := "oauth:" + r.PostForm.Get("client_id")
	issued, err := h.svc.IssuePassport(principal.WithWorkspaceID(r.Context(), workspaceID),
		Identity{UserID: userID, WorkspaceID: workspaceID},
		IssuePassportInput{Label: &label, Scopes: scopes})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token": issued.Token,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(issued.ExpiresAt).Seconds()),
		"scope":        strings.Join(scopes, " "),
	})
}

// redeemAuthCode validates the exchange against the stored grant and
// consumes the single-use code in one transaction.
func (h Handlers) redeemAuthCode(r *http.Request, code, verifier string) (userID, workspaceID ids.UUID, scopes []string, err error) {
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		// Read first, validate, and only then consume: a stranger who
		// holds the code but not the verifier must not be able to BURN
		// it for the legitimate client (denial-of-flow). The final
		// conditional UPDATE keeps single-use airtight under races.
		var (
			challenge   string
			clientID    string
			redirectURI string
			resource    *string
		)
		err := tx.QueryRow(r.Context(), `
			SELECT user_id, workspace_id, scopes, code_challenge, client_id, redirect_uri, resource
			FROM oauth_authorization_code
			WHERE code_hash = $1 AND consumed_at IS NULL AND expires_at > now()`,
			hashOAuthCode(code)).
			Scan(&userID, &workspaceID, &scopes, &challenge, &clientID, &redirectURI, &resource)
		if errors.Is(err, pgx.ErrNoRows) {
			return errCodeSpent
		}
		if err != nil {
			return err
		}
		if r.PostForm.Get("client_id") != clientID || r.PostForm.Get("redirect_uri") != redirectURI {
			return errGrantMismatch
		}
		// RFC 8707: a code bound to a resource mints tokens for that
		// resource only.
		if resource != nil && r.PostForm.Get("resource") != *resource {
			return errAudienceMismatch
		}
		// PKCE S256: SHA-256(verifier), base64url unpadded, constant shape.
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			return errGrantMismatch
		}
		tag, err := tx.Exec(r.Context(), `
			UPDATE oauth_authorization_code SET consumed_at = now()
			WHERE code_hash = $1 AND consumed_at IS NULL`, hashOAuthCode(code))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errCodeSpent // a racing exchange got there first
		}
		return nil
	})
	return userID, workspaceID, scopes, err
}

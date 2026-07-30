// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The token endpoint: the authorization-code + PKCE exchange that ends
// the A2 handshake. The access token minted here IS an Agent Seat Passport
// — there is no separate OAuth token store to drift out of sync with
// passport revocation — and it hangs off the oauth_grant row that records
// the consent, so the connection as a whole stays revocable.

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		h.tokenFromAuthCode(w, r)
	case oauthRefreshToken:
		h.tokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only authorization_code and refresh_token")
	}
}

// tokenFromAuthCode ends the handshake: the code the human's consent produced
// becomes the first passport and, when the grant allows it, the first refresh
// token.
func (h Handlers) tokenFromAuthCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	verifier := r.PostForm.Get("code_verifier")
	if code == "" || verifier == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}

	issued, refresh, err := h.exchangeAuthCode(r, code, verifier)
	switch {
	// A code cannot exist in a workspace that doesn't resolve, and the
	// answer must not distinguish that from a spent code.
	case errors.Is(err, errCodeSpent), errors.Is(err, database.ErrNoWorkspace):
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

	writeTokenResponse(w, issued, refresh)
}

// tokenFromRefresh renews a connection: the presented token is spent and its
// successor issued, or the presentation is refused.
//
// EVERY refusal answers invalid_grant. Claude re-runs consent on that code
// and on no other, so a more precise code — invalid_request for a malformed
// presentation, invalid_scope for an over-wide one, a 403 for a revoked grant
// — would leave the connector retrying a token that will never work again,
// with no path back to a human.
func (h Handlers) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	issued, refresh, err := h.svc.rotateRefreshToken(r.Context(), refreshRequest{
		Token:             r.PostForm.Get(oauthRefreshToken),
		ClientID:          r.PostForm.Get(oauthParamClientID),
		Scopes:            strings.Fields(r.PostForm.Get("scope")),
		Resource:          r.PostForm.Get(oauthParamResource),
		CanonicalResource: h.mcpResource,
	})
	switch {
	case errors.Is(err, errRefreshScope):
		oauthError(w, http.StatusBadRequest, "invalid_grant",
			"the requested scope exceeds what this connection was granted")
		return
	// A refresh token cannot exist in a workspace that doesn't resolve, and
	// the answer must not distinguish that from an unknown token.
	case errors.Is(err, errRefreshRejected), errors.Is(err, database.ErrNoWorkspace):
		oauthError(w, http.StatusBadRequest, "invalid_grant",
			"the refresh token is unknown, expired, or already used")
		return
	case err != nil:
		httperr.Write(w, r, err)
		return
	}
	writeTokenResponse(w, issued, refresh)
}

// writeTokenResponse is the RFC 6749 §5.1 success body, spelled once: the
// code exchange and every later rotation must hand a client the same shape,
// or a connector that renews stops finding the fields it found at connect.
func writeTokenResponse(w http.ResponseWriter, issued IssuedPassport, refresh string) {
	response := map[string]any{
		"access_token": issued.Token,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(issued.ExpiresAt).Seconds()),
		"scope":        strings.Join(issued.Scopes, " "),
	}
	// A refresh token is answered only when the grant allows one: a client
	// that never asked for offline_access must not be handed a long-lived
	// credential it never consented to store.
	if refresh != "" {
		response[oauthRefreshToken] = refresh
		response["refresh_expires_in"] = int(refreshTokenTTL.Seconds())
	}
	httperr.WriteJSON(w, http.StatusOK, response)
}

// exchangeAuthCode turns a valid authorization code into the credentials
// that outlive it, in ONE transaction: the single-use code is validated
// and consumed, the grant recording what the human approved is written,
// the first refresh token is minted beneath it, and the passport the
// client will actually call with is stamped with that grant. A partial
// commit here would leave a client holding a refresh token for a grant
// that does not exist, or a live passport no revocation can reach.
//
// refresh is empty unless the authorization carried offline_access.
func (h Handlers) exchangeAuthCode(r *http.Request, code, verifier string) (issued IssuedPassport, refresh string, err error) {
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		redeemed, err := h.consumeAuthCode(r, tx, code, verifier)
		if err != nil {
			return err
		}

		// offline_access rode the code's scopes column (oauth.go's
		// oauthAuthorize) because that table has no marker column of its
		// own. Here it becomes what it always meant — refresh_allowed on
		// the grant — and leaves the scope list: it is session lifetime,
		// not authority over any record, and validScopes has no entry for
		// it, so a passport carrying it would be refused outright.
		passportScopes := redeemed.Scopes
		refreshAllowed := slices.Contains(redeemed.Scopes, scopeOfflineAccess)
		if refreshAllowed {
			passportScopes = slices.DeleteFunc(slices.Clone(redeemed.Scopes), func(sc string) bool {
				return sc == scopeOfflineAccess
			})
		}

		grantID, refreshToken, err := issueGrant(r.Context(), tx, issueGrantInput{
			WorkspaceID:    redeemed.WorkspaceID,
			UserID:         redeemed.UserID,
			ClientID:       redeemed.ClientID,
			Scopes:         passportScopes,
			RefreshAllowed: refreshAllowed,
			Resource:       redeemed.Resource,
		})
		if err != nil {
			return err
		}
		refresh = refreshToken

		// The label names the client the consent was for; the grant is what
		// actually binds the passport to it.
		label := oauthPassportLabel(redeemed.ClientID)
		issued, err = mintPassport(r.Context(), tx,
			Identity{UserID: redeemed.UserID, WorkspaceID: redeemed.WorkspaceID},
			IssuePassportInput{Label: &label, Scopes: passportScopes}, &grantID)
		return err
	})
	if err != nil {
		return IssuedPassport{}, "", err
	}
	return issued, refresh, nil
}

// redeemedCode is what a validated, spent authorization code carried into
// the rest of the exchange. Scopes are still as authorized — the
// offline_access marker included.
type redeemedCode struct {
	UserID      ids.UserID
	WorkspaceID ids.WorkspaceID
	Scopes      []string
	ClientID    string
	Resource    *string
}

// consumeAuthCode validates the exchange against the stored authorization
// and consumes the single-use code, inside the caller's transaction so the
// credentials issued against it commit with it.
func (h Handlers) consumeAuthCode(r *http.Request, tx pgx.Tx, code, verifier string) (redeemedCode, error) {
	// Read first, validate, and only then consume: a stranger who holds the
	// code but not the verifier must not be able to BURN it for the
	// legitimate client (denial-of-flow). The final conditional UPDATE keeps
	// single-use airtight under races.
	var (
		out         redeemedCode
		challenge   string
		redirectURI string
	)
	// The client is joined for its lifecycle alone: a code minted seconds
	// before an admin disabled the client must not still redeem into a grant,
	// a refresh chain and a passport under it. A dead client makes the row
	// vanish, so the answer is the same invalid_grant a spent code gets — the
	// endpoint stays silent about which of the two it was.
	err := tx.QueryRow(r.Context(), `
		SELECT a.user_id, a.workspace_id, a.scopes, a.code_challenge, a.client_id, a.redirect_uri, a.resource
		FROM oauth_authorization_code a
		JOIN oauth_client c ON (c.workspace_id, c.client_id) = (a.workspace_id, a.client_id)
		WHERE a.code_hash = $1 AND a.consumed_at IS NULL AND a.expires_at > now()
		  AND `+liveClientPredicate,
		hashOAuthCode(code)).
		Scan(&out.UserID, &out.WorkspaceID, &out.Scopes, &challenge, &out.ClientID, &redirectURI, &out.Resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return redeemedCode{}, errCodeSpent
	}
	if err != nil {
		return redeemedCode{}, err
	}
	if r.PostForm.Get(oauthParamClientID) != out.ClientID || !redirectURIMatches(redirectURI, r.PostForm.Get("redirect_uri")) {
		return redeemedCode{}, errGrantMismatch
	}
	if !audienceMatches(r.PostForm.Get(oauthParamResource), h.mcpResource, out.Resource) {
		return redeemedCode{}, errAudienceMismatch
	}
	// PKCE S256: SHA-256(verifier), base64url unpadded, constant shape.
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		return redeemedCode{}, errGrantMismatch
	}
	tag, err := tx.Exec(r.Context(), `
		UPDATE oauth_authorization_code SET consumed_at = now()
		WHERE code_hash = $1 AND consumed_at IS NULL`, hashOAuthCode(code))
	if err != nil {
		return redeemedCode{}, err
	}
	if tag.RowsAffected() == 0 {
		return redeemedCode{}, errCodeSpent // a racing exchange got there first
	}
	return out, nil
}

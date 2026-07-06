// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The A2 authorization server (B-EP06.18b, B-EP03.14/.15, ADR-0013):
// OAuth 2.1 shape — authorization-code + PKCE S256 ONLY, public clients
// via Dynamic Client Registration, RFC 8414/9728 metadata, RFC 8707
// audience binding. There is no third-party IdP in the agent path: the
// token minted at the end IS an Agent Seat Passport, so every later
// call re-authenticates against live passport + human state and
// revocation binds mid-session exactly like the A1 path.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// authorization codes are single-use couriers; five minutes is
// generous for a redirect round-trip.
const authCodeTTL = 5 * time.Minute

// OAuthRouter serves the authorization-server endpoints. Mounted
// behind the same workspace/session middleware as /v1: register and
// token are public (the workspace still binds via slug/subdomain);
// authorize demands the signed-in human whose authority the passport
// will borrow.
func (h Handlers) OAuthRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/register", h.oauthRegister)
	mux.HandleFunc("GET /oauth/authorize", h.oauthConsentForm)
	mux.HandleFunc("POST /oauth/authorize", h.oauthAuthorize)
	mux.HandleFunc("POST /oauth/token", h.oauthToken)
	return mux
}

type dcrRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (h Handlers) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var req dcrRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed registration document")
		return
	}
	// Public clients only: PKCE is the proof of possession. A client
	// asking for a secret-based method is asking to be privileged —
	// refused, and there is no column to store a secret in anyway.
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only public clients register here (token_endpoint_auth_method must be none)")
		return
	}
	if req.ClientName == "" || len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name and redirect_uris are required")
		return
	}
	for _, raw := range req.RedirectURIs {
		if !validRedirectURI(raw) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				fmt.Sprintf("%q: redirect uris must be https, or http on localhost", raw))
			return
		}
	}

	clientID, err := randomToken()
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3)`,
			clientID, req.ClientName, req.RedirectURIs)
		return err
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_name":                req.ClientName,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
	})
}

// consentCookie carries the double-submit nonce that binds the consent
// POST to the browser that saw the consent screen. SameSite=Strict
// means a cross-site attacker can neither read nor ride it.
const consentCookie = "crm_oauth_consent"

// authorizeRequest is the validated, not-yet-consented authorize call.
type authorizeRequest struct {
	ClientID      string
	ClientName    string
	RedirectURI   string
	Scopes        []string
	CodeChallenge string
	Resource      string
	State         string
}

// validateAuthorize checks everything about the request EXCEPT consent:
// response type, mandatory PKCE S256, scopes, known client, registered
// redirect. No code exists until the human approves.
func (h Handlers) validateAuthorize(r *http.Request, q url.Values) (authorizeRequest, string, string) {
	if q.Get("response_type") != "code" {
		return authorizeRequest{}, "unsupported_response_type", "only response_type=code"
	}
	// S256 is mandatory (OAuth 2.1): no challenge and the downgrade to
	// plain are both refused before any code exists.
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) < 43 {
		return authorizeRequest{}, "invalid_request", "PKCE S256 code_challenge is required"
	}
	scopes, err := parseOAuthScopes(q.Get("scope"))
	if err != nil {
		return authorizeRequest{}, "invalid_scope", err.Error()
	}
	req := authorizeRequest{
		ClientID:      q.Get("client_id"),
		RedirectURI:   q.Get("redirect_uri"),
		Scopes:        scopes,
		CodeChallenge: q.Get("code_challenge"),
		Resource:      q.Get("resource"),
		State:         q.Get("state"),
	}
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		var uris []string
		err := tx.QueryRow(r.Context(),
			`SELECT client_name, redirect_uris FROM oauth_client WHERE client_id = $1`,
			req.ClientID).Scan(&req.ClientName, &uris)
		if errors.Is(err, pgx.ErrNoRows) {
			return errUnknownClient
		}
		if err != nil {
			return err
		}
		if !slices.Contains(uris, req.RedirectURI) {
			return errRedirectMismatch
		}
		return nil
	})
	switch {
	case errors.Is(err, errUnknownClient):
		return authorizeRequest{}, "invalid_client", "unknown client_id"
	case errors.Is(err, errRedirectMismatch):
		// Never redirect to an unregistered URI — answer the caller.
		return authorizeRequest{}, "invalid_request", "redirect_uri is not registered for this client"
	case err != nil:
		return authorizeRequest{}, "server_error", "authorize lookup failed"
	}
	return req, "", ""
}

// oauthConsentForm (GET) shows the human WHAT is asking for WHICH
// authority and arms the consent nonce. It never mints a code: a GET
// riding an existing session must not be able to authorize anything —
// a DCR-registered client luring a signed-in admin onto this URL would
// otherwise silently borrow their authority (OAuth CSRF).
func (h Handlers) oauthConsentForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFrom(r.Context()); !ok {
		httperr.Unauthorized(w, r, "authorization requires the signed-in human whose authority the agent will borrow")
		return
	}
	req, oauthCode, detail := h.validateAuthorize(r, r.URL.Query())
	if oauthCode != "" {
		oauthError(w, http.StatusBadRequest, oauthCode, detail)
		return
	}
	nonce, err := randomToken()
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: consentCookie, Value: nonce, Path: "/oauth/authorize",
		MaxAge: 300, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})

	var page strings.Builder
	page.WriteString(`<!doctype html><meta charset="utf-8"><title>Authorize access</title>` +
		`<body style="font-family:system-ui;max-width:32rem;margin:4rem auto">` +
		`<h1>Authorize access</h1><p><strong>`)
	page.WriteString(template.HTMLEscapeString(req.ClientName))
	page.WriteString(`</strong> requests the scopes:</p><ul>`)
	for _, scope := range req.Scopes {
		page.WriteString("<li>" + template.HTMLEscapeString(scope) + "</li>")
	}
	page.WriteString(`</ul><form method="post" action="/oauth/authorize">`)
	for name, value := range map[string]string{
		"response_type": "code", "client_id": req.ClientID, "redirect_uri": req.RedirectURI,
		"scope": strings.Join(req.Scopes, " "), "code_challenge": req.CodeChallenge,
		"code_challenge_method": "S256", "resource": req.Resource, "state": req.State,
		"consent": nonce,
	} {
		page.WriteString(`<input type="hidden" name="` + template.HTMLEscapeString(name) +
			`" value="` + template.HTMLEscapeString(value) + `">`)
	}
	page.WriteString(`<button type="submit">Approve</button></form></body>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page.String()))
}

// oauthAuthorize (POST) is the consent decision: same-site by header,
// nonce-bound to the browser that saw the form, and only THEN a code.
func (h Handlers) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authorization requires the signed-in human whose authority the agent will borrow")
		return
	}
	// Modern browsers stamp the initiator; a cross-site POST is refused
	// outright (defense in depth over the nonce).
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		oauthError(w, http.StatusForbidden, "access_denied", "cross-site consent is refused")
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	nonce, err := r.Cookie(consentCookie)
	if err != nil || nonce.Value == "" ||
		subtle.ConstantTimeCompare([]byte(nonce.Value), []byte(r.PostForm.Get("consent"))) != 1 {
		oauthError(w, http.StatusForbidden, "access_denied", "consent token missing or stale — reload the authorization page")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: consentCookie, Value: "", Path: "/oauth/authorize", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})

	req, oauthCode, detail := h.validateAuthorize(r, url.Values(r.PostForm))
	if oauthCode != "" {
		oauthError(w, http.StatusBadRequest, oauthCode, detail)
		return
	}

	code, err := randomToken()
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO oauth_authorization_code
			  (workspace_id, code_hash, client_id, user_id, scopes, code_challenge, redirect_uri, resource, expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, NULLIF($7, ''), now() + $8::interval)`,
			hashOAuthCode(code), req.ClientID, id.UserID, req.Scopes, req.CodeChallenge,
			req.RedirectURI, req.Resource, authCodeTTL.String())
		return err
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	location, _ := url.Parse(req.RedirectURI)
	params := location.Query()
	params.Set("code", code)
	if req.State != "" {
		params.Set("state", req.State)
	}
	location.RawQuery = params.Encode()
	// Not an open redirect: the target was matched EXACTLY against the
	// client's registered redirect_uris above; an unregistered URI never
	// reaches this line.
	http.Redirect(w, r, location.String(), http.StatusFound) // #nosec G710
}

var (
	errUnknownClient    = errors.New("oauth: unknown client")
	errRedirectMismatch = errors.New("oauth: redirect mismatch")
)

// oauthError is the RFC 6749 §5.2 error shape.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	httperr.WriteJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func parseOAuthScopes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{"read"}, nil
	}
	scopes := strings.Fields(raw)
	for _, sc := range scopes {
		if !validScopes[principal.Scope(sc)] {
			return nil, fmt.Errorf("scope %q is not one of read|draft|write|send|enrich", sc)
		}
	}
	return scopes, nil
}

// validRedirectURI admits https anywhere and plain http only on
// loopback (native-app dev flows).
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	default:
		return false
	}
}

func hashOAuthCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("oauth: entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

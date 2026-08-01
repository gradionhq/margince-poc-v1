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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// authorization codes are single-use couriers; five minutes is
// generous for a redirect round-trip.
const authCodeTTL = 5 * time.Minute

// OAuthRouter serves the authorization-server endpoints. Mounted
// behind the same workspace/session middleware as /v1: register, token
// and revoke are public (the workspace still binds via slug/subdomain);
// the consent POST demands the signed-in human whose authority the
// passport will borrow, and the consent GET admits a session-less one
// for the sole purpose of sending them somewhere they can sign in.
func (h Handlers) OAuthRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/register", h.oauthRegister)
	mux.HandleFunc("GET /oauth/authorize", h.oauthConsentRedirect)
	mux.HandleFunc("POST /oauth/authorize", h.oauthAuthorize)
	mux.HandleFunc("POST /oauth/token", h.oauthToken)
	mux.HandleFunc("POST /oauth/revoke", h.oauthRevoke)
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
	if errors.Is(err, database.ErrNoWorkspace) {
		// Registration is per tenant; the request's host resolved to none.
		oauthError(w, http.StatusBadRequest, "invalid_request", "no workspace resolved for this request")
		return
	}
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
	Offline       bool
	CodeChallenge string
	Resource      string
	State         string
}

// validateAuthorize checks everything about the request EXCEPT consent:
// response type, mandatory PKCE S256, scopes, known client, registered
// redirect. No code exists until the human approves.
func (h Handlers) validateAuthorize(r *http.Request, q url.Values) (authorizeRequest, string, string) {
	if q.Get("response_type") != oauthResponseTypeCode {
		return authorizeRequest{}, "unsupported_response_type", "only response_type=code"
	}
	// S256 is mandatory (OAuth 2.1): no challenge and the downgrade to
	// plain are both refused before any code exists.
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) < 43 {
		return authorizeRequest{}, "invalid_request", "PKCE S256 code_challenge is required"
	}
	scopes, offline, err := parseOAuthScopes(q.Get("scope"))
	if err != nil {
		return authorizeRequest{}, "invalid_scope", err.Error()
	}
	req := authorizeRequest{
		ClientID:      q.Get(oauthParamClientID),
		RedirectURI:   q.Get("redirect_uri"),
		Scopes:        scopes,
		Offline:       offline,
		CodeChallenge: q.Get("code_challenge"),
		Resource:      q.Get(oauthParamResource),
		State:         q.Get("state"),
	}
	// RFC 8707: a present audience must name this installation's MCP
	// endpoint, checked before any code exists — a refused audience must
	// mint nothing. Absent resource stays accepted (older clients omit
	// it) and is stored NULL below. An unset h.mcpResource (no
	// --public-base-url configured) can never equal a present resource,
	// so this fails closed rather than treating "no canonical value" as
	// "matches everything" — unreachable through the mounted routes
	// today (the api refuses to boot the connector gate without
	// --public-base-url, and /oauth/* is only mounted when that gate is
	// on), but the comparison must hold on its own regardless of how it
	// is reached.
	if req.Resource != "" && req.Resource != h.mcpResource {
		return authorizeRequest{}, "invalid_target", "the requested resource is not this installation's MCP endpoint"
	}
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		var uris []string
		// A disabled or deleted client reads as UNKNOWN, deliberately: the same
		// answer an unregistered client_id gets, so the refusal tells an
		// attacker nothing about whether a client exists and has been switched
		// off.
		err := tx.QueryRow(r.Context(),
			`SELECT c.client_name, c.redirect_uris FROM oauth_client c
			  WHERE c.client_id = $1 AND `+liveClientPredicate,
			req.ClientID).Scan(&req.ClientName, &uris)
		if errors.Is(err, pgx.ErrNoRows) {
			return errUnknownClient
		}
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(uris, func(registered string) bool {
			return redirectURIMatches(registered, req.RedirectURI)
		}) {
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

// oauthConsentRedirect (GET) validates the request, arms the consent nonce
// and redirects the browser to the consent screen. It never mints a code: a
// GET riding an existing session must not be able to authorize anything
// — a DCR-registered client luring a signed-in admin onto this URL
// would otherwise silently borrow their authority (OAuth CSRF).
func (h Handlers) oauthConsentRedirect(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFrom(r.Context()); !ok {
		// A human who runs `claude mcp add` arrives here in a browser that may
		// carry no session at all, and the endpoint cannot ask for one: it serves
		// no HTML. The SPA can — AuthGate renders the login screen in place at
		// whatever route was asked for — so the answer is the screen, and the human
		// signs in without losing the request.
		//
		// It carries the request and NOTHING this endpoint has not yet done:
		// no nonce (there is no human to bind one to) and no validated value
		// (validateAuthorize has not run). Once signed in the screen re-enters this
		// endpoint, which then validates and arms as usual. This redirect cannot
		// loop: its target is the SPA document, a route this api does not serve, so
		// nothing behind it redirects again on its own.
		redirectToConsentScreen(w, r, consentScreenParams(r.URL.Query()))
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

	// The consent SCREEN lives in the SPA; this endpoint stays where discovery
	// advertises it and keeps doing the work only the server can: validate the
	// request and arm the consent nonce.
	redirectToConsentScreen(w, r, consentHandoffParams(req, nonce))
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
		// The ordinary cause is a human who left the screen open past the cookie's
		// five minutes, so the answer is the screen — where re-entry mints a fresh
		// nonce. A forged nonce lands here too and gets the same answer: it minted
		// nothing either way, and the screen it reaches serves the human whose
		// session the request already had to carry.
		refuseToConsentScreen(w, r, url.Values(r.PostForm), consentErrorStale)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: consentCookie, Value: "", Path: "/oauth/authorize", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})

	// A POST that fails validation is read by the human's browser, not by the
	// client, so the refusal goes to the screen and validateAuthorize's
	// client-facing code and description stay behind: the screen states the
	// refusal in the human's own language, and the specific code is a client
	// developer's vocabulary, delivered on the GET where a client developer looks.
	req, refusal, _ := h.validateAuthorize(r, url.Values(r.PostForm))
	if refusal != "" {
		refuseToConsentScreen(w, r, url.Values(r.PostForm), consentErrorInvalid)
		return
	}

	// Deny is answered to the CLIENT, not to the browser: RFC 6749 §4.1.2.1
	// says the client learns access_denied at its own redirect_uri with its
	// state echoed, so it stops waiting instead of hanging on a tab the human
	// closed. It is judged AFTER the nonce check — a forged deny is still a
	// forgery — and mints nothing: no grant, no code.
	if r.PostForm.Get("deny") != "" {
		redirectToClient(w, r, req, url.Values{oauthParamError: {"access_denied"}})
		return
	}

	// The human LENDS one of their own passports rather than granting scopes ad
	// hoc, so the code carries the INTERSECTION of that passport's authority and
	// the client's request — never wider than either one. resolveLend states why
	// that intersection is re-computed here instead of taken from the form.
	lent, lendable, err := h.svc.resolveLend(r.Context(), id, req.Scopes, r.PostForm.Get("passport_id"))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	if !lendable {
		// Nothing was minted, and the human is one selection away from a working
		// consent: back to the screen, which re-reads the live list and asks again.
		refuseToConsentScreen(w, r, url.Values(r.PostForm), consentErrorUnlendable)
		return
	}
	req.Scopes = lent.Scopes

	code, err := h.mintAuthorizationCode(r, req, id, lent.ID)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	redirectToClient(w, r, req, url.Values{"code": {code}})
}

// mintAuthorizationCode writes the single-use code the consent produced and
// returns the plaintext courier the client will redeem; only its hash is
// stored. The scopes it records are the ones the human actually lent — the
// intersection, never the client's request.
//
// The offline_access marker's durable home is oauth_grant.refresh_allowed, and
// no grant exists until the code is redeemed — so it rides in this
// unconstrained scopes column to survive the round trip instead of dying here.
// The exchange re-derives the boolean from it and strips it before any scope
// reaches the passport (oauth_token.go).
//
// The code row and the audit row naming the lend commit TOGETHER: which
// passport a human handed to a client is the central authority fact of this
// flow, and a code that existed without it would be a lend nobody could trace.
func (h Handlers) mintAuthorizationCode(
	r *http.Request, req authorizeRequest, id Identity, lentID ids.PassportID,
) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	storedScopes := req.Scopes
	if req.Offline {
		storedScopes = append(append([]string{}, req.Scopes...), scopeOfflineAccess)
	}
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		var codeID ids.UUID
		if err := tx.QueryRow(r.Context(), `
			INSERT INTO oauth_authorization_code
			  (workspace_id, code_hash, client_id, user_id, scopes, code_challenge, redirect_uri, resource, expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, NULLIF($7, ''), now() + $8::interval)
			RETURNING id`,
			hashOAuthCode(code), req.ClientID, id.UserID, storedScopes, req.CodeChallenge,
			req.RedirectURI, req.Resource, authCodeTTL.String()).Scan(&codeID); err != nil {
			return err
		}
		return auditLend(r.Context(), tx, codeID, req, lentID)
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// auditLend records WHICH of the human's passports was lent to this client,
// and the authority that went with it. Neither oauth_authorization_code nor
// oauth_grant has a column for the passport, so this row is the only place the
// question "which of my passports did I lend to this connection?" can be
// answered afterwards.
//
// The after image is the authority actually handed over — the intersected
// scopes, never the client's request — and refresh_allowed beside them, the
// same pair issueGrant records when the code is later redeemed, so the consent
// and its redemption read as one story. The actor is stamped by storekit from
// the authenticated principal; the session middleware bound it, so it can never
// come from the request body. Only the code's hash is ever stored, and the
// plaintext courier appears in no audit field.
//
// No outbox event rides with it. The events.md §5 catalog is closed and defines
// no oauth-consent verb — exactly as it defines none for oauth_grant, which is
// why issueGrant audits without emitting too (oauth_grant.go). The one type
// that would fit structurally, audit.appended, is declared in the contract as
// having no emit site and none planned for V1, so emitting it would need the
// contract changed first: raised upstream (P3) rather than filled here with a
// type that means something else.
func auditLend(
	ctx context.Context, tx pgx.Tx, codeID ids.UUID, req authorizeRequest, lentID ids.PassportID,
) error {
	_, err := storekit.Audit(ctx, tx, "create", "oauth_authorization_code", codeID, nil,
		map[string]any{
			auditFieldPassportID:     lentID,
			auditFieldClientID:       req.ClientID,
			auditFieldScopes:         req.Scopes,
			auditFieldRefreshAllowed: req.Offline,
		})
	return err
}

var (
	errUnknownClient    = errors.New("oauth: unknown client")
	errRedirectMismatch = errors.New("oauth: redirect mismatch")
)

// oauthError is the RFC 6749 §5.2 error shape.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	httperr.WriteJSON(w, status, map[string]string{oauthParamError: code, "error_description": description})
}

// scopeOfflineAccess is the scope Claude appends to ask for a refresh
// token (§5.2). It requests session lifetime, not access: parseOAuthScopes
// accepts it but never returns it as a passport scope — validScopes has
// no entry for it, so the passport mint would reject it as unknown if it
// ever got that far.
const scopeOfflineAccess = "offline_access"

// parseOAuthScopes splits and validates the space-delimited scope
// parameter. offline reports whether the caller asked for offline_access;
// the returned scopes never include it, so every downstream consumer that
// treats scopes as passport authority (the consent list, the passport
// mint) sees only the closed read|draft|write|send|enrich vocabulary.
func parseOAuthScopes(raw string) (scopes []string, offline bool, err error) {
	if strings.TrimSpace(raw) == "" {
		return []string{string(principal.ScopeRead)}, false, nil
	}
	for _, sc := range strings.Fields(raw) {
		if sc == scopeOfflineAccess {
			offline = true
			continue
		}
		if !validScopes[principal.Scope(sc)] {
			return nil, false, fmt.Errorf("scope %q is not one of read|draft|write|send|enrich", sc)
		}
		scopes = append(scopes, sc)
	}
	// A raw string that named no access scope at all — offline_access is
	// the only marker that can cause this, since anything else unknown
	// already errored above — carries no authority to deny outright: it is
	// the same "nothing asked for" situation as the blank-string case
	// above, not a client mistake. Defaulting on the empty OUTCOME (rather
	// than special-casing "offline_access" as the one literal request that
	// defaults) means no path ever mints a zero-scope passport that
	// silently fails every later tool call, whatever future marker-style
	// scope might someday reduce the parsed list to nothing.
	if len(scopes) == 0 {
		scopes = []string{string(principal.ScopeRead)}
	}
	return scopes, offline, nil
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

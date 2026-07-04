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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
	mux.HandleFunc("GET /oauth/authorize", h.oauthAuthorize)
	mux.HandleFunc("POST /oauth/token", h.oauthToken)
	return mux
}

// OAuthServerMetadata is the RFC 8414 discovery document. The issuer is
// the serving host — one issuer per workspace subdomain in production.
func OAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := requestIssuer(r)
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"registration_endpoint":                 issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"read", "draft", "write", "send", "enrich"},
	})
}

// ProtectedResourceMetadata is the RFC 9728 document: the resource
// names its authorization server so a generic MCP client can discover
// the handshake.
func ProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	issuer := requestIssuer(r)
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"resource":                 issuer,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
	})
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

func (h Handlers) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authorization requires the signed-in human whose authority the agent will borrow")
		return
	}
	q := r.URL.Query()
	if q.Get("response_type") != "code" {
		oauthError(w, http.StatusBadRequest, "unsupported_response_type", "only response_type=code")
		return
	}
	// S256 is mandatory (OAuth 2.1): no challenge and the downgrade to
	// plain are both refused before any code exists.
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) < 43 {
		oauthError(w, http.StatusBadRequest, "invalid_request", "PKCE S256 code_challenge is required")
		return
	}
	scopes, err := parseOAuthScopes(q.Get("scope"))
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")

	code, err := randomToken()
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	err = database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		var uris []string
		err := tx.QueryRow(r.Context(),
			`SELECT redirect_uris FROM oauth_client WHERE client_id = $1`, clientID).Scan(&uris)
		if errors.Is(err, pgx.ErrNoRows) {
			return errUnknownClient
		}
		if err != nil {
			return err
		}
		if !containsExact(uris, redirectURI) {
			return errRedirectMismatch
		}
		_, err = tx.Exec(r.Context(), `
			INSERT INTO oauth_authorization_code
			  (workspace_id, code_hash, client_id, user_id, scopes, code_challenge, redirect_uri, resource, expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        $1, $2, $3, $4, $5, $6, NULLIF($7, ''), now() + $8::interval)`,
			hashOAuthCode(code), clientID, id.UserID, scopes, q.Get("code_challenge"),
			redirectURI, q.Get("resource"), authCodeTTL.String())
		return err
	})
	switch {
	case errors.Is(err, errUnknownClient):
		oauthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		return
	case errors.Is(err, errRedirectMismatch):
		// Never redirect to an unregistered URI — answer the caller.
		oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is not registered for this client")
		return
	case err != nil:
		httperr.Write(w, r, err)
		return
	}

	// V1 consent is the signed-in session driving this URL; the explicit
	// consent screen is a SPA slice on top of the same endpoint.
	location, _ := url.Parse(redirectURI)
	params := location.Query()
	params.Set("code", code)
	if state := q.Get("state"); state != "" {
		params.Set("state", state)
	}
	location.RawQuery = params.Encode()
	// Not an open redirect: the target was matched EXACTLY against the
	// client's registered redirect_uris above; an unregistered URI never
	// reaches this line.
	http.Redirect(w, r, location.String(), http.StatusFound) // #nosec G710
}

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

	var (
		userID      ids.UUID
		workspaceID ids.UUID
		scopes      []string
	)
	err := database.WithWorkspaceTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
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

var (
	errUnknownClient    = errors.New("oauth: unknown client")
	errRedirectMismatch = errors.New("oauth: redirect mismatch")
	errCodeSpent        = errors.New("oauth: code spent")
	errGrantMismatch    = errors.New("oauth: grant mismatch")
	errAudienceMismatch = errors.New("oauth: audience mismatch")
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

func containsExact(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
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

// requestIssuer reconstructs the externally visible origin. TLS
// terminates ahead of the chassis in production, so the forwarded
// proto wins when present.
func requestIssuer(r *http.Request) string {
	// Only the two legitimate values are honored; anything else in the
	// forwarded header is attacker noise. Host itself must be sanitized
	// by the fronting proxy — the metadata documents say so.
	scheme := "https"
	switch r.Header.Get("X-Forwarded-Proto") {
	case "https":
	case "http":
		scheme = "http"
	default:
		if r.TLS == nil {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

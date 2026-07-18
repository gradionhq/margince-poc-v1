// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The per-provider OAuth capture surface (RC-8; capture.md CAP-WIRE-1):
// listConnectors / connectConnector / connectorOAuthCallback /
// disconnectConnector, for the standing (persisted) mail connectors —
// distinct from the one-shot /connectors/imap/connect. connect returns the
// provider consent URL carrying a signed state; the session-less callback
// verifies that state, exchanges the code, reconstructs the granting human's
// authority from the (trusted) state, and persists the connection through the
// capture Registry; the background poller then syncs it. Only gmail is wired
// today (gcal/graph are contract-declared, not yet implemented).
//
// connectorHandlers is embedded in Server as a zero value; a role that does
// not wire the Gmail OAuth app (no --gmail-client-id) leaves oauth/registry
// nil, and every operation answers the repo's standard 501 rather than
// nil-derefing — capture stays declared-but-absent by omission.

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// connectStateTTL bounds the consent round-trip: generous for a human to
// click through Google, short enough that a leaked state is quickly useless.
const connectStateTTL = 10 * time.Minute

// providerGmail is the only capture provider this transport implements today
// (gcal/graph are contract-declared, not yet wired).
const providerGmail = "gmail"

// oauthCSRFCookie carries the per-flow nonce (SameSite=Lax so it rides the
// top-level redirect back from Google) that must match the nonce in the
// signed state — the account-linking-CSRF defence.
const oauthCSRFCookie = "oauth_csrf"

type connectorHandlers struct {
	registry *capture.Registry
	oauth    gmail.OAuth
	gmailAPI gmail.API
	signer   stateSigner
	// publicBaseURL is the canonical public/front origin (the SPA): where the
	// browser lands after consent, and — for a same-origin deployment — the
	// default base for the callback redirect_uri too.
	publicBaseURL string
	// apiBaseURL is the api's externally-reachable base, used ONLY for the
	// callback redirect_uri (which must resolve to where the api serves it).
	// Empty for a same-origin deployment (the callback then rides
	// publicBaseURL/v1); a split dev stack (SPA :5173, api :8080) sets it.
	apiBaseURL string
}

// wired reports whether the Gmail OAuth app is composed for this role.
func (h connectorHandlers) wired() bool { return h.registry != nil && h.oauth != nil }

func (h connectorHandlers) callbackURL() string {
	base := h.apiBaseURL
	if base == "" {
		base = h.publicBaseURL
	}
	return strings.TrimRight(base, "/") + "/v1/connectors/gmail/callback"
}

// landingURL is the wizard's OAuth-return deep link. The SPA is hash-routed,
// so the outcome rides the route — the connect step reads it and renders
// success, the honest denial, or the honest failure. Earlier-step completion
// is server-derived on mount, so no client state needs to survive the
// redirect.
func (h connectorHandlers) landingURL(outcome string) string {
	return strings.TrimRight(h.publicBaseURL, "/") + "/#/onboarding/connect/" + outcome
}

func (h connectorHandlers) ListConnectors(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		httperr.NotImplemented(w, r, "ListConnectors")
		return
	}
	views, err := h.registry.Connections(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	resp := crmcontracts.CaptureConnectionListResponse{
		Data: make([]crmcontracts.CaptureConnection, 0, len(views)),
	}
	for _, v := range views {
		resp.Data = append(resp.Data, toContractConnection(v))
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

func (h connectorHandlers) ConnectConnector(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider) {
	if !h.wired() {
		httperr.NotImplemented(w, r, "ConnectConnector")
		return
	}
	if string(provider) == providerIMAP {
		h.connectIMAP(w, r)
		return
	}
	if string(provider) != providerGmail {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "connector_unsupported",
			Detail: "Only gmail and imap connect here; gcal/graph are not yet implemented.",
		})
		return
	}
	actor, ok := principal.Actor(r.Context())
	ws, hasWS := principal.WorkspaceID(r.Context())
	if !ok || actor.Type != principal.PrincipalHuman || !hasWS {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized,
			Code:   "unauthenticated",
			Detail: "Connecting a mailbox is a signed-in human action.",
		})
		return
	}
	// CSRF: a random nonce goes into both a SameSite=Lax cookie and the signed
	// state; the callback requires them to match, so a victim can't complete an
	// attacker-initiated flow (account-linking CSRF).
	nonce := rand.Text()
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCSRFCookie,
		Value:    nonce,
		Path:     "/v1/connectors",
		MaxAge:   int(connectStateTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	state := h.signer.sign(
		connectState{Workspace: ws, User: actor.UserID, Provider: providerGmail, Nonce: nonce},
		time.Now().Add(connectStateTTL),
	)
	authURL := h.oauth.AuthCodeURL(state, h.callbackURL())
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ConnectConnectorResponse{AuthorizeUrl: &authURL})
}

func (h connectorHandlers) ConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider, params crmcontracts.ConnectorOAuthCallbackParams) {
	if !h.wired() {
		httperr.NotImplemented(w, r, "ConnectorOAuthCallback")
		return
	}
	ctx := r.Context()
	// The user denied consent at Google — surface it honestly, never as an error.
	if params.Error != nil && *params.Error != "" {
		http.Redirect(w, r, h.landingURL("denied"), http.StatusFound)
		return
	}
	// The signed state is the only trustworthy carrier here (no session cookie
	// on the cross-site redirect). A bad/expired/mismatched state or a missing
	// code cannot proceed — redirect with an honest error, details logged only.
	st, err := h.signer.verify(params.State, time.Now())
	if err != nil || string(provider) != providerGmail || st.Provider != providerGmail || params.Code == nil || *params.Code == "" {
		slog.WarnContext(ctx, "gmail connector callback rejected", "err", err, "provider", string(provider))
		http.Redirect(w, r, h.landingURL("error"), http.StatusFound)
		return
	}
	// CSRF: the SameSite=Lax oauth_csrf cookie must match the nonce in the
	// signed state, proving the browser completing the flow is the one that
	// started it. Without this, an attacker could trick a victim into
	// completing the attacker's flow and link the victim's mailbox to the
	// attacker's account (account-linking CSRF).
	csrf, cerr := r.Cookie(oauthCSRFCookie)
	if cerr != nil || st.Nonce == "" || subtle.ConstantTimeCompare([]byte(csrf.Value), []byte(st.Nonce)) != 1 {
		slog.WarnContext(ctx, "gmail connector callback: CSRF nonce missing/mismatched", "err", cerr)
		http.Redirect(w, r, h.landingURL("error"), http.StatusFound)
		return
	}
	// One-shot: clear the CSRF cookie now that it's been consumed (same secure
	// attributes as when it was set, so the delete is honored).
	http.SetCookie(w, &http.Cookie{
		Name: oauthCSRFCookie, Path: "/v1/connectors", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})

	authReq, err := gmail.AuthRequestFrom(*params.Code, h.callbackURL())
	if err != nil {
		slog.ErrorContext(ctx, "gmail connector callback: building auth request", "err", err)
		http.Redirect(w, r, h.landingURL("error"), http.StatusFound)
		return
	}
	auth, err := gmail.New(h.oauth, h.gmailAPI).Authenticate(ctx, authReq)
	if err != nil {
		slog.ErrorContext(ctx, "gmail connector callback: token exchange", "err", err)
		http.Redirect(w, r, h.landingURL("error"), http.StatusFound)
		return
	}

	// Reconstruct the granting human's authority from the trusted (signed)
	// state: workspace + user id. A minimal read-scoped human principal is
	// what Registry.Connect needs — it stamps granted_by and checks the
	// connector's read scope; the app_user FK rejects a vanished user, and
	// every later sync re-derives live RBAC (so a since-revoked human's grant
	// dies at sync, not here).
	runCtx := principal.WithWorkspaceID(ctx, st.Workspace)
	runCtx = principal.WithActor(runCtx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:" + st.User.String(),
		UserID: st.User,
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
	if _, err := h.registry.Connect(runCtx, providerGmail, auth); err != nil {
		slog.ErrorContext(ctx, "gmail connector callback: persisting connection", "err", err)
		http.Redirect(w, r, h.landingURL("error"), http.StatusFound)
		return
	}
	http.Redirect(w, r, h.landingURL("ok"), http.StatusFound)
}

func (h connectorHandlers) DisconnectConnector(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider) {
	if h.registry == nil {
		httperr.NotImplemented(w, r, "DisconnectConnector")
		return
	}
	if err := h.registry.Disconnect(r.Context(), string(provider)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toContractConnection maps a registry connection row onto the wire shape.
// Storage now uses the contract's own status vocabulary (CAP-DDL-2 reconciled
// capture_connection to it), so status is a straight cast — no translation. The
// credential is never present.
func toContractConnection(v capture.ConnectionView) crmcontracts.CaptureConnection {
	c := crmcontracts.CaptureConnection{
		Id:             openapi_types.UUID(v.ID),
		Provider:       crmcontracts.CaptureConnectionProvider(v.Provider),
		Status:         crmcontracts.CaptureConnectionStatus(v.Status),
		Scopes:         v.Scopes,
		WatchExpiresAt: v.WatchExpiresAt,
	}
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	if len(v.Cursor) > 0 {
		s := string(v.Cursor)
		c.SyncCursor = &s
	}
	return c
}

const providerIMAP = "imap"

const codeConnectorStoreFailed = "connector_store_failed"

// connectIMAP establishes a STANDING imap connection: the credentials are
// probed (dial + login, session closed), sealed to the vault by
// Registry.Connect, and the background sweep takes over — the same lifecycle
// as gmail, minus the OAuth ceremony. The transient one-shot pull
// (/connectors/imap/connect) remains a separate surface until its callers
// migrate.
func (h connectorHandlers) connectIMAP(w http.ResponseWriter, r *http.Request) {
	actor, ok := principal.Actor(r.Context())
	_, hasWS := principal.WorkspaceID(r.Context())
	if !ok || actor.Type != principal.PrincipalHuman || !hasWS {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized,
			Code:   "unauthenticated",
			Detail: "Connecting a mailbox is a signed-in human action.",
		})
		return
	}
	var req crmcontracts.ConnectConnectorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Imap == nil || req.Imap.Secret == nil {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_credentials_required",
			Detail: "The imap provider needs host, username and secret in the request body.",
		})
		return
	}
	port := 0
	if req.Imap.Port != nil {
		port = *req.Imap.Port
	}
	authReq, err := imap.AuthRequestFrom(imap.Credentials{
		Host:     req.Imap.Host,
		Port:     port,
		Email:    req.Imap.Username,
		Password: *req.Imap.Secret,
	})
	if err != nil {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_credentials_invalid",
			Detail: "These credentials could not be processed.",
		})
		return
	}
	auth, err := imap.NewStanding().Authenticate(r.Context(), authReq)
	if err != nil {
		writeIMAPConnectError(w, r, err)
		return
	}
	if _, err := h.registry.Connect(r.Context(), providerIMAP, auth); err != nil {
		slog.ErrorContext(r.Context(), "imap connector: persisting connection", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   codeConnectorStoreFailed,
			Detail: "The connection could not be stored. Nothing was captured; try again.",
		})
		return
	}
	views, err := h.registry.Connections(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "imap connector: reading back connection", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   codeConnectorStoreFailed,
			Detail: "The connection was stored but could not be read back.",
		})
		return
	}
	for _, v := range views {
		if v.Provider == providerIMAP {
			w.Header().Set("Content-Type", "application/json")
			conn := toContractConnection(v)
			//craft:ignore swallowed-errors terminal response encode; the client sees a broken body, retrying changes nothing
			_ = json.NewEncoder(w).Encode(crmcontracts.ConnectConnectorResponse{
				Connection: &conn,
			})
			return
		}
	}
	httperr.Write(w, r, &httperr.DetailedError{
		Status: http.StatusInternalServerError,
		Code:   codeConnectorStoreFailed,
		Detail: "The connection was stored but did not appear in the read-back.",
	})
}

// writeIMAPConnectError maps the connector sentinels onto the transport
// without leaking the provider's raw error.
func writeIMAPConnectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, imap.ErrLoginRejected):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_login_rejected",
			Detail: "The mailbox rejected these credentials. Check host, email and app password.",
		})
	case errors.Is(err, imap.ErrUnreachable):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadGateway,
			Code:   "imap_unreachable",
			Detail: "The mail server could not be reached.",
		})
	default:
		slog.ErrorContext(r.Context(), "imap connector: authenticate", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   "imap_connect_failed",
			Detail: "The connection could not be established.",
		})
	}
}

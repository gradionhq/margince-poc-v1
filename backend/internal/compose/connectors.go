// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The per-provider OAuth capture surface (RC-8; capture.md CAP-WIRE-1):
// listConnectors / connectConnector / connectorOAuthCallback /
// disconnectConnector, for the standing (persisted) mail/calendar connectors —
// distinct from the one-shot /connectors/imap/connect. connect returns the
// provider consent URL carrying a signed state; the session-less callback
// verifies that state, exchanges the code, reconstructs the granting human's
// authority from the (trusted) state, and persists the connection through the
// capture Registry; the background poller then syncs it. Gmail and Google
// Calendar (gcal) share one Google OAuth app here (differing only in scope);
// graph (Microsoft 365) is contract-declared but not yet wired, and imap has
// its own /connectors/imap/connect surface.
//
// connectorHandlers is embedded in Server as a zero value; a role that does
// not wire the Google OAuth app (no --gmail-client-id) leaves oauth/registry
// nil, and every operation answers the repo's standard 501 rather than
// nil-derefing — capture stays declared-but-absent by omission.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gcal"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// connectStateTTL bounds the consent round-trip: generous for a human to
// click through Google, short enough that a leaked state is quickly useless.
const connectStateTTL = 10 * time.Minute

// The capture providers this OAuth transport implements. Gmail and gcal share
// one Google OAuth app (differing only in scope); graph/imap are elsewhere.
const (
	providerGmail = "gmail"
	providerGcal  = "gcal"
)

// googleOAuth is the shared shape of the gmail/gcal OAuth clients — the same
// Google OAuth2 handshake, so the provider-generic connect/callback code holds
// either one without knowing which.
type googleOAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (refreshToken string, err error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// oauthCSRFCookie carries the per-flow nonce (SameSite=Lax so it rides the
// top-level redirect back from Google) that must match the nonce in the
// signed state — the account-linking-CSRF defence.
const oauthCSRFCookie = "oauth_csrf"

type connectorHandlers struct {
	registry  *capture.Registry
	oauth     gmail.OAuth
	gmailAPI  gmail.API
	gcalOAuth gcal.OAuth
	gcalAPI   gcal.API
	signer    stateSigner
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

// wired reports whether the Google OAuth app is composed for this role (Gmail
// is the anchor: gmail + gcal share one app, so its presence mounts the
// surface).
func (h connectorHandlers) wired() bool { return h.registry != nil && h.oauth != nil }

// supportsProvider reports whether this handler has the OAuth client wired for
// the given capture provider (graph/imap are not this transport's).
func (h connectorHandlers) supportsProvider(provider string) bool {
	switch provider {
	case providerGmail:
		return h.oauth != nil
	case providerGcal:
		return h.gcalOAuth != nil
	default:
		return false
	}
}

// oauthFor returns the OAuth client for a supported provider, or nil.
//
//nolint:ireturn // returns the shared google-oauth seam by design (provider dispatch)
func (h connectorHandlers) oauthFor(provider string) googleOAuth {
	switch provider {
	case providerGmail:
		return h.oauth
	case providerGcal:
		return h.gcalOAuth
	default:
		return nil
	}
}

// authenticate exchanges an OAuth code for the sealed connector.Auth using the
// right provider connector (each stamps its own owner + scopes).
func (h connectorHandlers) authenticate(ctx context.Context, provider, code, redirectURI string) (connector.Auth, error) {
	switch provider {
	case providerGmail:
		req, err := gmail.AuthRequestFrom(code, redirectURI)
		if err != nil {
			return nil, err
		}
		return gmail.New(h.oauth, h.gmailAPI).Authenticate(ctx, req)
	case providerGcal:
		req, err := gcal.AuthRequestFrom(code, redirectURI)
		if err != nil {
			return nil, err
		}
		return gcal.New(h.gcalOAuth, h.gcalAPI).Authenticate(ctx, req)
	default:
		return nil, fmt.Errorf("compose: no OAuth connector for provider %q", provider)
	}
}

func (h connectorHandlers) callbackURL(provider string) string {
	base := h.apiBaseURL
	if base == "" {
		base = h.publicBaseURL
	}
	return strings.TrimRight(base, "/") + "/v1/connectors/" + provider + "/callback"
}

func (h connectorHandlers) landingURL(outcome string) string {
	return strings.TrimRight(h.publicBaseURL, "/") + "/activation?connect=" + outcome
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
	prov := string(provider)
	if !h.supportsProvider(prov) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "connector_unsupported",
			Detail: "Only the gmail and gcal connectors are available here; imap uses /connectors/imap/connect, and graph is not yet implemented.",
		})
		return
	}
	actor, ok := principal.Actor(r.Context())
	ws, hasWS := principal.WorkspaceID(r.Context())
	if !ok || actor.Type != principal.PrincipalHuman || !hasWS {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized,
			Code:   "unauthenticated",
			Detail: "Connecting a mailbox or calendar is a signed-in human action.",
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
		connectState{Workspace: ws, User: actor.UserID, Provider: prov, Nonce: nonce},
		time.Now().Add(connectStateTTL),
	)
	authURL := h.oauthFor(prov).AuthCodeURL(state, h.callbackURL(prov))
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ConnectConnectorResponse{AuthorizeUrl: &authURL})
}

func (h connectorHandlers) ConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider, params crmcontracts.ConnectorOAuthCallbackParams) {
	if !h.wired() {
		httperr.NotImplemented(w, r, "ConnectorOAuthCallback")
		return
	}
	ctx := r.Context()
	prov := string(provider)
	// The user denied consent at Google — surface it honestly, never as an error.
	if params.Error != nil && *params.Error != "" {
		http.Redirect(w, r, h.landingURL("denied"), http.StatusFound)
		return
	}
	// The signed state is the only trustworthy carrier here (no session cookie
	// on the cross-site redirect). A bad/expired/mismatched state, an
	// unsupported provider, or a missing code cannot proceed — redirect with an
	// honest error, details logged only.
	st, err := h.signer.verify(params.State, time.Now())
	if err != nil || !h.supportsProvider(prov) || st.Provider != prov || params.Code == nil || *params.Code == "" {
		slog.WarnContext(ctx, "connector callback rejected", "err", err, "provider", prov)
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
		slog.WarnContext(ctx, "connector callback: CSRF nonce missing/mismatched", "err", cerr, "provider", prov)
		http.Redirect(w, r, h.landingURL("error"), http.StatusFound)
		return
	}
	// One-shot: clear the CSRF cookie now that it's been consumed (same secure
	// attributes as when it was set, so the delete is honored).
	http.SetCookie(w, &http.Cookie{
		Name: oauthCSRFCookie, Path: "/v1/connectors", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})

	auth, err := h.authenticate(ctx, prov, *params.Code, h.callbackURL(prov))
	if err != nil {
		slog.ErrorContext(ctx, "connector callback: token exchange", "err", err, "provider", prov)
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
	if _, err := h.registry.Connect(runCtx, prov, auth); err != nil {
		slog.ErrorContext(ctx, "connector callback: persisting connection", "err", err, "provider", prov)
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

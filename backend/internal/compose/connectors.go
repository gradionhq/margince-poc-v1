// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The per-provider OAuth capture surface (RC-8; capture.md CAP-WIRE-1):
// listConnectors / connectConnector / connectorOAuthCallback /
// disconnectConnector, for the standing (persisted) mail connectors —
// distinct from the direct-credential /connectors/imap/connect (connectors_imap.go),
// which is itself a standing connection, just OAuth-less. connect returns the
// provider consent URL carrying a signed state; the session-less callback
// verifies that state, exchanges the code, reconstructs the granting human's
// authority from the (trusted) state, and persists the connection through the
// capture Registry; the background poller then syncs it. Gmail, Microsoft
// Graph, and Google Calendar (gcal) share this flow, dispatched by provider;
// gcal reuses the same Google OAuth app as Gmail, differing only in scope.
//
// connectorHandlers is embedded in Server as a zero value; a role that wires
// neither OAuth app (no --gmail-client-id / --graph-client-id) leaves
// oauth/registry nil, and every operation answers the repo's standard 501
// rather than nil-derefing — capture stays declared-but-absent by omission.

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gcal"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/graph"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// connectStateTTL bounds the consent round-trip: generous for a human to
// click through Google, short enough that a leaked state is quickly useless.
const connectStateTTL = 10 * time.Minute

// The OAuth capture providers this transport implements. Gmail and Google
// Calendar (gcal) share one Google OAuth app (differing only in scope); graph
// is the Microsoft 365 app.
const (
	providerGmail = "gmail"
	providerGcal  = "gcal"
	providerGraph = "graph"
)

// codeUnauthorized is the RFC 7807 code for connector/backfill ops that
// require a signed-in human principal — the contract's documented 401
// machine code (crm.yaml's normative Unauthorized example), matching the
// platform 401 writer.
const codeUnauthorized = "unauthorized"

type connectorHandlers struct {
	registry *capture.Registry
	// authority is identity's live resolver. The callback re-reads the
	// granting human through it before it spends the authorization code: the
	// signed state proves who STARTED the consent, and minutes of provider
	// UI can pass before it comes back.
	authority authz.Resolver
	// imapAuthenticate probes+seals IMAP credentials; nil means the
	// production standing connector. Injectable so the transport's own
	// branches are testable without a live mail server.
	imapAuthenticate func(ctx context.Context, req connector.AuthRequest) (connector.Auth, error)
	oauth            gmail.OAuth
	gmailAPI         gmail.API
	gcalOAuth        gcal.OAuth
	gcalAPI          gcal.API
	graphOAuth       graph.OAuth
	graphAPI         graph.API
	signer           stateSigner
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

func (h connectorHandlers) callbackURL(provider string) string {
	base := h.apiBaseURL
	if base == "" {
		base = h.publicBaseURL
	}
	return strings.TrimRight(base, "/") + "/v1/connectors/" + provider + "/callback"
}

// oauthApp is one composed OAuth provider seen through the shared
// connect/callback flow: the consent-URL builder and the code-for-credential
// exchange, so the flow itself stays provider-agnostic.
type oauthApp struct {
	authCodeURL  func(state, redirectURI string) string
	authenticate func(ctx context.Context, code, redirectURI string) (connector.Auth, error)
}

// oauthApp resolves the composed OAuth app for a provider; false when this
// deployment did not configure it (its surface stays the declared 501).
func (h connectorHandlers) oauthApp(provider string) (oauthApp, bool) {
	switch provider {
	case providerGmail:
		if h.oauth == nil {
			return oauthApp{}, false
		}
		return oauthApp{
			authCodeURL: h.oauth.AuthCodeURL,
			authenticate: func(ctx context.Context, code, redirectURI string) (connector.Auth, error) {
				req, err := gmail.AuthRequestFrom(code, redirectURI)
				if err != nil {
					return nil, err
				}
				return gmail.New(h.oauth, h.gmailAPI).Authenticate(ctx, req)
			},
		}, true
	case providerGcal:
		if h.gcalOAuth == nil {
			return oauthApp{}, false
		}
		return oauthApp{
			authCodeURL: h.gcalOAuth.AuthCodeURL,
			authenticate: func(ctx context.Context, code, redirectURI string) (connector.Auth, error) {
				req, err := gcal.AuthRequestFrom(code, redirectURI)
				if err != nil {
					return nil, err
				}
				return gcal.New(h.gcalOAuth, h.gcalAPI).Authenticate(ctx, req)
			},
		}, true
	case providerGraph:
		if h.graphOAuth == nil {
			return oauthApp{}, false
		}
		return oauthApp{
			authCodeURL: h.graphOAuth.AuthCodeURL,
			authenticate: func(ctx context.Context, code, redirectURI string) (connector.Auth, error) {
				req, err := graph.AuthRequestFrom(code, redirectURI)
				if err != nil {
					return nil, err
				}
				return graph.New(h.graphOAuth, h.graphAPI).Authenticate(ctx, req)
			},
		}, true
	default:
		return oauthApp{}, false
	}
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
	// The standing IMAP connect needs only the registry (credentials are
	// per-connection, vault-sealed) — never the Gmail OAuth app.
	if string(provider) == providerIMAP {
		if h.registry == nil {
			httperr.NotImplemented(w, r, "ConnectConnector")
			return
		}
		h.connectIMAP(w, r)
		return
	}
	if string(provider) != providerGmail && string(provider) != providerGraph && string(provider) != providerGcal {
		if h.registry == nil {
			httperr.NotImplemented(w, r, "ConnectConnector")
			return
		}
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "connector_unsupported",
			Detail: "Only the gmail, gcal, graph and imap connectors can be connected here.",
		})
		return
	}
	app, ok := h.oauthApp(string(provider))
	if h.registry == nil || !ok {
		// The OAuth app for this provider is not composed in this deployment —
		// its surface keeps the declared 501.
		httperr.NotImplemented(w, r, "ConnectConnector")
		return
	}
	actor, ok := principal.Actor(r.Context())
	ws, hasWS := principal.WorkspaceID(r.Context())
	if !ok || actor.Type != principal.PrincipalHuman || !hasWS {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized,
			Code:   codeUnauthorized,
			Detail: "Connecting a mailbox is a signed-in human action.",
		})
		return
	}
	// The body is optional for OAuth providers (they submit nothing), so an
	// absent one is not a failure — only a malformed one is. ContentLength ==
	// 0 is the reliable "definitely no body" signal; -1 (chunked, length
	// unknown until read) must still be decoded, or a chunked request's
	// return_to is silently dropped and a malformed chunked body skips
	// rejection entirely.
	returnTo := returnToOnboarding
	if r.ContentLength != 0 {
		var req crmcontracts.ConnectConnectorRequest
		if !httperr.Decode(w, r, &req) {
			return
		}
		if req.ReturnTo != nil && string(*req.ReturnTo) == returnToSettings {
			returnTo = returnToSettings
		}
	}
	// CSRF: a random nonce goes into both a SameSite=Lax cookie and the signed
	// state; the callback requires them to match, so a victim can't complete an
	// attacker-initiated flow (account-linking CSRF).
	nonce := rand.Text()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName(string(provider)),
		Value:    nonce,
		Path:     "/v1/connectors",
		MaxAge:   int(connectStateTTL / time.Second),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	state := h.signer.sign(
		connectState{
			Workspace: ws, User: actor.UserID, Provider: string(provider),
			Nonce: nonce, ReturnTo: returnTo, Version: stateVersionNamespacedCSRF,
		},
		time.Now().Add(connectStateTTL),
	)
	authURL := app.authCodeURL(state, h.callbackURL(string(provider)))
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ConnectConnectorResponse{AuthorizeUrl: &authURL})
}

func (h connectorHandlers) ConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, provider crmcontracts.CaptureProvider, params crmcontracts.ConnectorOAuthCallbackParams) {
	app, ok := h.oauthApp(string(provider))
	if h.registry == nil || !ok {
		httperr.NotImplemented(w, r, "ConnectorOAuthCallback")
		return
	}
	ctx := r.Context()
	// The signed state is the only trustworthy carrier here (no session cookie
	// on the cross-site redirect), and it is what names the surface the human
	// started from. Verify it BEFORE branching on the outcome: a denial that
	// began in Settings has to land back in Settings, or the person never sees
	// the note explaining what happened. An unverifiable state yields no
	// trustworthy ReturnTo, so those paths keep the default.
	st, err := h.signer.verify(params.State, time.Now())
	stateTrusted := err == nil && st.Provider == string(provider)
	returnTo := returnToOnboarding
	if stateTrusted {
		returnTo = st.ReturnTo
	}
	// The user denied consent at the provider — surface it honestly, never as
	// an error.
	if params.Error != nil && *params.Error != "" {
		http.Redirect(w, r, h.landingURL(outcomeDenied, returnTo, string(provider)), http.StatusFound)
		return
	}
	// A bad/expired/mismatched state or a missing code cannot proceed —
	// redirect with an honest error, details logged only.
	if !stateTrusted || params.Code == nil || *params.Code == "" {
		slog.WarnContext(ctx, "connector callback rejected", "err", err, "provider", string(provider))
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo, string(provider)), http.StatusFound)
		return
	}
	// CSRF: the nonce cookie must match the nonce in the signed state, proving
	// the browser completing the flow is the one that started it. Without this,
	// an attacker could trick a victim into completing the attacker's flow and
	// link the victim's mailbox to the attacker's account (account-linking
	// CSRF). The signed state is already verified by this point — what this
	// establishes is the BROWSER's identity — so the redirect can honor the
	// surface the flow started from.
	if !consumeCSRFNonce(w, r, string(provider), st) {
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo, string(provider)), http.StatusFound)
		return
	}

	runCtx := grantorContext(ctx, st)
	// The grant needs LIVE authority, resolved before the code is spent: an
	// exchanged authorization code is a real provider credential, and one
	// minted for a human who may no longer hold it has to be revoked at the
	// provider rather than simply discarded. Registry.Connect enforces the same
	// invariant; this is the cheaper, earlier half of it.
	if err := h.requireLiveGrantor(runCtx, st); err != nil {
		slog.WarnContext(ctx, "connector callback: the granting human no longer holds live authority",
			"err", err, "provider", string(provider))
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo, string(provider)), http.StatusFound)
		return
	}

	auth, err := app.authenticate(ctx, *params.Code, h.callbackURL(string(provider)))
	if err != nil {
		logConnectFailure(ctx, string(provider), err)
		http.Redirect(w, r, h.landingURL(connectFailureOutcome(string(provider), err), returnTo, string(provider)), http.StatusFound)
		return
	}

	if _, err := h.registry.Connect(runCtx, string(provider), auth); err != nil {
		slog.ErrorContext(ctx, "connector callback: persisting connection", "err", err, "provider", string(provider))
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo, string(provider)), http.StatusFound)
		return
	}
	http.Redirect(w, r, h.landingURL(outcomeOK, returnTo, string(provider)), http.StatusFound)
}

// grantorContext reconstructs the granting human's authority from the trusted
// (signed) state: workspace + user id. A minimal read-scoped human principal is
// what Registry.Connect needs — it stamps granted_by and checks the connector's
// read scope; the live authority behind it is resolved separately.
func grantorContext(ctx context.Context, st connectState) context.Context {
	runCtx := principal.WithWorkspaceID(ctx, st.Workspace)
	return principal.WithActor(runCtx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:" + st.User.String(),
		UserID: st.User,
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
}

// requireLiveGrantor resolves the human named by the signed state against
// identity's live authority. A missing resolver is a wiring fault and fails
// closed: an unchecked grant is the hole this exists to close, and silently
// skipping the check would look exactly like passing it.
func (h connectorHandlers) requireLiveGrantor(ctx context.Context, st connectState) error {
	if h.authority == nil {
		return errors.New("connector callback: no authority resolver wired — a grant cannot be checked against live authority")
	}
	if _, err := h.authority.EffectiveRBAC(ctx, st.Workspace, st.User); err != nil {
		return err
	}
	_, err := h.authority.SeatType(ctx, st.Workspace, st.User)
	return err
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
		Scopes:         v.ProviderScopes,
		WatchExpiresAt: v.WatchExpiresAt,
		AccountLabel:   v.AccountLabel,
	}
	if c.Scopes == nil {
		c.Scopes = []string{}
	}
	if len(v.Cursor) > 0 {
		s := string(v.Cursor)
		c.SyncCursor = &s
	}
	c.LastSyncedAt = v.LastSyncedAt
	c.LastSyncErrorClass = v.LastErrorClass
	c.NextSyncDueAt = v.NextSyncDueAt
	bf := backfillStatusPayload(v.Backfill)
	c.Backfill = &bf
	return c
}

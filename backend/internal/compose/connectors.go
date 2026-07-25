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
	"crypto/subtle"
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
	"github.com/gradionhq/margince/backend/internal/modules/capture/googleconn"
	"github.com/gradionhq/margince/backend/internal/modules/capture/graph"
	"github.com/gradionhq/margince/backend/internal/modules/capture/oauthflow"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

// oauthCSRFCookie is the base name of the per-flow nonce cookie (SameSite=Lax
// so it rides the top-level redirect back from Google) that must match the
// nonce in the signed state — the account-linking-CSRF defence.
const oauthCSRFCookie = "oauth_csrf"

// csrfCookieName namespaces the CSRF nonce cookie per provider, so the two
// connectors on the one Google OAuth app (gmail + gcal) don't clobber each
// other's nonce in concurrent flows. The providers that shipped on the shared
// un-suffixed "oauth_csrf" name (gmail, graph) keep it, so a consent round-trip
// started on a prior build still verifies against a callback served by this one
// across a deploy; only the new provider (gcal) takes the suffix.
func csrfCookieName(provider string) string {
	if provider == providerGmail || provider == providerGraph {
		return oauthCSRFCookie
	}
	return oauthCSRFCookie + "_" + provider
}

type connectorHandlers struct {
	registry *capture.Registry
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

// landingURL is the OAuth-return deep link. The SPA is hash-routed, so the
// outcome rides the route — the landing surface reads it and renders success,
// the honest denial, or the honest failure. returnTo names WHICH surface, and
// is resolved through a closed set: it is an enum, never a URL, so no caller
// input ever reaches the Location header. Anything unrecognized — including an
// absent value and a URL-shaped one — lands on onboarding.
func (h connectorHandlers) landingURL(outcome, returnTo string) string {
	route := "/#/onboarding/connect/"
	if returnTo == returnToSettings {
		route = "/#/settings/integrations/"
	}
	return strings.TrimRight(h.publicBaseURL, "/") + route + outcome
}

const (
	returnToOnboarding = "onboarding"
	returnToSettings   = "settings"
)

// The OAuth-return outcomes the landing surface renders. They are a closed set
// the SPA maps to copy, never rendering a raw route segment for one it does not
// know (Settings shows nothing; onboarding falls back to its generic failure).
// They exist because "it failed, try again" is only honest for SOME failures: a
// declined consent is not a failure at all, a refused credential needs its human
// to reconnect, and a provider API that was never enabled for the deployment
// needs an administrator — retrying that one forever cannot work.
const (
	outcomeOK            = "ok"
	outcomeDenied        = "denied"
	outcomeRejected      = "rejected"
	outcomeMisconfigured = "misconfigured"
	outcomeError         = "error"
)

// disabledProviderAPI reports whether the failure is a provider API that was
// never enabled for this deployment. The reason vocabulary is Google's, so the
// question is only asked of the Google connectors — a Microsoft failure that
// happened to reuse one of those code names must not be answered with Google's
// remedy.
func disabledProviderAPI(provider string, err error) bool {
	if provider != providerGmail && provider != providerGcal {
		return false
	}
	return googleconn.Misconfigured(err)
}

// connectFailureOutcome picks the landing outcome for a failed consent
// completion, so what the human reads matches what they can actually do about
// it. Two distinct deployment faults land on misconfigured — a provider API that
// was never enabled, and an OAuth client the provider refused to authenticate
// (a wrong client id/secret, which is provider-independent) — because a human
// re-consenting clears neither. Anything we cannot attribute to the provider
// stays the generic error: an honest "we don't know yet", never a guess at whose
// fault it is.
func connectFailureOutcome(provider string, err error) string {
	switch {
	case disabledProviderAPI(provider, err), oauthflow.Misconfigured(err):
		return outcomeMisconfigured
	case errors.Is(err, connector.ErrAuthRejected):
		return outcomeRejected
	default:
		return outcomeError
	}
}

// logConnectFailure records a failed consent completion for the operator. The
// two deployment faults each get a line naming their OWN remedy: a machine
// reason code ("accessNotConfigured", "invalid_client") says what the provider
// refused but not what to go do about it, and the two remedies are different
// places. The generic line deliberately does not name a step — the failure can
// come from the token exchange, the refresh, or the owner lookup, and
// ProviderError.Op already carries which call it actually was.
func logConnectFailure(ctx context.Context, provider string, err error) {
	switch {
	case disabledProviderAPI(provider, err):
		slog.ErrorContext(ctx,
			"connector callback: this provider's API is not enabled for the deployment — "+
				"enable it in the Google Cloud project behind the OAuth client, then reconnect",
			"err", err, "provider", provider)
	case oauthflow.Misconfigured(err):
		slog.ErrorContext(ctx,
			"connector callback: the provider refused this deployment's OAuth client — "+
				"check the configured client id and secret for this connector",
			"err", err, "provider", provider)
	default:
		slog.ErrorContext(ctx, "connector callback: completing consent failed",
			"err", err, "provider", provider)
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
		connectState{Workspace: ws, User: actor.UserID, Provider: string(provider), Nonce: nonce, ReturnTo: returnTo},
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
		http.Redirect(w, r, h.landingURL(outcomeDenied, returnTo), http.StatusFound)
		return
	}
	// A bad/expired/mismatched state or a missing code cannot proceed —
	// redirect with an honest error, details logged only.
	if !stateTrusted || params.Code == nil || *params.Code == "" {
		slog.WarnContext(ctx, "connector callback rejected", "err", err, "provider", string(provider))
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo), http.StatusFound)
		return
	}
	// CSRF: the SameSite=Lax oauth_csrf cookie must match the nonce in the
	// signed state, proving the browser completing the flow is the one that
	// started it. Without this, an attacker could trick a victim into
	// completing the attacker's flow and link the victim's mailbox to the
	// attacker's account (account-linking CSRF). The signed state is already
	// verified by this point — what this check establishes is the BROWSER's
	// identity — so the redirect can honor the surface the flow started from.
	csrf, cerr := r.Cookie(csrfCookieName(string(provider)))
	if cerr != nil || st.Nonce == "" || subtle.ConstantTimeCompare([]byte(csrf.Value), []byte(st.Nonce)) != 1 {
		slog.WarnContext(ctx, "connector callback: CSRF nonce missing/mismatched", "err", cerr, "provider", string(provider))
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo), http.StatusFound)
		return
	}
	// One-shot: clear the CSRF cookie now that it's been consumed (same secure
	// attributes as when it was set, so the delete is honored).
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName(string(provider)), Path: "/v1/connectors", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})

	auth, err := app.authenticate(ctx, *params.Code, h.callbackURL(string(provider)))
	if err != nil {
		logConnectFailure(ctx, string(provider), err)
		http.Redirect(w, r, h.landingURL(connectFailureOutcome(string(provider), err), returnTo), http.StatusFound)
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
	if _, err := h.registry.Connect(runCtx, string(provider), auth); err != nil {
		slog.ErrorContext(ctx, "connector callback: persisting connection", "err", err, "provider", string(provider))
		http.Redirect(w, r, h.landingURL(outcomeError, returnTo), http.StatusFound)
		return
	}
	http.Redirect(w, r, h.landingURL(outcomeOK, returnTo), http.StatusFound)
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

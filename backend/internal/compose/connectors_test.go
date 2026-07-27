// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gcal"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const testStateKey = "a-32-byte-or-longer-signing-key!!"

// wiredHandlers builds connectorHandlers with a real signer + real (pure)
// Google OAuth clients (gmail + gcal) and a non-nil registry, so the non-DB
// paths (connect URL, state verification, provider gating) exercise real code.
// The registry's DB methods are never reached on these paths.
func wiredHandlers() connectorHandlers {
	return connectorHandlers{
		registry:      capture.NewRegistry(nil, nil, liveAuthority{}, nil),
		authority:     liveAuthority{},
		oauth:         gmail.NewOAuth(gmail.OAuthConfig{ClientID: "cid", ClientSecret: "sec", Scopes: []string{"https://www.googleapis.com/auth/gmail.readonly"}}),
		gmailAPI:      gmail.NewAPI(nil, ""),
		gcalOAuth:     gcal.NewOAuth(gcal.OAuthConfig{ClientID: "cid", ClientSecret: "sec"}),
		gcalAPI:       gcal.NewAPI(nil, ""),
		signer:        newStateSigner([]byte(testStateKey)),
		publicBaseURL: "https://app.test", // the SPA/front origin — landing
		apiBaseURL:    "https://api.test", // the api origin — callback redirect_uri
	}
}

func humanCtx() context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.MustParse("11111111-1111-1111-1111-111111111111"))
	return principal.WithActor(ctx, principal.Principal{
		Type:   principal.PrincipalHuman,
		ID:     "human:22222222-2222-2222-2222-222222222222",
		UserID: ids.MustParse("22222222-2222-2222-2222-222222222222"),
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
}

func TestConnectConnectorReturnsSignedAuthorizeURL(t *testing.T) {
	h := wiredHandlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/gmail/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "gmail")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var resp crmcontracts.ConnectConnectorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AuthorizeUrl == nil {
		t.Fatal("authorize_url missing")
	}
	u, err := url.Parse(*resp.AuthorizeUrl)
	if err != nil {
		t.Fatalf("authorize_url not a URL: %v", err)
	}
	// The redirect_uri points back at our callback, and the state must verify.
	if got := u.Query().Get("redirect_uri"); got != "https://api.test/v1/connectors/gmail/callback" {
		t.Errorf("redirect_uri = %q, want the api callback", got)
	}
	st, err := h.signer.verify(u.Query().Get("state"), time.Now())
	if err != nil {
		t.Fatalf("authorize_url state does not verify: %v", err)
	}
	if st.Provider != "gmail" || st.User != ids.MustParse("22222222-2222-2222-2222-222222222222") {
		t.Errorf("state = %+v, want gmail + the acting user", st)
	}
}

func TestConnectConnectorReturnsSignedAuthorizeURLForGcal(t *testing.T) {
	h := wiredHandlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/gcal/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "gcal")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for gcal (body %s)", rec.Code, rec.Body)
	}
	var resp crmcontracts.ConnectConnectorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AuthorizeUrl == nil {
		t.Fatal("authorize_url missing")
	}
	u, err := url.Parse(*resp.AuthorizeUrl)
	if err != nil {
		t.Fatalf("authorize_url not a URL: %v", err)
	}
	// The redirect_uri and the signed state must both be gcal's.
	if got := u.Query().Get("redirect_uri"); got != "https://api.test/v1/connectors/gcal/callback" {
		t.Errorf("redirect_uri = %q, want the gcal callback", got)
	}
	// The calendar consent scope must be requested — and ONLY it, never the mail
	// scope (least privilege: a calendar connect must not silently grant mail).
	if got := u.Query().Get("scope"); !strings.Contains(got, "calendar.readonly") {
		t.Errorf("scope = %q, want the calendar.readonly consent", got)
	} else if strings.Contains(got, "gmail.readonly") {
		t.Errorf("scope = %q, must not request the gmail.readonly scope", got)
	}
	// gcal authorizes separately from Gmail: no incremental authorization, so
	// the calendar credential can't accrete the mail-read grant.
	if u.Query().Get("include_granted_scopes") != "" {
		t.Errorf("gcal consent must not send include_granted_scopes (per-connector least privilege)")
	}
	st, err := h.signer.verify(u.Query().Get("state"), time.Now())
	if err != nil {
		t.Fatalf("authorize_url state does not verify: %v", err)
	}
	if st.Provider != "gcal" {
		t.Errorf("state provider = %q, want gcal", st.Provider)
	}
}

func TestConnectConnectorRejectsUnsupportedProvider(t *testing.T) {
	h := wiredHandlers()
	rec := httptest.NewRecorder()
	// A provider this OAuth transport does not handle (not gmail/gcal/graph, and
	// not the separate imap surface) must be refused with a clean
	// connector_unsupported, never misrouted onto another provider's flow.
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/whatsapp/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "whatsapp")

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for an unsupported provider", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "connector_unsupported") {
		t.Errorf("body should carry connector_unsupported: %s", rec.Body)
	}
}

func TestConnectConnectorNotImplementedWhenUnwired(t *testing.T) {
	var h connectorHandlers // zero value: no oauth/registry
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/connectors/gmail/connect", nil).WithContext(humanCtx())

	h.ConnectConnector(rec, req, "gmail")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when the Gmail app is not wired", rec.Code)
	}
}

func TestCallbackDeniedRedirectsHonestly(t *testing.T) {
	h := wiredHandlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	denied := "access_denied"

	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{Error: &denied})

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/#/onboarding/connect/denied/gmail" {
		t.Errorf("Location = %q, want the denied landing", loc)
	}
}

func TestCallbackBadStateRedirectsError(t *testing.T) {
	h := wiredHandlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	code := "the-code"

	// A forged/garbage state must not proceed to a token exchange.
	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{
		Code:  &code,
		State: "not-a-valid-signed-state",
	})

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/#/onboarding/connect/error/gmail" {
		t.Errorf("Location = %q, want the error landing", loc)
	}
}

func TestLandingURLMapsReturnToThroughAClosedSet(t *testing.T) {
	h := connectorHandlers{publicBaseURL: "https://crm.example.com/"}
	for _, tc := range []struct {
		name     string
		returnTo string
		want     string
	}{
		{"settings", "settings", "https://crm.example.com/#/settings/integrations/ok/graph"},
		{"onboarding", "onboarding", "https://crm.example.com/#/onboarding/connect/ok/graph"},
		{"absent falls back to onboarding", "", "https://crm.example.com/#/onboarding/connect/ok/graph"},
		{"unknown falls back to onboarding", "elsewhere", "https://crm.example.com/#/onboarding/connect/ok/graph"},
		{"a URL is never reflected", "https://evil.example/", "https://crm.example.com/#/onboarding/connect/ok/graph"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.landingURL("ok", tc.returnTo, "graph"); got != tc.want {
				t.Errorf("landingURL(ok, %q, graph) = %q, want %q", tc.returnTo, got, tc.want)
			}
		})
	}
}

// The landing surface has to know WHICH mailbox this round-trip connected: a
// workspace with both Gmail and Microsoft live would otherwise offer the
// import for whichever one the roster lists first.
func TestLandingURLNamesTheProviderTheConsentReturnedFor(t *testing.T) {
	h := connectorHandlers{publicBaseURL: "https://crm.example.com"}
	for _, provider := range []string{"gmail", "graph", "gcal"} {
		want := "https://crm.example.com/#/onboarding/connect/ok/" + provider
		if got := h.landingURL(outcomeOK, returnToOnboarding, provider); got != want {
			t.Errorf("landingURL(ok, onboarding, %q) = %q, want %q", provider, got, want)
		}
	}
}

// What the human reads has to match what they can do. Three failures that all
// used to render "please try again": one where trying again is right, one where
// it repeats the same refusal, and one that no user action can ever clear.
func TestConnectFailureOutcomeMatchesTheRemedy(t *testing.T) {
	disabledAPI := &connector.ProviderError{
		Op: "/calendars/primary", Status: http.StatusForbidden,
		Reason: "accessNotConfigured", Class: gcal.ErrAuthRejected,
	}
	revokedGrant := &connector.ProviderError{
		Op: "token", Status: http.StatusBadRequest,
		Reason: "invalid_grant", Class: gcal.ErrAuthRejected,
	}
	unreachable := &connector.ProviderError{
		Op: "token", Status: http.StatusBadGateway, Class: gcal.ErrUnreachable,
	}

	// A refused OAuth client is the deployment's own credentials, not the
	// human's grant — and unlike the disabled API it is provider-independent.
	badClient := &connector.ProviderError{
		Op: "token", Status: http.StatusUnauthorized,
		Reason: "invalid_client", Class: gcal.ErrAuthRejected,
	}

	for _, tc := range []struct {
		name     string
		provider string
		err      error
		want     string
	}{
		{"a disabled provider API needs an administrator", providerGcal, disabledAPI, outcomeMisconfigured},
		{"a refused OAuth client needs an administrator too", providerGcal, badClient, outcomeMisconfigured},
		{"a refused OAuth client is provider-independent", providerGraph, badClient, outcomeMisconfigured},
		{"a refused grant needs its human", providerGcal, revokedGrant, outcomeRejected},
		{"an unreachable provider is worth retrying", providerGcal, unreachable, outcomeError},
		{"a bare auth sentinel carries no remedy detail", providerGcal, gcal.ErrAuthRejected, outcomeRejected},
		{"an unattributed failure stays generic", providerGcal, errors.New("something of ours broke"), outcomeError},
		// Google's reason vocabulary must not be read against Microsoft: the
		// remedy names a Google Cloud project that has nothing to do with it.
		{"a Google reason code is not consulted for graph", providerGraph, disabledAPI, outcomeRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := connectFailureOutcome(tc.provider, tc.err); got != tc.want {
				t.Errorf("connectFailureOutcome(%s, %v) = %q, want %q", tc.provider, tc.err, got, tc.want)
			}
		})
	}
}

// A human who declines consent from Settings has to land back in Settings — on
// the onboarding screen they never see the note explaining what happened. The
// signed state is what names the surface, so it is verified before the outcome
// is branched on.
func TestCallbackDenialReturnsToTheSurfaceItStartedFrom(t *testing.T) {
	h := wiredHandlers()
	denied := "access_denied"
	state := h.signer.sign(connectState{
		Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"),
		User:      ids.MustParse("22222222-2222-2222-2222-222222222222"),
		Provider:  "gmail",
		Nonce:     "a-nonce",
		ReturnTo:  returnToSettings,
	}, time.Now().Add(time.Minute))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{
		Error: &denied,
		State: state,
	})

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/#/settings/integrations/denied/gmail" {
		t.Errorf("Location = %q, want the denial to land back in Settings", loc)
	}
}

// The mirror of the above: a denial whose state cannot be trusted has no
// trustworthy ReturnTo either, so it keeps the default rather than honoring a
// surface an attacker chose.
func TestCallbackDenialWithUntrustedStateKeepsTheDefaultSurface(t *testing.T) {
	h := wiredHandlers()
	denied := "access_denied"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{
		Error: &denied,
		State: "forged",
	})

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/#/onboarding/connect/denied/gmail" {
		t.Errorf("Location = %q, want the onboarding default", loc)
	}
}

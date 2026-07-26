// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The per-flow CSRF nonce: one browser can have two consent flows open at
// once (a mailbox and a calendar, a Google account and a Microsoft one), so
// the nonce cookie is per provider. A shared name means the second consent
// silently invalidates the first.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture/gmail"
	"github.com/gradionhq/margince/backend/internal/modules/capture/oauthflow"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// refusingOAuth is a gmail.OAuth the provider refuses at the token endpoint.
// It makes the callback's post-CSRF path reachable with no network: a flow
// that clears the nonce gate lands on "rejected", one that does not lands on
// "error", so the two are distinguishable on the wire.
type refusingOAuth struct{}

func (refusingOAuth) AuthCodeURL(state, redirectURI string) string {
	return "https://consent.test/authorize?redirect_uri=" + url.QueryEscape(redirectURI) + "&state=" + url.QueryEscape(state)
}

func (refusingOAuth) Exchange(context.Context, string, string) (oauthflow.TokenGrant, error) {
	return oauthflow.TokenGrant{}, gmail.ErrAuthRejected
}

func (refusingOAuth) AccessToken(context.Context, string) (string, error) {
	return "", gmail.ErrAuthRejected
}

// browserJar mimics a cookie store: same name overwrites, distinct names
// coexist — which is exactly what the namespacing turns on.
type browserJar map[string]string

func (j browserJar) keep(resp *http.Response) {
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(j, c.Name)
			continue
		}
		j[c.Name] = c.Value
	}
}

func (j browserJar) attach(req *http.Request) {
	for name, value := range j {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
}

func TestConcurrentGmailAndGraphConsentDoNotClobberEachOther(t *testing.T) {
	if csrfCookieName(providerGmail) == csrfCookieName(providerGraph) {
		t.Fatalf("gmail and graph share the CSRF cookie name %q", csrfCookieName(providerGmail))
	}

	h := graphWiredHandlers()
	h.oauth = refusingOAuth{}
	jar := browserJar{}

	start := func(t *testing.T, provider string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/connectors/"+provider+"/connect", nil).WithContext(humanCtx())
		h.ConnectConnector(rec, req, crmcontracts.CaptureProvider(provider))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s connect status = %d, want 200 (body %s)", provider, rec.Code, rec.Body)
		}
		var resp crmcontracts.ConnectConnectorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s connect decode: %v", provider, err)
		}
		if resp.AuthorizeUrl == nil {
			t.Fatalf("%s connect returned no authorize_url", provider)
		}
		u, err := url.Parse(*resp.AuthorizeUrl)
		if err != nil {
			t.Fatalf("%s authorize_url not a URL: %v", provider, err)
		}
		jar.keep(rec.Result())
		return u.Query().Get("state")
	}

	// The human opens the mailbox consent, then the calendar-adjacent
	// Microsoft one, before finishing either.
	gmailState := start(t, providerGmail)
	start(t, providerGraph)
	if len(jar) != 2 {
		t.Fatalf("two open consent flows left %d nonce cookies (%v); the second overwrote the first", len(jar), jar)
	}

	code := "the-code"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	jar.attach(req)
	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{State: gmailState, Code: &code})

	if got, want := rec.Header().Get("Location"), "https://app.test/#/onboarding/connect/rejected"; got != want {
		t.Fatalf("gmail callback landed on %q, want %q — the graph flow must not invalidate the gmail nonce", got, want)
	}
	// The nonce is one-shot: the cookie the callback consumed is cleared, and
	// the still-open graph flow's is not.
	jar.keep(rec.Result())
	if _, live := jar[csrfCookieName(providerGmail)]; live {
		t.Errorf("the consumed gmail nonce survived the callback")
	}
	if _, live := jar[csrfCookieName(providerGraph)]; !live {
		t.Errorf("the gmail callback cleared the still-open graph flow's nonce")
	}
}

func TestCallbackAcceptsAConsentStartedBeforeTheCookieWasNamespaced(t *testing.T) {
	// A consent round-trip in flight across the deploy that introduced the
	// namespacing: its state carries no version, and its browser holds the
	// un-suffixed cookie. It must still complete.
	h := graphWiredHandlers()
	h.oauth = refusingOAuth{}
	state := h.signer.sign(connectState{
		Workspace: ids.MustParse("11111111-1111-1111-1111-111111111111"),
		User:      ids.MustParse("22222222-2222-2222-2222-222222222222"),
		Provider:  providerGmail,
		Nonce:     "legacy-nonce",
	}, time.Now().Add(connectStateTTL))

	code := "the-code"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/connectors/gmail/callback", nil)
	req.AddCookie(&http.Cookie{Name: oauthCSRFCookie, Value: "legacy-nonce"})
	h.ConnectorOAuthCallback(rec, req, "gmail", crmcontracts.ConnectorOAuthCallbackParams{State: state, Code: &code})

	if got, want := rec.Header().Get("Location"), "https://app.test/#/onboarding/connect/rejected"; got != want {
		t.Fatalf("pre-namespacing callback landed on %q, want %q", got, want)
	}
	// The legacy cookie is the one that must be cleared; leaving it set
	// poisons the next flow, which reads the namespaced name.
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthCSRFCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("the legacy nonce cookie was not cleared: %v", rec.Result().Cookies())
	}
}

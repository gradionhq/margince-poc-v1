// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The consent screen's read model (GET /oauth/consent-request): which of the
// signed-in human's passports may be lent to the requesting client. Each
// exclusion the query enforces — own passports only, alive, unbound — is
// asserted separately, so a query that dropped one filter would still fail a
// test that only counted rows. What the client requested is NOT an exclusion,
// which is its own test below.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// consentCookieName is the double-submit cookie the authorize GET arms. Spelled
// here rather than imported: identity keeps it unexported, and a test that
// restates the wire name catches a rename that would silently break every
// browser mid-flow.
const consentCookieName = "crm_oauth_consent"

// mintPassport creates a hand-minted passport through the public surface and
// returns its id — never an INSERT, so the row matches what a human's mint
// actually writes.
func (o *oauthEnv) mintPassport(t *testing.T, label string, scopes []string) string {
	t.Helper()
	var minted struct {
		ID string `json:"passport_id"`
	}
	if status := o.call(t, "POST", "/v1/passports", anyMap{
		"label": label, "scopes": scopes,
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("mint %q → %d", label, status)
	}
	return minted.ID
}

func (o *oauthEnv) revokePassport(t *testing.T, id string) {
	t.Helper()
	if status := o.call(t, "DELETE", "/v1/passports/"+id, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke %s → %d", id, status)
	}
}

// consentRequest reads the consent screen's model for a pending authorization.
func (o *oauthEnv) consentRequest(t *testing.T, scope string) consentRequestWire {
	t.Helper()
	var got consentRequestWire
	status := o.call(t, "GET",
		"/v1/oauth/consent-request?client_id="+url.QueryEscape(o.clientID)+
			"&scope="+url.QueryEscape(scope), nil, nil, &got)
	if status != http.StatusOK {
		t.Fatalf("consent-request → %d", status)
	}
	return got
}

type consentRequestWire struct {
	ClientName string `json:"client_name"`
	Offline    bool   `json:"offline"`
	Passports  []struct {
		ID     string   `json:"id"`
		Label  string   `json:"label"`
		Scopes []string `json:"scopes"`
	} `json:"passports"`
}

// A passport is lendable only if it is THIS human's, still alive, and not
// already bound to a connection. Each exclusion is asserted separately: a query
// that dropped one filter would still pass a test that only counted rows.
func TestSelectablePassportsExcludesEveryUnlendableShape(t *testing.T) {
	o := setupOAuth(t)
	ctx := context.Background()

	o.mintPassport(t, "lendable", []string{"read", "write"})
	revoked := o.mintPassport(t, "revoked", []string{"read"})
	o.revokePassport(t, revoked)
	bound := o.mintPassport(t, "bound", []string{"read"})
	if _, err := o.owner.Exec(ctx,
		`WITH new_grant AS (
		   INSERT INTO oauth_grant (workspace_id, client_id, user_id, scopes, refresh_allowed)
		   SELECT workspace_id, $2, on_behalf_of, ARRAY['read']::text[], false
		   FROM passport WHERE id = $1 RETURNING id)
		 UPDATE passport SET oauth_grant_id = new_grant.id
		 FROM new_grant WHERE passport.id = $1`, bound, o.clientID); err != nil {
		t.Fatalf("binding a passport to a grant: %v", err)
	}

	got := o.consentRequest(t, "read write")

	var labels []string
	for _, option := range got.Passports {
		labels = append(labels, option.Label)
	}
	if !slices.Equal(labels, []string{"lendable"}) {
		t.Fatalf("selectable passports = %v, want only [lendable]", labels)
	}
	// The scopes offered are the passport's own — the screen has one set to show,
	// because the connection receives exactly it.
	if got := got.Passports[0].Scopes; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("scopes = %v, want [read write]", got)
	}
}

// What the client asked for excludes nothing and narrows nothing: a passport
// grants its own scopes, so one that overlaps the request in NOTHING is still
// offered, and one WIDER than the request is offered at its full width. Both
// halves matter — the old intersection rule would have dropped the first from
// the list entirely and shown the second only its overlap.
func TestSelectablePassportsIgnoresWhatTheClientRequested(t *testing.T) {
	o := setupOAuth(t)
	o.mintPassport(t, "broad", []string{"read", "write", "send"})
	o.mintPassport(t, "disjoint", []string{"enrich"})

	got := o.consentRequest(t, "read")

	offered := map[string][]string{}
	for _, option := range got.Passports {
		offered[option.Label] = option.Scopes
	}
	if !slices.Equal(offered["broad"], []string{"read", "write", "send"}) {
		t.Errorf("broad passport offers %v, want [read write send] — the request does not trim it",
			offered["broad"])
	}
	if !slices.Equal(offered["disjoint"], []string{"enrich"}) {
		t.Errorf("disjoint passport offers %v, want [enrich] — a passport sharing no scope with the request is still the human's to lend",
			offered["disjoint"])
	}
}

// An expired passport is a dead credential, not a template — the
// expires_at > now() clause has to hold with no other exclusion in play, or
// a dropped `AND expires_at > now()` would pass every other test here
// silently. Set into the past through the owner connection rather than
// waiting on a real clock: the SQL predicate is judged against the
// database's own now(), so backdating the row is the deterministic way to
// put a passport on the wrong side of it.
func TestSelectablePassportsExcludesAnExpiredPassport(t *testing.T) {
	o := setupOAuth(t)
	ctx := context.Background()

	expired := o.mintPassport(t, "expired", []string{"read"})
	if _, err := o.owner.Exec(ctx,
		`UPDATE passport SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired); err != nil {
		t.Fatalf("backdating a passport's expiry: %v", err)
	}

	got := o.consentRequest(t, "read")
	if len(got.Passports) != 0 {
		t.Fatalf("passports = %v, want none — the only passport is expired", got.Passports)
	}
}

// Another human's passport must never appear on THIS human's consent screen,
// however completely it overlaps the request and however long it has left to
// live — on_behalf_of = $1 is what stands between an agent and borrowing
// authority nobody granted to it. The harness's only session is the
// bootstrap admin's, and this suite has no way to sign in AS a second human
// (that needs the password-reset flow's mailer, which lives in identity's own
// unit tests, not this HTTP harness) — so the second user is minted through
// the real admin invite endpoint, and their passport is inserted directly on
// the owner connection, the same way the "bound" fixture above binds a grant.
func TestSelectablePassportsExcludesAnotherUsersPassport(t *testing.T) {
	o := setupOAuth(t)
	ctx := context.Background()

	var other struct {
		ID string `json:"id"`
	}
	if status := o.call(t, "POST", "/v1/users", anyMap{
		"email": "otherhuman@acme.test", "display_name": "Other Human", "role": "rep",
	}, nil, &other); status != http.StatusCreated {
		t.Fatalf("inviting a second user → %d", status)
	}
	if _, err := o.owner.Exec(ctx,
		`INSERT INTO passport (workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
		 SELECT workspace_id, id, id, 'not mine', ARRAY['read']::text[], 'other-user-'||id, now() + interval '1 day'
		 FROM app_user WHERE id = $1`, other.ID); err != nil {
		t.Fatalf("minting a passport for the second user: %v", err)
	}

	got := o.consentRequest(t, "read")
	if len(got.Passports) != 0 {
		t.Fatalf("passports = %v, want none — this passport belongs to another user", got.Passports)
	}
}

// registerClientDirectly inserts a live oauth_client row over the owner
// connection. The harness's normal path to a live client is POST
// /oauth/register, but that endpoint is itself part of the connector's
// gated route group — unavailable in exactly the deployment state (connector
// off) a test needs a live client to probe.
func registerClientDirectly(t *testing.T, e *env, clientID string) {
	t.Helper()
	ctx := context.Background()
	var wsID string
	if err := e.owner.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("looking up the workspace: %v", err)
	}
	if _, err := e.owner.Exec(ctx,
		`INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris)
		 VALUES ($1, $2, 'directly registered', ARRAY['https://client.example/cb']::text[])`,
		wsID, clientID); err != nil {
		t.Fatalf("inserting a live oauth_client row: %v", err)
	}
}

// This read follows the connector's deployment switch exactly like every
// other /oauth/ path: a signed-in human asking about a client that
// genuinely exists gets the real answer only while the connector is
// declared, and the identical apperrors.ErrNotFound every absent /oauth/
// path answers once it is not. Both halves probe the SAME client id,
// inserted directly rather than through /oauth/register — that endpoint is
// itself ungated only while the connector is on, so it cannot supply the
// fixture for the off case — which keeps client existence constant and
// leaves the connector switch as the only variable between them.
func TestConsentRequestFollowsTheConnectorSwitch(t *testing.T) {
	const clientID = "directly-registered-client"
	// A request the CONTRACT cannot bind is refused by the generated router
	// before any middleware or handler runs, so the switch has no say in it.
	// That refusal is captured in both states and compared below: it is the
	// one answer this operation gives that the gate cannot change, and the
	// thing an off switch must hide is the DEPLOYMENT's state, which two
	// identical refusals disclose nothing about.
	var unbindable [2]int

	t.Run("off", func(t *testing.T) {
		e := setup(t)
		e.bootstrapWorkspace(t)
		registerClientDirectly(t, e, clientID)

		status := e.call(t, "GET", "/v1/oauth/consent-request?client_id="+clientID+"&scope=read", nil, nil, nil)
		if status != http.StatusNotFound {
			t.Fatalf("consent-request for a live client, connector off → %d, want 404", status)
		}
		unbindable[0] = e.call(t, "GET", "/v1/oauth/consent-request?client_id="+clientID, nil, nil, nil)
	})

	t.Run("on", func(t *testing.T) {
		c := setupConnector(t)
		registerClientDirectly(t, c.env, clientID)

		status := c.call(t, "GET", "/v1/oauth/consent-request?client_id="+clientID+"&scope=read", nil, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("consent-request for the same live client, connector on → %d, want 200", status)
		}
		unbindable[1] = c.call(t, "GET", "/v1/oauth/consent-request?client_id="+clientID, nil, nil, nil)
	})

	if unbindable[0] != http.StatusUnprocessableEntity || unbindable[1] != unbindable[0] {
		t.Errorf("a request missing the required scope parameter → %d with the connector off and %d with it on, want %d from both: the parameter refusal must not distinguish the two deployments",
			unbindable[0], unbindable[1], http.StatusUnprocessableEntity)
	}
}

// authorizeNoFollow issues the authorize GET with redirects DISABLED, so the
// 302 itself is the assertion target rather than whatever it points at. The
// Set-Cookie values come back too: the armed nonce is half of the double-submit
// pair the fragment carries the other half of.
func (o *oauthEnv) authorizeNoFollow(t *testing.T, extra url.Values) (int, string, string, []*http.Cookie) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		o.ts.URL+"/oauth/authorize?"+o.authorizeQuery(extra).Encode(), nil)
	if err != nil {
		t.Fatalf("building the authorize request: %v", err)
	}
	// o.client carries the signed-in human's session in its jar and trusts the
	// harness's TLS certificate; a fresh http.Client would have neither.
	o.client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	defer func() { o.client.CheckRedirect = nil }()
	resp, err := o.client.Do(req)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	defer closeBody(t, resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Location"), string(body), resp.Cookies()
}

func cookieValue(t *testing.T, setCookies []*http.Cookie, name string) string {
	t.Helper()
	for _, cookie := range setCookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	t.Fatalf("no %s cookie in the response", name)
	return ""
}

// The authorize GET hands the browser to the SPA and mints nothing. The params
// ride in the FRAGMENT, which is never sent to a server — so client_id, state
// and the PKCE challenge stay out of api access logs.
func TestAuthorizeRedirectsToTheSPAConsentScreen(t *testing.T) {
	o := setupOAuth(t)

	status, location, body, setCookies := o.authorizeNoFollow(t, url.Values{"scope": {"read write"}})

	if status != http.StatusFound {
		t.Fatalf("authorize → %d %s, want 302", status, body)
	}
	fragment := "#/oauth-consent?"
	if !strings.Contains(location, fragment) {
		t.Fatalf("Location = %q, want the SPA consent route %q", location, fragment)
	}
	// The old server-rendered page is gone: no HTML, and nothing that could be
	// mistaken for a consent decision.
	if strings.Contains(body, "<form") || strings.Contains(body, "Approve") {
		t.Fatalf("authorize still renders a form: %s", body)
	}
	// Everything the SPA needs must be AFTER the '#', not before it.
	before, after, _ := strings.Cut(location, "#")
	if strings.Contains(before, "client_id") {
		t.Fatalf("client_id leaked into the server-visible part of %q", location)
	}
	for _, param := range []string{"client_id", "state", "code_challenge", "redirect_uri", "scope", "consent"} {
		if !strings.Contains(after, param) {
			t.Fatalf("fragment %q is missing %s, which the SPA must POST back", after, param)
		}
	}
	// The consent nonce travels in the fragment and matches the cookie the same
	// response armed — the SPA cannot read that HttpOnly cookie, and the cookie
	// is Path=/oauth/authorize so no other endpoint can echo it either. The POST
	// then proves possession of BOTH.
	fragmentParams, err := url.ParseQuery(after[strings.Index(after, "?")+1:])
	if err != nil {
		t.Fatalf("parsing the fragment query %q: %v", after, err)
	}
	nonce := fragmentParams.Get("consent")
	if nonce == "" {
		t.Fatal("the fragment carries no consent nonce, so the POST can never satisfy the double-submit check")
	}
	if got := cookieValue(t, setCookies, consentCookieName); got != nonce {
		t.Fatalf("cookie %s = %q, want the fragment's nonce %q", consentCookieName, got, nonce)
	}
	// The nonce must not be visible to the server on the redirect it rode in on.
	if strings.Contains(before, nonce) {
		t.Fatalf("the consent nonce leaked into the server-visible part of %q", location)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// Federated sign-in end to end against a fake issuer (A107/ADR-0061 §6,
// §11): the state that is single-use and browser-bound, the binding written
// once on a verified-email match, and the four refusals that must each be a
// redirect rather than a session.
//
// The issuer is the only fake — a real provider is a true boundary. The
// database, the state row, the binding, the session, and the audit trail are
// all real.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

const oidcTestClientID = "margince.apps.googleusercontent.test"

// issuerFake is a provider: discovery, a JWK Set, and a token endpoint that
// mints an ID token for whichever identity the case has staged.
type issuerFake struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	// subject/email are the identity the next exchange asserts.
	subject string
	email   string
	// emailVerified is false for the case where the provider has not
	// verified the address it is asserting.
	emailVerified bool
	// hostedDomain populates the `hd` claim; empty omits it.
	hostedDomain string
	// nonce is captured from the authorization request, so the ID token
	// echoes the one the server actually stored.
	nonce string
	// authQuery is the last authorization request's parameters.
	authQuery url.Values
	// tokenForm is the last code exchange's form.
	tokenForm url.Values
}

func newIssuerFake(t *testing.T) *issuerFake {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the issuer signing key: %v", err)
	}
	f := &issuerFake{key: key, subject: "google-subject-1", emailVerified: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.writeJSON(w, map[string]any{
			"issuer":                 f.server.URL,
			"authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint":         f.server.URL + "/token",
			"jwks_uri":               f.server.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := f.key.PublicKey
		f.writeJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "issuer-key-1",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.authQuery = r.URL.Query()
		f.nonce = f.authQuery.Get("nonce")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.refuseToken(w)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			f.refuseToken(w)
			return
		}
		f.tokenForm = form
		// A real provider checks the verifier against the challenge it was
		// given. The fake does too, so "we sent a verifier" cannot pass for
		// "we sent the RIGHT verifier" if the flow ever regresses.
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.authQuery.Get("code_challenge") {
			f.refuseToken(w)
			return
		}
		f.writeJSON(w, map[string]any{"id_token": f.idToken()})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// refuseToken answers a token request the fixture could not read as a form —
// an RFC 6749 refusal, so a broken fixture reads as a failed exchange rather
// than as an unreadable transport error.
func (f *issuerFake) refuseToken(w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadRequest)
	f.writeJSON(w, map[string]any{"error": "invalid_request"})
}

func (f *issuerFake) writeJSON(w http.ResponseWriter, value map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(fmt.Sprintf("writing a fixture response: %v", err))
	}
}

func (f *issuerFake) idToken() string {
	now := time.Now()
	claims := map[string]any{
		"iss": f.server.URL, "sub": f.subject, "aud": oidcTestClientID,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"nonce": f.nonce, "email": f.email, "email_verified": f.emailVerified,
		"name": "Federated Human",
	}
	if f.hostedDomain != "" {
		claims["hd"] = f.hostedDomain
	}
	header := oidcSegment(map[string]any{"alg": "RS256", "typ": "JWT", "kid": "issuer-key-1"})
	payload := oidcSegment(claims)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		panic(fmt.Sprintf("signing the fixture id token: %v", err))
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func oidcSegment(value map[string]any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encoding a fixture jws segment: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// federatedEnv is one bootstrapped installation plus a wired federated
// provider pointing at the fake issuer.
type federatedEnv struct {
	*revocationEnv
	issuer *issuerFake
	h      Handlers
}

func setupFederatedEnv(t *testing.T, slug string, allowedDomains ...string) *federatedEnv {
	t.Helper()
	base := setupRevocationEnv(t, slug)
	issuer := newIssuerFake(t)
	login, err := NewOIDCLogin(OIDCLoginConfig{
		ProviderKey: "google", Label: "Continue with Google",
		Issuer: issuer.server.URL, ClientID: oidcTestClientID, ClientSecret: "the-client-secret",
		RedirectURI:    "http://localhost:8080/v1/auth/oidc/google/callback",
		AllowedDomains: allowedDomains,
	})
	if err != nil {
		t.Fatalf("wiring federated sign-in: %v", err)
	}
	return &federatedEnv{revocationEnv: base, issuer: issuer, h: NewHandlers(base.svc).WithOIDCLogin(login)}
}

// start runs the start endpoint and returns the state the browser holds.
// The provider's authorization endpoint is then called directly, because the
// browser's trip to the consent screen is the one part of the flow that is
// not this installation's code.
func (e *federatedEnv) start(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/google/start", nil).WithContext(e.wsOnlyCtx())
	e.h.StartOidcLogin(rec, req, "google")
	if rec.Code != http.StatusFound {
		t.Fatalf("start status = %d, want 302: %s", rec.Code, rec.Body)
	}
	state := oidcCookieValue(t, rec, oidcStateCookie)
	if state == "" {
		t.Fatal("start set no state cookie — the callback would have nothing to bind to")
	}
	// Follow the redirect to the provider so it captures the nonce it must
	// echo, exactly as a browser would.
	resp, err := e.issuer.server.Client().Get(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("following the authorization redirect: %v", err)
	}
	//craft:ignore swallowed-errors best-effort close of the fixture consent response; the captured query is the outcome
	defer func() { _ = resp.Body.Close() }()
	if got := e.issuer.authQuery.Get("state"); got != state {
		t.Errorf("the provider received state %q, want the cookie's %q", got, state)
	}
	if e.issuer.authQuery.Get("code_challenge_method") != "S256" {
		t.Error("the authorization request did not use PKCE S256")
	}
	return state
}

// callback runs the callback endpoint with the given query parameters.
func (e *federatedEnv) callback(t *testing.T, state, code string, cookieState string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/v1/auth/oidc/google/callback?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(code)
	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(e.wsOnlyCtx())
	if cookieState != "" {
		req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: cookieState})
	}
	rec := httptest.NewRecorder()
	e.h.CompleteOidcLogin(rec, req, "google", callbackParams(state, code, ""))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302: %s", rec.Code, rec.Body)
	}
	return rec
}

func TestFederatedSignInBindsOnFirstVerifiedEmailMatchThenResolvesBySubject(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-link")
	e.issuer.email = strings.ToUpper(e.member.Email) // the provider echoes what the human typed
	state := e.start(t)

	rec := e.callback(t, state, "the-authorization-code", state)
	session := oidcCookieValue(t, rec, sessionCookie)
	if session == "" {
		t.Fatalf("no session cookie after a valid federated sign-in (location %q)", rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Location"); strings.Contains(got, "sso_error") {
		t.Fatalf("location = %q, want the app rather than a refusal", got)
	}
	// The session is real: it authenticates the human the provider named.
	id, err := e.svc.Authenticate(e.wsOnlyCtx(), session)
	if err != nil {
		t.Fatalf("the minted session does not authenticate: %v", err)
	}
	if id.UserID != e.member.UserID {
		t.Errorf("session belongs to %s, want the matched member %s", id.UserID, e.member.UserID)
	}
	// The exchange carried the PKCE verifier — a stolen code alone is useless.
	if e.issuer.tokenForm.Get("code_verifier") == "" {
		t.Error("the code exchange carried no PKCE verifier")
	}

	// The binding is permanent and keyed by (issuer, subject); the email is
	// recorded only as evidence of how it came to be.
	issuer, subject, linkedEmail := e.binding(t, e.member.UserID)
	if issuer != e.issuer.server.URL || subject != "google-subject-1" {
		t.Errorf("binding = (%q, %q), want the issuer and subject", issuer, subject)
	}
	if linkedEmail != e.member.Email {
		t.Errorf("email_at_link_time = %q, want the lower-cased matched address", linkedEmail)
	}

	// The provider now asserts a DIFFERENT address for the same subject — a
	// rename at the provider. The subject is the identity, so the same human
	// signs in and no second binding appears.
	e.issuer.email = "renamed@" + strings.SplitN(e.member.Email, "@", 2)[1]
	renamedState := e.start(t)
	renamed := e.callback(t, renamedState, "another-code", renamedState)
	if session := oidcCookieValue(t, renamed, sessionCookie); session == "" {
		t.Fatalf("a renamed provider account did not sign in (location %q)", renamed.Header().Get("Location"))
	}
	if bindings := e.bindingCount(t, e.member.UserID); bindings != 1 {
		t.Errorf("bindings for the member = %d, want exactly 1", bindings)
	}
}

// Without the browser's handle cookie the callback is not a returning
// sign-in — it is somebody else's request carrying a state they saw.
func TestFederatedSignInRefusesACallbackWithoutTheBrowsersCookie(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-nocookie")
	e.issuer.email = e.member.Email
	state := e.start(t)

	rec := e.callback(t, state, "the-code", "")
	assertSSORefusal(t, rec, ssoErrorExpired)
	// And the state is still unconsumed, because the refusal happened before
	// any database work — a CSRF attempt must not spend a real attempt.
	if consumed := e.stateConsumed(t, state); consumed {
		t.Error("a cookie-less callback consumed the login state")
	}
}

func TestFederatedSignInStateIsSingleUse(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-replay")
	e.issuer.email = e.member.Email
	state := e.start(t)

	if session := oidcCookieValue(t, e.callback(t, state, "code-1", state), sessionCookie); session == "" {
		t.Fatal("the first callback minted no session")
	}
	// A replayed callback — the same state, the same code — is refused, and
	// refused as "expired" rather than as anything that identifies the human.
	assertSSORefusal(t, e.callback(t, state, "code-1", state), ssoErrorExpired)
}

func TestFederatedSignInRefusesAnUnmatchedIdentityNeutrally(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-nomatch")

	t.Run("no local human holds the verified address", func(t *testing.T) {
		e.issuer.subject = "google-subject-unknown"
		e.issuer.email = "stranger@elsewhere.test"
		state := e.start(t)
		assertSSORefusal(t, e.callback(t, state, "the-code", state), ssoErrorNotLinked)
	})

	t.Run("the address maps to a human already bound to another account", func(t *testing.T) {
		// Bind the member to one provider account…
		e.issuer.subject, e.issuer.email = "google-subject-first", e.member.Email
		state := e.start(t)
		if session := oidcCookieValue(t, e.callback(t, state, "code-1", state), sessionCookie); session == "" {
			t.Fatal("the first sign-in minted no session")
		}
		// …then arrive with a DIFFERENT subject asserting the same address.
		// An email match must never silently relink an already-bound identity,
		// and the refusal is the SAME code as "no such user" — telling them
		// apart would confirm which addresses exist.
		e.issuer.subject = "google-subject-second"
		state = e.start(t)
		assertSSORefusal(t, e.callback(t, state, "code-2", state), ssoErrorNotLinked)
		if bindings := e.bindingCount(t, e.member.UserID); bindings != 1 {
			t.Errorf("bindings after the relink attempt = %d, want 1", bindings)
		}
	})
}

func TestFederatedSignInRefusesAnUnverifiedEmail(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-unverified")
	e.issuer.email, e.issuer.emailVerified = e.member.Email, false
	state := e.start(t)

	// The address is exactly what the first binding is matched on, so an
	// unverified one cannot be allowed to match anything.
	assertSSORefusal(t, e.callback(t, state, "the-code", state), ssoErrorUnverifiedEmail)
	if bindings := e.bindingCount(t, e.member.UserID); bindings != 0 {
		t.Errorf("bindings = %d, want none", bindings)
	}
}

func TestFederatedSignInEnforcesTheAllowedDomainOnTheProvidersClaim(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-domain", "allowed.test")
	// The verified address is in the allowed domain, but the provider's own
	// organization claim says otherwise. The claim outranks the address (§14).
	e.issuer.email, e.issuer.hostedDomain = e.member.Email, "consumer.test"
	state := e.start(t)
	assertSSORefusal(t, e.callback(t, state, "the-code", state), ssoErrorDomainNotAllowed)

	// The single allowed domain is also hinted to the provider's account
	// chooser — a convenience that is never the enforcement point.
	if got := e.issuer.authQuery.Get("hd"); got != "allowed.test" {
		t.Errorf("hd hint = %q, want allowed.test", got)
	}
}

func TestFederatedSignInActivatesAnInvitedHuman(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-invited")
	// An invited member (A97) has no password: completing the provider flow
	// is exactly the proof of address control their activation waits for —
	// the §11 pending-administrator shape, generalized.
	invitedEmail := "invited@" + strings.SplitN(e.member.Email, "@", 2)[1]
	invitedID := ids.New[ids.UserKind]()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO app_user (id, workspace_id, email, display_name, status)
		 VALUES ($1, $2, $3, 'Invited Human', 'invited')`,
		invitedID, e.admin.WorkspaceID, invitedEmail); err != nil {
		t.Fatal(err)
	}
	e.issuer.subject, e.issuer.email = "google-subject-invited", invitedEmail
	state := e.start(t)

	rec := e.callback(t, state, "the-code", state)
	if session := oidcCookieValue(t, rec, sessionCookie); session == "" {
		t.Fatalf("an invited human was not signed in (location %q)", rec.Header().Get("Location"))
	}
	var status string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT status FROM app_user WHERE id = $1`, invitedID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Errorf("status after the federated activation = %q, want active", status)
	}
}

func TestFederatedSignInRefusesALockedAccount(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-locked")
	e.issuer.email = e.member.Email
	// The §27 lock is not password-specific: an admin (or a brute-force
	// streak) locking an account expects it locked, and a second door that
	// ignored it would make the first one decorative.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE app_user SET locked_until = now() + interval '15 minutes' WHERE id = $1`,
		e.member.UserID); err != nil {
		t.Fatal(err)
	}
	state := e.start(t)

	// Refused as the same neutral code as every other miss — the login screen
	// must not become an oracle for account state.
	assertSSORefusal(t, e.callback(t, state, "the-code", state), ssoErrorNotLinked)

	// And once the lock lapses the same identity signs in, so the refusal was
	// the lock and nothing else.
	if _, err := e.owner.Exec(context.Background(),
		`UPDATE app_user SET locked_until = NULL WHERE id = $1`, e.member.UserID); err != nil {
		t.Fatal(err)
	}
	unlocked := e.start(t)
	if session := oidcCookieValue(t, e.callback(t, unlocked, "another-code", unlocked), sessionCookie); session == "" {
		t.Fatal("an unlocked account was still refused")
	}
}

func TestPasswordDisabledInstallationRefusesTheWholePasswordFamily(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-only")
	h := e.h.WithPasswordLogin(false).WithPasswordReset(&capturedMail{}, "https://crm.example.test")

	// A CORRECT password is refused: the switch is enforced at the surface,
	// not merely validated at boot.
	rec := httptest.NewRecorder()
	h.Login(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/login",
		strings.NewReader(`{"email":"`+e.member.Email+`","password":"`+memberPassword+`"}`)).
		WithContext(e.wsOnlyCtx()))
	if rec.Code == http.StatusOK {
		t.Fatalf("password sign-in succeeded on a password-disabled installation: %s", rec.Body)
	}
	if cookie := oidcCookieValue(t, rec, sessionCookie); cookie != "" {
		t.Fatal("a password-disabled installation minted a session cookie")
	}

	// Recovery is part of the same family: a reset link that still minted a
	// session would be the bypass turning password login off exists to close.
	for name, serve := range map[string]func(http.ResponseWriter, *http.Request){
		"forgot-password": h.RequestPasswordReset,
		"reset-password":  h.ResetPassword,
	} {
		rec := httptest.NewRecorder()
		serve(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/"+name,
			strings.NewReader(`{"email":"`+e.member.Email+`","token":"t","new_password":"an entirely new password"}`)).
			WithContext(e.wsOnlyCtx()))
		if rec.Code < 400 {
			t.Errorf("%s answered %d on a password-disabled installation: %s", name, rec.Code, rec.Body)
		}
	}

	// And the probe says so, so the login screen never draws the form or the
	// reset link — even though a mailer IS wired here.
	rec = httptest.NewRecorder()
	h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if body := rec.Body.String(); !strings.Contains(body, `"password":false`) || !strings.Contains(body, `"password_reset":false`) {
		t.Errorf("capabilities = %s, want password and password_reset both false", body)
	}

	// The default posture is unchanged: a Handlers nobody told keeps password
	// sign-in on.
	rec = httptest.NewRecorder()
	e.h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"password":true`) {
		t.Errorf("default capabilities = %s, want password:true", rec.Body)
	}
}

func TestFederatedSignInSurfacesAProviderDenialAsItsOwnCode(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-denied")
	state := e.start(t)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/auth/oidc/google/callback?error=access_denied&state="+url.QueryEscape(state), nil).
		WithContext(e.wsOnlyCtx())
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: state})
	rec := httptest.NewRecorder()
	e.h.CompleteOidcLogin(rec, req, "google", callbackParams(state, "", "access_denied"))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", rec.Code)
	}
	// A human who changed their mind is the common case, and it reads as its
	// own state rather than as a failure of the installation.
	assertSSORefusal(t, rec, ssoErrorDenied)
}

func TestUnconfiguredProviderKeyIsNotFoundAndCapabilitiesListsTheWiredOne(t *testing.T) {
	e := setupFederatedEnv(t, "oidc-unconfigured")

	// A key this installation did not configure discloses nothing beyond what
	// the capabilities probe already says.
	rec := httptest.NewRecorder()
	e.h.StartOidcLogin(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/microsoft/start", nil).
		WithContext(e.wsOnlyCtx()), "microsoft")
	if rec.Code != http.StatusNotFound {
		t.Errorf("start for an unconfigured provider = %d, want 404: %s", rec.Code, rec.Body)
	}

	// The probe lists a provider because the flow behind it is wired.
	rec = httptest.NewRecorder()
	e.h.GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"key":"google"`) {
		t.Errorf("capabilities = %s, want the wired google provider", rec.Body)
	}
	// And an installation with none lists none — the login screen then draws
	// no button.
	rec = httptest.NewRecorder()
	NewHandlers(e.svc).GetAuthCapabilities(rec, httptest.NewRequest(http.MethodGet, "/v1/auth/capabilities", nil))
	if !strings.Contains(rec.Body.String(), `"oidc_providers":[]`) {
		t.Errorf("unwired capabilities = %s, want an empty provider list", rec.Body)
	}
}

// callbackParams builds the generated query-parameter struct the router
// hands the handler. An empty string means the parameter was ABSENT, which
// is a distinct case the handler must answer for itself.
func callbackParams(state, code, providerError string) crmcontracts.CompleteOidcLoginParams {
	params := crmcontracts.CompleteOidcLoginParams{}
	if state != "" {
		params.State = &state
	}
	if code != "" {
		params.Code = &code
	}
	if providerError != "" {
		params.Error = &providerError
	}
	return params
}

// --- assertions and reads ---

// assertSSORefusal pins that the callback redirected to the login screen
// with the expected bounded code and minted NO session.
func assertSSORefusal(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "sso_error="+wantCode) {
		t.Fatalf("location = %q, want sso_error=%s", location, wantCode)
	}
	if session := oidcCookieValue(t, rec, sessionCookie); session != "" {
		t.Fatal("a refused federated sign-in minted a session")
	}
}

// oidcCookieValue reads a set cookie's value; "" when the response set none
// (or cleared it).
func oidcCookieValue(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge >= 0 && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}

func (e *federatedEnv) binding(t *testing.T, userID ids.UserID) (issuer, subject, email string) {
	t.Helper()
	if err := e.owner.QueryRow(context.Background(),
		`SELECT issuer, subject, email_at_link_time FROM external_identity WHERE user_id = $1`,
		userID).Scan(&issuer, &subject, &email); err != nil {
		t.Fatalf("reading the binding: %v", err)
	}
	return issuer, subject, email
}

func (e *federatedEnv) bindingCount(t *testing.T, userID ids.UserID) int {
	t.Helper()
	var count int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM external_identity WHERE user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("counting bindings: %v", err)
	}
	return count
}

func (e *federatedEnv) stateConsumed(t *testing.T, rawState string) bool {
	t.Helper()
	var consumed bool
	err := database.WithWorkspaceTx(e.wsOnlyCtx(), e.svc.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT consumed_at IS NOT NULL FROM oidc_login_state WHERE state_hash = $1`,
			hashToken(rawState)).Scan(&consumed)
	})
	if err != nil {
		t.Fatalf("reading the login state: %v", err)
	}
	return consumed
}

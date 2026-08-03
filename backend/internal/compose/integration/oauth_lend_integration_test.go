// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The consent DECISION (POST /oauth/authorize), as distinct from the screen's
// read model next door in oauth_consent_integration_test.go: the human lends one
// of their own passports and the connection receives exactly that passport's
// scopes, or the human refuses and the client is told. Which passport was lent
// is recorded in the audit trail, because no column anywhere holds it.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// approve is one consent decision that LENDS a named passport: the GET arms the
// nonce, the POST names the passport, and the caller judges the answer. Spelled
// once so the success and refusal helpers below cannot drift apart in how they
// drive it.
func (o *oauthEnv) approve(t *testing.T, extra url.Values, passportID string) (status int, location, body string) {
	t.Helper()
	form := o.armConsent(t, extra)
	form.Set("passport_id", passportID)
	return o.postConsent(t, form)
}

// approveWithPassport lends a passport the CALLER minted and returns the code
// the client's redirect carries — so a test can lend authority wider or narrower
// than the request and assert on what the connection actually receives.
func (o *oauthEnv) approveWithPassport(t *testing.T, extra url.Values, passportID string) string {
	t.Helper()
	status, location, body := o.approve(t, extra, passportID)
	if status != http.StatusFound {
		t.Fatalf("consent POST → %d %s", status, body)
	}
	granted, err := url.Parse(location)
	if err != nil || granted.Query().Get("code") == "" || granted.Query().Get("state") != "night-state" {
		t.Fatalf("redirect malformed: %q", location)
	}
	return granted.Query().Get("code")
}

// approveRefused is approveWithPassport without the success assertion, for a
// caller whose subject IS the refusal — the fatal "want 302" would abort the
// test before its own assertion ran. It returns the armed nonce alongside the
// answer, because what a refusal must NOT hand back is that nonce. An empty
// passportID posts no selection at all.
func (o *oauthEnv) approveRefused(t *testing.T, extra url.Values, passportID string) (status int, location, armed string) {
	t.Helper()
	form := o.armConsent(t, extra)
	if passportID != "" {
		form.Set("passport_id", passportID)
	}
	status, location, _ = o.postConsent(t, form)
	return status, location, form.Get("consent")
}

// denyRaw is the human refusing. RFC 6749 §4.1.2.1 answers the CLIENT at its
// own redirect_uri, so the status and Location are the whole observable outcome
// — there is no code to hand back.
func (o *oauthEnv) denyRaw(t *testing.T, extra url.Values) (int, string) {
	t.Helper()
	form := o.armConsent(t, extra)
	form.Set("deny", "1")
	status, location, _ := o.postConsent(t, form)
	return status, location
}

// The connection receives exactly the lent passport's scopes. The client asks
// for LESS than the passport carries here — the case every mainstream MCP client
// produces, since they send no scope parameter at all and the authorize parser
// then defaults to read — and the grant is the passport's full set regardless.
// The old intersection rule capped this at the request, which is what made the
// 🟡 write half of the tool surface unreachable through a real client.
//
// Asserted on three independent records of the same grant, because any one of
// them alone could hold the request instead: the token response's scope, the
// minted credential's own scopes column, and the grant row the exchange wrote.
func TestApproveGrantsExactlyTheLentPassportsScopes(t *testing.T) {
	o := setupOAuth(t)
	passport := o.mintPassport(t, "broad", []string{"read", "write", "send"})

	code := o.approveWithPassport(t, url.Values{"scope": {"read"}}, passport)
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v", status, body)
	}
	// RFC 6749 §5.1: the response reports the scopes actually GRANTED, so a
	// client that asked for less still learns what it got. That honesty is what
	// makes the wider grant safe to hand over without asking.
	if scope, _ := body["scope"].(string); scope != "read write send" {
		t.Fatalf("granted scope = %q, want %q: the client asked for read, but the human lent all three",
			scope, "read write send")
	}
	minted, _ := body["access_token"].(string)
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM passport WHERE token_hash = $1
		   AND scopes = ARRAY['read','write','send']::text[]`,
		sha256Hex(minted))
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM oauth_grant WHERE scopes = ARRAY['read','write','send']::text[]`)
	// The lent passport is UNTOUCHED: the connection got its own credential, so
	// revoking the connection must not kill the human's REST credential (I3).
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM passport WHERE id = $1 AND revoked_at IS NULL AND oauth_grant_id IS NULL`,
		passport)

	// The passport is the WHOLE answer, not a floor added to the request: a
	// client asking for MORE than the passport carries still gets only the
	// passport. Without this half, granting the union of the two would pass.
	narrow := o.mintPassport(t, "narrow", []string{"read"})
	code = o.approveWithPassport(t, url.Values{"scope": {"read write"}}, narrow)
	status, body = o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v", status, body)
	}
	if scope, _ := body["scope"].(string); scope != "read" {
		t.Fatalf("granted scope = %q, want %q: the lent passport carries no write, however loudly the client asked",
			scope, "read")
	}
}

// lendAudit is the audit row's after image for a lend — typed, so a missing or
// renamed field reads as an empty value the assertions catch rather than a map
// lookup nobody notices.
type lendAudit struct {
	PassportID     string   `json:"passport_id"`
	ClientID       string   `json:"client_id"`
	Scopes         []string `json:"scopes"`
	RefreshAllowed bool     `json:"refresh_allowed"`
}

// WHICH passport was lent is the central authority fact of this flow, and no
// column anywhere holds it: oauth_authorization_code and oauth_grant each record
// the client and the scopes, neither records the credential the human handed
// over. The audit row is therefore the only answer to "which of my passports did
// I lend to this connection?", so it is asserted by CONTENT — a count would pass
// with the wrong passport id, the requested scopes, or the wrong actor in it.
func TestApproveAuditsWhichPassportWasLent(t *testing.T) {
	o := setupOAuth(t)
	ctx := context.Background()
	lent := o.mintPassport(t, "lendable", []string{"read", "write", "send"})

	// The request is deliberately narrower than the passport, so the audited
	// scopes can only be the authority actually handed over: auditing the
	// request would read "read", the passport reads "read write send".
	code := o.approveWithPassport(t, url.Values{"scope": {"read"}}, lent)

	// The human whose authority was lent, derived from the row the flow itself
	// wrote rather than restated — the audit actor must be that same human.
	var human string
	if err := o.owner.QueryRow(ctx,
		`SELECT on_behalf_of FROM passport WHERE id = $1`, lent).Scan(&human); err != nil {
		t.Fatalf("reading the lent passport's human: %v", err)
	}
	// One consent, one row: counted separately because the QueryRow below would
	// silently take the first of several.
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'oauth_authorization_code'`)

	var (
		action, actorType, actorID, entityID string
		afterJSON                            []byte
	)
	if err := o.owner.QueryRow(ctx, `
		SELECT action, actor_type, actor_id, entity_id, after
		FROM audit_log WHERE entity_type = 'oauth_authorization_code'`).
		Scan(&action, &actorType, &actorID, &entityID, &afterJSON); err != nil {
		t.Fatalf("reading the lend's audit row: %v", err)
	}
	if action != "create" || actorType != "human" || actorID != "human:"+human {
		t.Fatalf("audit row = action %q actor %s/%s, want create by human:%s — the actor is stamped from the authenticated principal",
			action, actorType, actorID, human)
	}
	// It hangs off the code the consent produced, which is what makes the two
	// rows one fact rather than two coincidences.
	var codeID string
	if err := o.owner.QueryRow(ctx, `SELECT id FROM oauth_authorization_code`).Scan(&codeID); err != nil {
		t.Fatalf("reading the authorization code row: %v", err)
	}
	if entityID != codeID {
		t.Fatalf("audit entity_id = %q, want the code row %q", entityID, codeID)
	}

	var after lendAudit
	if err := json.Unmarshal(afterJSON, &after); err != nil {
		t.Fatalf("decoding the audit after image %s: %v", afterJSON, err)
	}
	if after.PassportID != lent {
		t.Fatalf("audited passport_id = %q, want the lent passport %q", after.PassportID, lent)
	}
	if after.ClientID != o.clientID {
		t.Fatalf("audited client_id = %q, want %q", after.ClientID, o.clientID)
	}
	// The authority actually handed over, which is the lent passport's own —
	// never the client's narrower request.
	if !slices.Equal(after.Scopes, []string{"read", "write", "send"}) {
		t.Fatalf("audited scopes = %v, want [read write send] — the lent passport's own, not the client's request",
			after.Scopes)
	}
	if after.RefreshAllowed {
		t.Fatal("audited refresh_allowed is true although no renewal was requested")
	}
	// The courier itself is never written down — only the hash it becomes.
	assertOwnerCount(t, o, 0,
		`SELECT count(*) FROM audit_log WHERE after::text LIKE '%' || $1 || '%'`, code)
}

// A passport the human may not lend cannot be lent, even by a hand-made POST:
// the list was rendered seconds ago and the check must be re-run (I2).
//
// The human is one selection away from a working consent, so the refusal goes
// back to the screen carrying the marker AND the armed nonce — a recoverable
// refusal, not JSON and not a dead end — asserted for both shapes the check
// refuses, a passport that is no longer selectable and a POST that named none at
// all. That the flow actually survives it is
// TestAnUnlendablePassportLeavesTheHumanASecondChoice's subject.
func TestApproveRefusesAnUnlendablePassport(t *testing.T) {
	o := setupOAuth(t)
	revoked := o.mintPassport(t, "revoked", []string{"read"})
	o.revokePassport(t, revoked)

	status, location, armed := o.approveRefused(t, url.Values{"scope": {"read"}}, revoked)

	if got := consentScreenRetry(t, status, location, armed); got != "unlendable_passport" {
		t.Fatalf("error = %q, want unlendable_passport: %q", got, location)
	}
	// A POST naming no passport at all is the same refusal: there is nothing to
	// lend either way, and the screen has to ask again for the same reason.
	status, location, armed = o.approveRefused(t, url.Values{"scope": {"read"}}, "")
	if got := consentScreenRetry(t, status, location, armed); got != "unlendable_passport" {
		t.Fatalf("error = %q for a POST naming no passport, want unlendable_passport: %q", got, location)
	}
	// The refusal has to come BEFORE anything durable exists. The code row and
	// the audit row naming the lend are the two a consent POST can write, so both
	// must be absent — a lend check that ran after the code was minted would
	// leave a row granting authority for a passport that may not be lent at all,
	// and the pair being absent TOGETHER is what makes them one transaction
	// rather than two writes that usually both happen.
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_authorization_code`)
	assertOwnerCount(t, o, 0,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'oauth_authorization_code'`)
}

// The boundary of the lend's atomic re-check, stated as behaviour rather than
// left to a comment. The consent commits the code and the lent passport's row
// lock together, so a revocation racing the POST cannot produce a code
// (identity's oauth_lend_lock_integration_test.go). Once the code EXISTS the
// question is different: nothing on oauth_authorization_code names the passport,
// so the exchange has nothing to re-check and the code redeems for its five
// minutes.
//
// That is deliberate, and this test is where it is deliberate: the connection's
// credential is a NEW grant-bound passport, and revoking the lent one is not the
// switch that ends connections derived from it — ending a connection goes through
// its grant. What the exchange DOES re-check is the human. So the day someone
// records the lent passport on the code row, this test is what tells them they are
// changing the meaning of a lend, not fixing a race.
func TestALentPassportRevokedAfterConsentStillRedeems(t *testing.T) {
	o := setupOAuth(t)
	lent := o.mintPassport(t, "revoked-after-consent", []string{"read", "write"})
	code := o.approveWithPassport(t, url.Values{"scope": {"read"}}, lent)

	o.revokePassport(t, lent)

	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v, want 200: the code carries no reference to the lent passport, so revoking it after consent cannot reach this exchange",
			status, body)
	}
	minted, _ := body["access_token"].(string)
	if !o.accessTokenWorks(t, minted) {
		t.Fatal("the connection's own credential has no authority although the exchange succeeded")
	}
	// The scopes are still the ones the human approved when the passport was
	// alive: a dead template is not a narrower one.
	if scope, _ := body["scope"].(string); scope != "read write" {
		t.Fatalf("granted scope = %q, want %q", scope, "read write")
	}
	// And the credential the human killed stayed killed — the connection did not
	// resurrect it, it was issued its own.
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM passport WHERE id = $1 AND revoked_at IS NOT NULL`, lent)
}

// The HUMAN is the other half of that boundary, and it falls the other way.
// Deactivation is the switch an admin reaches for when someone's authority must
// end, and the test above is exactly why a pending code would otherwise slip
// past it: the code holds the lent scopes and the human's id, and while they are
// deactivated the exchange refuses on the human alone. So the gap only opens on
// the way back — a code minted minutes before the deactivation, redeemed after a
// reactivation, would build a whole connection on a consent given under
// authority that was taken away in between, with nobody consenting again.
//
// That is the same thing DeactivateUser's grant cascade exists to stop, and the
// reactivation is what makes this test prove it: without one the exchange
// refuses on the live-human check whatever the code row says, and the assertion
// would pass against a deactivation that touched no code at all.
func TestAPendingConsentDoesNotSurviveItsHumansDeactivation(t *testing.T) {
	o := setupOAuth(t)
	lent := o.mintPassport(t, "pending-at-deactivation", []string{"read", "write"})
	code := o.approveWithPassport(t, url.Values{"scope": {"read"}}, lent)

	// The consenting human is the bootstrap admin, and the last active admin may
	// not be deactivated — the organization would lose user administration with
	// no way back. A second admin is what the guard is protecting against, so
	// inviting one is what lets the real endpoint run.
	var second userWire
	if status := o.call(t, "POST", "/v1/users", anyMap{
		"email": "second-admin@fable.test", "display_name": "Second Admin", "role": "admin",
	}, nil, &second); status != http.StatusCreated {
		t.Fatalf("inviting a second admin → %d", status)
	}
	granter := o.userIDByEmail(t, "granter@fable.test")
	if status := o.call(t, "POST", "/v1/users/"+granter+"/deactivate", anyMap{
		"reason": "left the company",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("deactivate → %d", status)
	}

	// Reactivated the way ReactivateUser does — it flips this one column and
	// restores nothing else. Driven over SQL rather than the endpoint because
	// the deactivation just revoked the only session this suite can call with,
	// and whether an admin can log in is not what this test is about.
	if _, err := o.owner.Exec(context.Background(),
		`UPDATE app_user SET status = 'active' WHERE id = $1`, granter); err != nil {
		t.Fatalf("reactivating the human: %v", err)
	}

	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("token → %d %v, want 400 invalid_grant: the consent behind this code ended when its human was deactivated",
			status, body)
	}
	// Refused because the code's own window was closed, not because something
	// downstream happened to catch it — and refused without spending the row, so
	// the refusal reads as "this code was never valid again" rather than as a
	// redemption that failed late.
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM oauth_authorization_code
		   WHERE user_id = $1 AND consumed_at IS NULL AND expires_at <= now()`, granter)
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_grant`)
	assertOwnerCount(t, o, 0,
		`SELECT count(*) FROM passport WHERE oauth_grant_id IS NOT NULL`)
}

// userIDByEmail resolves a member the suite created through the API but whose id
// it never saw, read as the owner because the acting session may already be gone
// by the time the assertion needs it.
func (o *oauthEnv) userIDByEmail(t *testing.T, email string) string {
	t.Helper()
	var id string
	if err := o.owner.QueryRow(context.Background(),
		`SELECT id FROM app_user WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("resolving %s: %v", email, err)
	}
	return id
}

// Deny is a first-class answer: the client is TOLD, per RFC 6749 §4.1.2.1,
// rather than left hanging on a closed tab.
func TestDenyRedirectsToTheClientWithAccessDenied(t *testing.T) {
	o := setupOAuth(t)
	o.mintPassport(t, "unused", []string{"read"})

	status, location := o.denyRaw(t, url.Values{"scope": {"read"}})

	if status != http.StatusFound {
		t.Fatalf("deny → %d, want 302", status)
	}
	if !strings.HasPrefix(location, oauthRedirect) {
		t.Fatalf("Location = %q, want the client's redirect_uri", location)
	}
	if !strings.Contains(location, "error=access_denied") {
		t.Fatalf("Location = %q must carry error=access_denied", location)
	}
	// state is echoed or the client cannot correlate the refusal with its request.
	if !strings.Contains(location, "state=night-state") {
		t.Fatalf("Location = %q must echo state", location)
	}
	// A refusal is not a quiet approval: the redirect carries no code, and no
	// code row was written for one to be drawn from later. Nothing was granted,
	// so there is deliberately no lend to audit either.
	if strings.Contains(location, "code=") {
		t.Fatalf("Location = %q carries a code although the human refused", location)
	}
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_authorization_code`)
	assertOwnerCount(t, o, 0,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'oauth_authorization_code'`)
}

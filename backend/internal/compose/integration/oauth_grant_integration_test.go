// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

// What the code exchange leaves behind when a connector asked to stay
// connected (§5.4): a grant that can revoke the whole connection, its first
// refresh token stored as a hash, and a consent flow that carried the renewal
// request to the human before any of it was written. Without offline_access none of it
// exists — a client must not be handed a long-lived credential it never asked
// to store.

// A connector that asked for offline_access leaves the exchange holding a
// refresh token AND a grant that can revoke the whole connection — the two
// facts the passport alone could never carry. The grant's scopes are
// passport scopes: offline_access is a property of the grant, never an
// authority over a record, so it must not survive into the array every
// RBAC bind reads.
func TestCodeExchangeIssuesAGrantAndItsFirstRefreshToken(t *testing.T) {
	o := setupOAuth(t)

	code := o.authorize(t, url.Values{"scope": {"read write offline_access"}})
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v", status, body)
	}
	refresh, _ := body["refresh_token"].(string)
	if refresh == "" {
		t.Fatalf("offline_access exchange returned no refresh_token: %v", body)
	}
	if ttl, _ := body["refresh_expires_in"].(float64); ttl <= 0 {
		t.Fatalf("refresh_expires_in = %v, want the refresh lifetime in seconds", body["refresh_expires_in"])
	}
	if scope, _ := body["scope"].(string); scope != "read write" {
		t.Fatalf("scope = %q, want the passport scopes without the marker", scope)
	}

	ctx := context.Background()
	// One consent, one grant: the count is asserted separately because
	// QueryRow would silently take the first of several.
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_grant`)
	var (
		grantID        string
		grantScopes    []string
		refreshAllowed bool
		revokedAt      *time.Time
	)
	if err := o.owner.QueryRow(ctx,
		`SELECT id, scopes, refresh_allowed, revoked_at FROM oauth_grant`).
		Scan(&grantID, &grantScopes, &refreshAllowed, &revokedAt); err != nil {
		t.Fatalf("reading the grant: %v", err)
	}
	if !refreshAllowed || revokedAt != nil {
		t.Fatalf("grant refresh_allowed=%v revoked_at=%v, want a live refreshable grant", refreshAllowed, revokedAt)
	}
	if slices.Contains(grantScopes, "offline_access") {
		t.Fatalf("grant scopes = %v, want passport scopes only", grantScopes)
	}

	// The passport the client calls with points at that grant, so revoking
	// the connection reaches the credential.
	var stamped string
	if err := o.owner.QueryRow(ctx,
		`SELECT oauth_grant_id FROM passport WHERE token_hash = $1`,
		sha256Hex(body["access_token"].(string))).Scan(&stamped); err != nil {
		t.Fatalf("reading the minted passport's grant: %v", err)
	}
	if stamped != grantID {
		t.Fatalf("passport.oauth_grant_id = %q, want the grant %q", stamped, grantID)
	}

	// Only the hash is stored, under that grant, unconsumed and unreplaced.
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_refresh_token`)
	var (
		tokenHash  string
		consumedAt *time.Time
		replacedBy *string
		expiresAt  time.Time
	)
	if err := o.owner.QueryRow(ctx,
		`SELECT token_hash, consumed_at, replaced_by, expires_at FROM oauth_refresh_token WHERE grant_id = $1`,
		grantID).Scan(&tokenHash, &consumedAt, &replacedBy, &expiresAt); err != nil {
		t.Fatalf("reading the refresh row under the grant: %v", err)
	}
	if tokenHash != sha256Hex(refresh) || consumedAt != nil || replacedBy != nil {
		t.Fatalf("refresh row = hash %q consumed %v replaced %v, want the hash of the returned token, fresh",
			tokenHash, consumedAt, replacedBy)
	}
	if !expiresAt.After(time.Now().Add(80 * 24 * time.Hour)) {
		t.Fatalf("refresh expires_at = %s, want the 90-day lifetime", expiresAt)
	}

	// The consent is audited as its own fact, not folded into the passport's.
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM audit_log
		 WHERE entity_type = 'oauth_grant' AND action = 'create' AND entity_id = $1`, grantID)
}

// The exchange writes an audit row asserting the human approved a renewable
// connection, so the screen they approved has to disclose it — offline_access
// is not a passport scope and so appears in no scope list of its own.
// A code authorized while the human was live must not redeem into a connection
// after that human is deactivated. Per-call re-auth is not enough on its own:
// the grant would outlive the deactivation dormant, and reactivating the human
// would restore a connector nobody re-approved. So the redemption is refused
// and NO grant row is written — and the refusal is the same sentence a spent
// code gets, because an unauthenticated caller may not probe account state.
func TestCodeExchangeAfterTheHumanIsDeactivatedIssuesNoGrant(t *testing.T) {
	o := setupOAuth(t)
	ctx := context.Background()

	code := o.authorize(t, url.Values{"scope": {"read write offline_access"}})

	// Every member, so the assertion does not depend on WHICH fixture user the
	// consent screen bound the code to.
	if _, err := o.owner.Exec(ctx,
		`UPDATE app_user SET status = 'deactivated' WHERE status = 'active'`); err != nil {
		t.Fatalf("deactivating the consenting members: %v", err)
	}

	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusBadRequest {
		t.Fatalf("token after deactivation → %d %v, want 400", status, body)
	}
	if got, _ := body["error"].(string); got != "invalid_grant" {
		t.Fatalf("error = %q, want invalid_grant", got)
	}
	// The wire answer must not separate "deactivated human" from "spent code".
	if got, _ := body["error_description"].(string); got != "code is unknown, expired, or already used" {
		t.Fatalf("error_description = %q, want the spent-code sentence verbatim", got)
	}
	if _, ok := body["access_token"]; ok {
		t.Fatalf("a refused exchange returned a token: %v", body)
	}
	// The whole point: no durable consent was recorded, so reactivating the
	// human cannot bring a connection back.
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_grant`)
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_refresh_token`)
}

// The renewal request has to survive the hand-off to the consent screen, which
// is the server's whole remaining share of disclosing it: the screen can only
// tell the human about a self-renewing connection if the redirect carries
// offline_access, and the consent POST only re-derives the Offline marker from
// the scope it sends back. It must equally never claim a renewal nobody asked
// for. (That the screen then shows it apart from the scope list — never as an
// item in it, where a human reads it as a permission over records rather than
// over the connection's lifetime — is asserted by the screen's own test.)
func TestAuthorizeCarriesTheRenewalRequestToTheConsentScreen(t *testing.T) {
	o := setupOAuth(t)

	status, location, body, _ := o.authorizeRawFollow(t, url.Values{"scope": {"read offline_access"}})
	if status != http.StatusFound {
		t.Fatalf("authorize → %d %s, want 302", status, body)
	}
	if got := consentFragment(t, location).Get("scope"); got != "read offline_access" {
		t.Fatalf("fragment scope = %q, want %q: dropping it drops the client's refresh request in silence", got, "read offline_access")
	}

	// A request that did not ask to stay connected must not claim it did.
	status, location, body, _ = o.authorizeRawFollow(t, url.Values{"scope": {"read"}})
	if status != http.StatusFound {
		t.Fatalf("authorize → %d %s, want 302", status, body)
	}
	if got := consentFragment(t, location).Get("scope"); got != "read" {
		t.Fatalf("fragment scope = %q, want %q: no renewal was requested", got, "read")
	}
}

// Without offline_access there is no refresh credential to hand back: a
// client must not be given a long-lived token it never asked to store.
func TestCodeExchangeWithoutOfflineAccessReturnsNoRefreshToken(t *testing.T) {
	o := setupOAuth(t)

	code := o.authorize(t, nil)
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("token → %d %v", status, body)
	}
	if _, present := body["refresh_token"]; present {
		t.Fatalf("token response carries a refresh_token without offline_access: %v", body)
	}
	if _, present := body["refresh_expires_in"]; present {
		t.Fatalf("token response carries refresh_expires_in without offline_access: %v", body)
	}

	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_grant`)
	var refreshAllowed bool
	if err := o.owner.QueryRow(context.Background(),
		`SELECT refresh_allowed FROM oauth_grant`).Scan(&refreshAllowed); err != nil {
		t.Fatalf("reading the grant: %v", err)
	}
	if refreshAllowed {
		t.Fatal("grant allows refresh although offline_access was never requested")
	}
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_refresh_token`)
}

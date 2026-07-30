// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Refresh-token rotation, the two properties a connector's whole lifetime
// rests on: every presentation of a token is serialized by a row lock, so
// concurrent renewals cannot mint two successors; and the replay rule tells a
// lost response apart from a stolen token — the first must leave a healthy
// connection working, the second must kill the grant, the chain and every
// passport under it.
//
// These are integration tests because both properties are properties of a
// real transaction: the lock, and what is still committed after a refusal.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// connect drives a full handshake for a connection that asked to stay
// connected, and returns the two credentials the client is left holding.
func (o *oauthEnv) connect(t *testing.T, scope string) (accessToken, refreshToken string) {
	t.Helper()
	code := o.authorize(t, url.Values{"scope": {scope + " offline_access"}})
	status, body := o.exchange(t, url.Values{"code": {code}})
	if status != http.StatusOK {
		t.Fatalf("code exchange → %d %v", status, body)
	}
	accessToken, _ = body["access_token"].(string)
	refreshToken, _ = body["refresh_token"].(string)
	if accessToken == "" || refreshToken == "" {
		t.Fatalf("handshake left the client without credentials: %v", body)
	}
	return accessToken, refreshToken
}

// renew presents a refresh token the way a connector does when its access
// token nears expiry.
func (o *oauthEnv) renew(t *testing.T, refreshToken string, extra url.Values) (int, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {o.clientID},
		"refresh_token": {refreshToken},
	}
	for k, vs := range extra {
		form[k] = vs
	}
	return o.postToken(t, form)
}

// renewal is one presentation's outcome carried back to the test goroutine.
// The concurrency case cannot use renew: only the goroutine running the test
// may fail it, so a presentation fired in parallel returns its error instead
// of calling t.
type renewal struct {
	status int
	body   string
	err    error
}

// presentInParallel fires n simultaneous presentations of the SAME refresh
// token — the race that a read-then-write rotation loses by minting a
// successor per presentation.
func (o *oauthEnv) presentInParallel(refreshToken string, n int) []renewal {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {o.clientID},
		"refresh_token": {refreshToken},
	}.Encode()

	out := make([]renewal, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			out[i] = o.present(form)
		}(i)
	}
	close(release)
	wg.Wait()
	return out
}

// present posts one already-encoded token-endpoint form, reporting failures
// as values so it is safe to call off the test goroutine.
func (o *oauthEnv) present(form string) renewal {
	req, err := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/token", strings.NewReader(form))
	if err != nil {
		return renewal{err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return renewal{err: err}
	}
	raw, err := io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	return renewal{status: resp.StatusCode, body: string(raw), err: err}
}

// refuseRenewal presents a refresh token expecting invalid_grant — the ONE
// error code every refresh failure answers, because any other code leaves a
// client retrying a dead token instead of asking the human again.
func (o *oauthEnv) refuseRenewal(t *testing.T, refreshToken string, extra url.Values) {
	t.Helper()
	status, body := o.renew(t, refreshToken, extra)
	if status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("renewal → %d %v, want 400 invalid_grant", status, body)
	}
}

// grantRevoked reports whether the installation's single grant is dead. Each
// test bootstraps its own database, so there is exactly one — asserted, since
// QueryRow would silently take the first of several.
func (o *oauthEnv) grantRevoked(t *testing.T) bool {
	t.Helper()
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_grant`)
	var revokedAt *time.Time
	if err := o.owner.QueryRow(context.Background(),
		`SELECT revoked_at FROM oauth_grant`).Scan(&revokedAt); err != nil {
		t.Fatalf("reading the grant: %v", err)
	}
	return revokedAt != nil
}

// assertChainLinked checks the store's side of one rotation: the presented
// row is spent and links FORWARD to the successor, whose own lifetime starts
// again from the rotation. That link is what later tells a lost-response retry
// apart from genuine reuse, and the sliding lifetime is why a connection that
// keeps renewing never has to bring the human back.
func (o *oauthEnv) assertChainLinked(t *testing.T, presented, successor string) {
	t.Helper()
	ctx := context.Background()
	var (
		consumedAt *time.Time
		replacedBy *string
	)
	if err := o.owner.QueryRow(ctx,
		`SELECT consumed_at, replaced_by FROM oauth_refresh_token WHERE token_hash = $1`,
		sha256Hex(presented)).Scan(&consumedAt, &replacedBy); err != nil {
		t.Fatalf("reading the presented refresh row: %v", err)
	}
	if consumedAt == nil || replacedBy == nil {
		t.Fatalf("presented row consumed=%v replaced_by=%v, want spent and linked forward", consumedAt, replacedBy)
	}
	var (
		successorID string
		expiresAt   time.Time
	)
	if err := o.owner.QueryRow(ctx,
		`SELECT id, expires_at FROM oauth_refresh_token WHERE token_hash = $1`,
		sha256Hex(successor)).Scan(&successorID, &expiresAt); err != nil {
		t.Fatalf("reading the successor refresh row: %v", err)
	}
	if successorID != *replacedBy {
		t.Fatalf("replaced_by = %q, want the successor row %q", *replacedBy, successorID)
	}
	if !expiresAt.After(time.Now().Add(80 * 24 * time.Hour)) {
		t.Fatalf("successor expires_at = %s, want the refresh lifetime measured from the rotation", expiresAt)
	}
}

// accessTokenWorks asks the resource surface whether a passport still has
// authority — the only question a connector's user actually cares about.
func (o *oauthEnv) accessTokenWorks(t *testing.T, accessToken string) bool {
	t.Helper()
	status := o.call(t, "GET", "/v1/people", nil, map[string]string{"Authorization": "Bearer " + accessToken}, nil)
	switch status {
	case http.StatusOK:
		return true
	case http.StatusUnauthorized:
		return false
	default:
		t.Fatalf("GET /v1/people with the access token → %d, want 200 or 401", status)
		return false
	}
}

// A rotation hands back a fresh pair and leaves the connector holding exactly
// one live passport: the predecessor dies with the token that minted it, so a
// leaked older access token cannot outlive the renewal that replaced it.
func TestRefreshRotatesAndRevokesThePredecessorPassport(t *testing.T) {
	o := setupOAuth(t)
	firstAccess, firstRefresh := o.connect(t, "read write")
	if !o.accessTokenWorks(t, firstAccess) {
		t.Fatal("the freshly issued access token has no authority")
	}

	// A stranger's client_id does not renew someone else's grant — and, like
	// a wrong PKCE verifier against a code, must not BURN the token for the
	// client it belongs to.
	o.refuseRenewal(t, firstRefresh, url.Values{"client_id": {"not-this-client"}})

	status, body := o.renew(t, firstRefresh, nil)
	if status != http.StatusOK {
		t.Fatalf("renewal → %d %v", status, body)
	}
	secondAccess, _ := body["access_token"].(string)
	secondRefresh, _ := body["refresh_token"].(string)
	if secondAccess == "" || secondRefresh == "" {
		t.Fatalf("rotation returned an incomplete pair: %v", body)
	}
	if secondAccess == firstAccess || secondRefresh == firstRefresh {
		t.Fatalf("rotation re-issued a presented credential: access same=%v refresh same=%v",
			secondAccess == firstAccess, secondRefresh == firstRefresh)
	}
	if scope, _ := body["scope"].(string); scope != "read write" {
		t.Fatalf("scope = %q, want the grant's scopes carried forward", scope)
	}

	o.assertChainLinked(t, firstRefresh, secondRefresh)

	// One consent, one live credential: the predecessor passport is revoked
	// inside the same transaction that minted its replacement.
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM passport WHERE oauth_grant_id IS NOT NULL AND revoked_at IS NULL`)
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM passport WHERE token_hash = $1 AND revoked_at IS NOT NULL`, sha256Hex(firstAccess))
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM passport WHERE token_hash = $1 AND revoked_at IS NULL`, sha256Hex(secondAccess))
	if o.accessTokenWorks(t, firstAccess) {
		t.Fatal("the replaced access token still has authority after the rotation")
	}
	if !o.accessTokenWorks(t, secondAccess) {
		t.Fatal("the rotated access token has no authority")
	}
	if o.grantRevoked(t) {
		t.Fatal("a plain rotation revoked the grant")
	}

	// The death of a credential is a bus fact: a long-lived holder must be
	// able to drop the passport rotation replaced.
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'passport.revoked'`)
}

// The defect this test exists for: a read-then-write rotation mints one
// successor per concurrent presentation, so a client that fires two renewals
// ends up holding two divergent chains and the loser's token silently
// resurrects a connection the human cannot see.
func TestConcurrentRefreshesMintExactlyOneSuccessor(t *testing.T) {
	o := setupOAuth(t)
	_, refresh := o.connect(t, "read write")

	const presentations = 8
	results := o.presentInParallel(refresh, presentations)

	var winners int
	var winner map[string]any
	for i, got := range results {
		if got.err != nil {
			t.Fatalf("presentation %d: %v", i, got.err)
		}
		switch got.status {
		case http.StatusOK:
			winners++
			if err := json.Unmarshal([]byte(got.body), &winner); err != nil {
				t.Fatalf("presentation %d: decoding the accepted renewal: %v", i, err)
			}
		case http.StatusBadRequest:
			if !strings.Contains(got.body, "invalid_grant") {
				t.Fatalf("presentation %d refused as %s, want invalid_grant", i, got.body)
			}
		default:
			t.Fatalf("presentation %d → %d %s, want 200 or 400", i, got.status, got.body)
		}
	}
	if winners != 1 {
		t.Fatalf("%d of %d simultaneous presentations were accepted, want exactly 1", winners, presentations)
	}

	// One successor row, whatever the interleaving: the predecessor plus its
	// single replacement, and only the replacement is spendable.
	assertOwnerCount(t, o, 2, `SELECT count(*) FROM oauth_refresh_token`)
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_refresh_token WHERE consumed_at IS NULL`)
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM passport WHERE oauth_grant_id IS NOT NULL AND revoked_at IS NULL`)

	// Seven simultaneous presentations are not seven thefts: the connection
	// survives, and the winner's token renews again.
	if o.grantRevoked(t) {
		t.Fatal("concurrent presentations of one token were read as reuse and killed the grant")
	}
	next, _ := winner["refresh_token"].(string)
	if next == "" {
		t.Fatalf("the accepted renewal returned no refresh token: %v", winner)
	}
	if status, body := o.renew(t, next, nil); status != http.StatusOK {
		t.Fatalf("renewing with the accepted successor → %d %v", status, body)
	}
}

// Refresh tokens are stored hashed, so a response lost in transit can never
// be replayed — the client retries with the only token it has. Punishing that
// retry as theft would destroy a working connection on the commonest benign
// failure there is.
func TestReplayedRefreshInsideTheGraceWindowDoesNotRevokeTheChain(t *testing.T) {
	o := setupOAuth(t)
	_, firstRefresh := o.connect(t, "read write")

	status, body := o.renew(t, firstRefresh, nil)
	if status != http.StatusOK {
		t.Fatalf("renewal → %d %v", status, body)
	}
	liveAccess, _ := body["access_token"].(string)
	liveRefresh, _ := body["refresh_token"].(string)

	// The retry the lost response provokes.
	o.refuseRenewal(t, firstRefresh, nil)

	if o.grantRevoked(t) {
		t.Fatal("a retry inside the grace window revoked the grant")
	}
	// Nothing was minted and nothing was killed: the client keeps calling
	// with the access token it already has until its own expiry, and the
	// human re-consents at leisure.
	assertOwnerCount(t, o, 2, `SELECT count(*) FROM oauth_refresh_token`)
	assertOwnerCount(t, o, 1, `SELECT count(*) FROM oauth_refresh_token WHERE consumed_at IS NULL`)
	if !o.accessTokenWorks(t, liveAccess) {
		t.Fatal("the live access token lost its authority because a response was lost in transit")
	}
	// And the successor it never received is still the live one, so a client
	// that DID receive it is unaffected.
	if status, body := o.renew(t, liveRefresh, nil); status != http.StatusOK {
		t.Fatalf("renewing with the successor after a retry → %d %v", status, body)
	}
}

// Outside the window, or against a successor already in use, a consumed token
// is theft, not a retry: RFC 9700 says the whole chain dies, and it must
// actually die — grant, every refresh row, every passport under it.
func TestReplayedRefreshOutsideTheWindowRevokesEverything(t *testing.T) {
	o := setupOAuth(t)
	_, firstRefresh := o.connect(t, "read write")

	status, body := o.renew(t, firstRefresh, nil)
	if status != http.StatusOK {
		t.Fatalf("renewal → %d %v", status, body)
	}
	liveAccess, _ := body["access_token"].(string)
	liveRefresh, _ := body["refresh_token"].(string)

	// Age the consumption past the grace window instead of waiting for it:
	// the rule is a comparison against a clock, so moving the timestamp
	// proves the same transition a sleep would.
	if _, err := o.owner.Exec(context.Background(),
		`UPDATE oauth_refresh_token SET consumed_at = now() - interval '5 minutes' WHERE token_hash = $1`,
		sha256Hex(firstRefresh)); err != nil {
		t.Fatalf("ageing the consumption: %v", err)
	}

	o.refuseRenewal(t, firstRefresh, nil)

	if !o.grantRevoked(t) {
		t.Fatal("a stolen refresh token left the grant alive")
	}
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM oauth_refresh_token WHERE consumed_at IS NULL`)
	assertOwnerCount(t, o, 0, `SELECT count(*) FROM passport WHERE oauth_grant_id IS NOT NULL AND revoked_at IS NULL`)
	if o.accessTokenWorks(t, liveAccess) {
		t.Fatal("the access token still works after the connection was revoked for reuse")
	}
	// The token the legitimate client holds is dead too — after theft the
	// human re-consents; there is no way to tell victim from thief.
	o.refuseRenewal(t, liveRefresh, nil)

	// The revoked consent is an audited fact of its own, and each passport's
	// death is on the bus (one for the rotation, one for the cascade).
	assertOwnerCount(t, o, 1,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'oauth_grant' AND action = 'archive'`)
	assertOwnerCount(t, o, 2,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'passport.revoked'`)
}

// A renewal may ask for less than the human approved and never for more: the
// grant is the ceiling, and a request past it is refused without spending the
// token — a client that asks wrongly must not be locked out by its own bug.
func TestRefreshNarrowsScopesButNeverWidensThem(t *testing.T) {
	o := setupOAuth(t)
	_, refresh := o.connect(t, "read write")

	o.refuseRenewal(t, refresh, url.Values{"scope": {"read write send"}})

	status, body := o.renew(t, refresh, url.Values{"scope": {"read"}})
	if status != http.StatusOK {
		t.Fatalf("narrowing renewal → %d %v", status, body)
	}
	if scope, _ := body["scope"].(string); scope != "read" {
		t.Fatalf("scope = %q, want the narrowed scope", scope)
	}
	narrowed, _ := body["access_token"].(string)
	var scopes []string
	if err := o.owner.QueryRow(context.Background(),
		`SELECT scopes FROM passport WHERE token_hash = $1`,
		sha256Hex(narrowed)).Scan(&scopes); err != nil {
		t.Fatalf("reading the rotated passport: %v", err)
	}
	if len(scopes) != 1 || scopes[0] != "read" {
		t.Fatalf("passport scopes = %v, want only the narrowed scope", scopes)
	}

	// The grant still bounds what a later renewal may ask for, so narrowing
	// once is not a one-way ratchet.
	next, _ := body["refresh_token"].(string)
	if status, body := o.renew(t, next, url.Values{"scope": {"read write"}}); status != http.StatusOK {
		t.Fatalf("re-widening within the grant → %d %v", status, body)
	}
}

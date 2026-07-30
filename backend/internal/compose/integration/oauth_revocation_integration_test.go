// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Revocation, from the only place it can honestly be observed: the wire a
// connector calls on. A connection can be cut off four ways — the passport, the
// grant, the client disabled, the client deleted — and each must bind on the
// NEXT call. A passport that outlives its grant or its client is a live
// credential nobody can see: it appears in no connection list, and revoking the
// thing a human believes owns it changes nothing.
//
// Two mechanisms make that true and this suite exercises both without caring
// which one answered: the cascade, which kills the credentials under a grant in
// the transaction that revokes it, and the liveness rule in the agent-auth
// query, which refuses a passport whose connection is dead however it died.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// connectedClient is a connector that finished the handshake: the harness, the
// two credentials the client now holds, and the four ways it can be cut off.
type connectedClient struct {
	*oauthEnv
	access  string
	refresh string
}

// setupConnectedClient drives the full handshake on the connector harness, so
// the credentials under test are the ones a real client is issued rather than
// rows a test wrote.
func setupConnectedClient(t *testing.T) *connectedClient {
	t.Helper()
	o := setupOAuth(t)
	access, refresh := o.connect(t)
	return &connectedClient{oauthEnv: o, access: access, refresh: refresh}
}

// callMCP is the call the client makes on every turn of a conversation. It
// returns the status alone: what matters here is admission, and 401 is the
// answer that sends a client back to the human for consent.
func (c *connectedClient) callMCP(t *testing.T) int {
	t.Helper()
	return c.listTools(t, c.access).StatusCode
}

// refreshSucceeds reports whether the client can still mint itself a fresh
// credential. Asking it after a revocation is the second half of every case
// below: an access token that expires in 30 days is not "cut off" if the
// refresh token beneath it hands out a new one.
func (c *connectedClient) refreshSucceeds(t *testing.T) bool {
	t.Helper()
	status, body := c.renew(t, c.refresh, nil)
	switch {
	case status == http.StatusOK:
		return true
	case status == http.StatusBadRequest && body["error"] == "invalid_grant":
		return false
	default:
		t.Fatalf("renewal → %d %v, want 200 or 400 invalid_grant", status, body)
		return false
	}
}

// revokePassport is the human's kill switch as the Settings screen calls it —
// the one direction with a product surface today.
func (c *connectedClient) revokePassport(t *testing.T) {
	t.Helper()
	var passportID string
	if err := c.owner.QueryRow(context.Background(),
		`SELECT id FROM passport WHERE token_hash = $1`, sha256Hex(c.access)).Scan(&passportID); err != nil {
		t.Fatalf("reading the passport the client holds: %v", err)
	}
	if status := c.call(t, "DELETE", "/v1/passports/"+passportID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE /v1/passports/%s → %d", passportID, status)
	}
}

// The other three directions are cut in the STORE, and deliberately so. Their
// operator surfaces (RFC 7009 revocation, the admin client screen) arrive
// later, and a suite that could only cut a connection through an endpoint would
// prove nothing about the state those endpoints produce. Authentication has to
// fail closed on the row state itself — a grant revoked by a DBA, or by a
// cascade written before a passport table existed, is exactly the row the
// cascade never saw.
func (c *connectedClient) revokeGrant(t *testing.T) {
	t.Helper()
	c.cut(t, `UPDATE oauth_grant SET revoked_at = now() WHERE revoked_at IS NULL`)
}

func (c *connectedClient) disableClient(t *testing.T) {
	t.Helper()
	c.cut(t, `UPDATE oauth_client SET disabled_at = now() WHERE client_id = $1`, c.clientID)
}

// Client delete is a SOFT delete: a hard row delete cannot express "revoke
// every credential under this client first" atomically, and the RESTRICT on
// passport → oauth_grant refuses it while any credential still points there.
func (c *connectedClient) deleteClient(t *testing.T) {
	t.Helper()
	c.cut(t, `UPDATE oauth_client SET deleted_at = now() WHERE client_id = $1`, c.clientID)
}

// sessionlessClient is a client with NO cookie jar, which is the only honest
// way to drive RFC 7009: the caller is a client process handing a credential
// back, not a browser, and SameSite would keep a cookie off that request even
// if one existed. The harness's own client carries the bootstrapped admin
// session, so posting through it exercises the session-authenticated path and
// says nothing about the one every real client takes.
func (o *oauthEnv) sessionlessClient() *http.Client {
	return &http.Client{Transport: o.ts.Client().Transport}
}

// revoke posts one presented token to RFC 7009 and returns the status alone
// — the endpoint answers 200 regardless of what it did, so status is the
// only thing a caller may observe about the outcome directly. It goes through
// a session-less client for the reason above.
func (o *oauthEnv) revoke(t *testing.T, token, tokenTypeHint string) int {
	t.Helper()
	form := url.Values{"token": {token}}
	if tokenTypeHint != "" {
		form.Set("token_type_hint", tokenTypeHint)
	}
	req, err := http.NewRequest(http.MethodPost, o.ts.URL+"/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.sessionlessClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	return resp.StatusCode
}

// cut applies one store-level revocation and insists it hit exactly one row: a
// statement that matched nothing would leave the connection alive and make
// every assertion after it vacuous.
func (c *connectedClient) cut(t *testing.T, statement string, args ...any) {
	t.Helper()
	tag, err := c.owner.Exec(context.Background(), statement, args...)
	if err != nil {
		t.Fatalf("cutting the connection off with %s: %v", statement, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("%s affected %d rows, want exactly 1", statement, tag.RowsAffected())
	}
}

// TestEveryRevocationPathStopsTheCredential covers the four ways a connector
// can be cut off. Each must bind on the NEXT call — a passport that outlives
// its grant or its client is a live credential nobody can see.
func TestEveryRevocationPathStopsTheCredential(t *testing.T) {
	for name, cut := range map[string]func(t *testing.T, c *connectedClient){
		"passport revoked": func(t *testing.T, c *connectedClient) { c.revokePassport(t) },
		"grant revoked":    func(t *testing.T, c *connectedClient) { c.revokeGrant(t) },
		"client disabled":  func(t *testing.T, c *connectedClient) { c.disableClient(t) },
		"client deleted":   func(t *testing.T, c *connectedClient) { c.deleteClient(t) },
	} {
		t.Run(name, func(t *testing.T) {
			c := setupConnectedClient(t)
			if code := c.callMCP(t); code != http.StatusOK {
				t.Fatalf("precondition: connected call → %d", code)
			}
			cut(t, c)
			if code := c.callMCP(t); code != http.StatusUnauthorized {
				t.Fatalf("after %s → %d, want 401", name, code)
			}
			if c.refreshSucceeds(t) {
				t.Fatalf("after %s, refresh still mints a credential", name)
			}
		})
	}
}

// A grant revoked without the cascade running is the case the liveness rule
// exists for, and this test pins it apart from the cascade: the passport row is
// left untouched — unrevoked, unexpired, its token hash still on file — and the
// call is refused anyway. Delete the two LEFT JOINs and this is the test that
// goes red while every cascade test stays green.
func TestARevokedGrantStopsAPassportRowTheCascadeNeverTouched(t *testing.T) {
	c := setupConnectedClient(t)
	c.revokeGrant(t)

	assertOwnerCount(t, c.oauthEnv, 1,
		`SELECT count(*) FROM passport
		  WHERE token_hash = $1 AND revoked_at IS NULL AND now() < expires_at`, sha256Hex(c.access))
	if code := c.callMCP(t); code != http.StatusUnauthorized {
		t.Fatalf("call under a revoked grant → %d, want 401 from the liveness rule alone", code)
	}
	if c.accessTokenWorks(t, c.access) {
		t.Fatal("the same credential still has authority on the REST surface: the liveness rule is on one path only")
	}
}

// Revoking the passport a connector holds ends the CONNECTION, not just that
// one credential: leaving the grant alive would let the client's next renewal
// mint a replacement seconds after the human pressed the button, which is
// indistinguishable from the revocation never happening.
func TestRevokingAConnectorsPassportRevokesTheGrantBeneathIt(t *testing.T) {
	c := setupConnectedClient(t)
	c.revokePassport(t)

	if !c.grantRevoked(t) {
		t.Fatal("the grant survived the revocation of the passport it issued, so refresh can resurrect the connection")
	}
	assertOwnerCount(t, c.oauthEnv, 0, `SELECT count(*) FROM oauth_refresh_token WHERE consumed_at IS NULL`)
	// One death, recorded once: the cascade retires the passport and the
	// deleting caller must not audit or announce the same row a second time.
	assertOwnerCount(t, c.oauthEnv, 1,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'passport' AND action = 'archive'`)
	assertOwnerCount(t, c.oauthEnv, 1,
		`SELECT count(*) FROM event_outbox WHERE envelope->>'type' = 'passport.revoked'`)
	assertOwnerCount(t, c.oauthEnv, 1,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'oauth_grant' AND action = 'archive'`)
}

// The liveness rule is scoped to OAuth-issued credentials. A locally minted
// passport (the A1 path) answers to no grant and no client, so a dead
// connector says nothing about it — and the whole local surface would go dark
// if the joins were read as a requirement rather than a condition.
func TestALocallyMintedPassportIsUnaffectedByADeadConnector(t *testing.T) {
	c := setupConnectedClient(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := c.call(t, "POST", "/v1/passports", anyMap{
		"label": "local agent", "scopes": []string{"read"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue a local passport → %d", status)
	}

	c.disableClient(t)
	c.revokeGrant(t)

	if code := c.listTools(t, minted.Token).StatusCode; code != http.StatusOK {
		t.Fatalf("locally minted passport → %d, want 200: it answers to no grant", code)
	}
	if code := c.callMCP(t); code != http.StatusUnauthorized {
		t.Fatalf("the connector's own credential → %d, want 401", code)
	}
}

// RFC 7009 revocation (POST /oauth/revoke) is the fifth way a connection
// ends, and the first with a client-initiated operator surface: a connector
// handing back either half of its credential pair, on its own side, without
// the human ever opening Settings. It reaches the same cascade as the other
// four, so it is proven the same way — the next call is refused and refresh
// cannot resurrect it.

// sessionlessGet issues one GET through the session-less client and returns
// the status. It exists to PROVE that client has no session before anything is
// concluded from what the same client gets away with elsewhere.
func (o *oauthEnv) sessionlessGet(t *testing.T, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, o.ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := o.sessionlessClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, resp)
	return resp.StatusCode
}

// Reachability is a separate property from behaviour, and it is the one this
// endpoint is most easily robbed of: /oauth/ is mounted behind the same
// session middleware /v1 sits behind, so a missing public-path exemption
// answers every real client 401 while discovery goes on advertising
// revocation_endpoint. The credential a client hands back IS its
// authentication here; there is no cookie in a client process to send.
func TestRFC7009RevocationIsReachableWithoutASession(t *testing.T) {
	c := setupConnectedClient(t)

	// The client the revocation goes through genuinely carries no session:
	// without this, a 200 below could be coming from the harness's admin
	// cookie rather than from the exemption under test.
	if status := c.sessionlessGet(t, "/v1/people"); status != http.StatusUnauthorized {
		t.Fatalf("the session-less client reached /v1/people → %d, want 401: it holds a session, so nothing below would be evidence", status)
	}

	if status := c.revoke(t, c.access, "access_token"); status != http.StatusOK {
		t.Fatalf("session-less revocation → %d, want 200: every client discovery advertises this endpoint to has no cookie to send", status)
	}
	if code := c.callMCP(t); code != http.StatusUnauthorized {
		t.Fatalf("after a session-less revocation → %d, want 401", code)
	}
	if c.refreshSucceeds(t) {
		t.Fatal("after a session-less revocation, refresh still mints a credential")
	}
}

// Presenting the access token is the shape a client uses when a human
// disconnects from ITS side: the whole connection dies, not just that one
// credential, so a racing refresh cannot mint a replacement seconds later.
func TestRevokingTheAccessTokenEndsTheWholeConnection(t *testing.T) {
	c := setupConnectedClient(t)
	if code := c.callMCP(t); code != http.StatusOK {
		t.Fatalf("precondition: connected call → %d", code)
	}

	if status := c.revoke(t, c.access, "access_token"); status != http.StatusOK {
		t.Fatalf("revoke → %d, want 200", status)
	}

	if code := c.callMCP(t); code != http.StatusUnauthorized {
		t.Fatalf("after revoking the access token → %d, want 401", code)
	}
	if c.refreshSucceeds(t) {
		t.Fatal("after revoking the access token, refresh still mints a credential")
	}
}

// Presenting the refresh token reaches exactly the same cascade: RFC 7009
// treats both halves of a connection's credential pair as one namespace, so
// a client that only ever touches its refresh token still ends the whole
// connection, not just that one row.
func TestRevokingTheRefreshTokenEndsTheWholeConnection(t *testing.T) {
	c := setupConnectedClient(t)
	if code := c.callMCP(t); code != http.StatusOK {
		t.Fatalf("precondition: connected call → %d", code)
	}

	if status := c.revoke(t, c.refresh, "refresh_token"); status != http.StatusOK {
		t.Fatalf("revoke → %d, want 200", status)
	}

	if code := c.callMCP(t); code != http.StatusUnauthorized {
		t.Fatalf("after revoking the refresh token → %d, want 401", code)
	}
	if c.refreshSucceeds(t) {
		t.Fatal("after revoking the refresh token, refresh still mints a credential")
	}
}

// RFC 7009's non-disclosure rule: an unknown token must answer exactly like a
// successful revocation, or the endpoint becomes an oracle for whether a
// token string is real. The live connection is asserted untouched, not just
// the status code — a 200 that happened to also kill the wrong grant would
// pass a status-only check and fail every client relying on this endpoint.
func TestRevokingAnUnknownTokenStillAnswersSuccess(t *testing.T) {
	c := setupConnectedClient(t)

	if status := c.revoke(t, "mgp_this-token-was-never-issued", "access_token"); status != http.StatusOK {
		t.Fatalf("revoke of an unknown token → %d, want 200", status)
	}

	if c.grantRevoked(t) {
		t.Fatal("revoking an unknown token revoked the live connection's grant")
	}
	if code := c.callMCP(t); code != http.StatusOK {
		t.Fatalf("after revoking an unknown token, the live connection → %d, want 200", code)
	}
}

// A client that cannot see revocation_endpoint in discovery will never call
// it: RFC 7009 is opt-in advertisement, not a well-known path a client is
// expected to guess.
func TestDiscoveryAdvertisesTheRevocationEndpoint(t *testing.T) {
	o := setupOAuth(t)

	var metadata struct {
		RevocationEndpoint string `json:"revocation_endpoint"`
	}
	if status := o.call(t, "GET", "/.well-known/oauth-authorization-server", nil, nil, &metadata); status != http.StatusOK {
		t.Fatalf("discovery → %d", status)
	}
	if !strings.HasSuffix(metadata.RevocationEndpoint, "/oauth/revoke") {
		t.Fatalf("revocation_endpoint = %q, want it to end in /oauth/revoke", metadata.RevocationEndpoint)
	}
}

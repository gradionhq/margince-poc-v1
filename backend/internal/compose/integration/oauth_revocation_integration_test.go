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
	access, refresh := o.connect(t, "read write")
	return &connectedClient{oauthEnv: o, access: access, refresh: refresh}
}

// callMCP is the call the client makes on every turn of a conversation. It
// returns the status alone: what matters here is admission, and 401 is the
// answer that sends a client back to the human for consent.
func (c *connectedClient) callMCP(t *testing.T) int {
	t.Helper()
	return c.post(t, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, c.access).StatusCode
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

	if code := c.post(t, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, minted.Token).StatusCode; code != http.StatusOK {
		t.Fatalf("locally minted passport → %d, want 200: it answers to no grant", code)
	}
	if code := c.callMCP(t); code != http.StatusUnauthorized {
		t.Fatalf("the connector's own credential → %d, want 401", code)
	}
}

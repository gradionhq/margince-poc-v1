// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The consent screen's read model (GET /oauth/consent-request): which of the
// signed-in human's passports may be lent to the requesting client. Each
// exclusion the query enforces — own passports only, alive, unbound, and
// overlapping the request — is asserted separately, so a query that dropped
// one filter would still fail a test that only counted rows.

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

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
	ClientName string   `json:"client_name"`
	Requested  []string `json:"requested"`
	Offline    bool     `json:"offline"`
	Passports  []struct {
		ID      string   `json:"id"`
		Label   string   `json:"label"`
		Scopes  []string `json:"scopes"`
		Granted []string `json:"granted"`
	} `json:"passports"`
}

// A passport is lendable only if it is THIS human's, still alive, not already
// bound to a connection, and overlaps what the client asked for. Each exclusion
// is asserted separately: a query that dropped one filter would still pass a
// test that only counted rows.
func TestSelectablePassportsExcludesEveryUnlendableShape(t *testing.T) {
	o := setupOAuth(t)
	ctx := context.Background()

	lendable := o.mintPassport(t, "lendable", []string{"read", "write"})
	o.mintPassport(t, "no-overlap", []string{"enrich"})
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
	// granted is the INTERSECTION, not the passport's own scopes.
	if got := got.Passports[0].Granted; !slices.Equal(got, []string{"read", "write"}) {
		t.Fatalf("granted = %v, want [read write]", got)
	}
	_ = lendable
}

// A passport whose scopes exceed the request lends only the overlap: a client
// must never receive authority it did not ask for (I1).
func TestSelectablePassportsNarrowsToTheRequest(t *testing.T) {
	o := setupOAuth(t)
	o.mintPassport(t, "broad", []string{"read", "write", "send"})

	got := o.consentRequest(t, "read")

	if len(got.Passports) != 1 {
		t.Fatalf("passports = %d, want 1", len(got.Passports))
	}
	if granted := got.Passports[0].Granted; !slices.Equal(granted, []string{"read"}) {
		t.Fatalf("granted = %v, want only [read] — the client asked for no more", granted)
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

	t.Run("off", func(t *testing.T) {
		e := setup(t)
		e.bootstrapWorkspace(t)
		registerClientDirectly(t, e, clientID)

		status := e.call(t, "GET", "/v1/oauth/consent-request?client_id="+clientID+"&scope=read", nil, nil, nil)
		if status != http.StatusNotFound {
			t.Fatalf("consent-request for a live client, connector off → %d, want 404", status)
		}
	})

	t.Run("on", func(t *testing.T) {
		c := setupConnector(t)
		registerClientDirectly(t, c.env, clientID)

		status := c.call(t, "GET", "/v1/oauth/consent-request?client_id="+clientID+"&scope=read", nil, nil, nil)
		if status != http.StatusOK {
			t.Fatalf("consent-request for the same live client, connector on → %d, want 200", status)
		}
	})
}

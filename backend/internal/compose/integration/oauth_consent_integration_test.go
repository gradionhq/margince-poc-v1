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

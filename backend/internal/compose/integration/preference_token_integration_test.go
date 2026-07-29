// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The preference token's authority, proven at the two seams that decide who
// can hold one. The token is a bearer credential over ONE person's consent
// record — on the anonymous public edge it reads their per-purpose state,
// withdraws, and grants, with no session at all — so the send path's mint
// carries the same row-scope gate the authenticated read does: a seat that
// cannot read the recipient cannot obtain their token.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seedRecipient creates a person with one email address, owned by the given
// user, so the send path's email→person resolve can find them.
func seedRecipient(t *testing.T, e *Env, name, email string, owner *ids.UUID) {
	t.Helper()
	if _, err := e.People.CreatePerson(e.Admin(), people.CreatePersonInput{
		FullName: name, OwnerID: userIDPtr(owner), Source: "manual",
		Emails: []people.PersonEmailInput{{Email: email, EmailType: "work", IsPrimary: true}},
	}); err != nil {
		t.Fatalf("seeding %s: %v", name, err)
	}
}

// livePreferenceTokens counts the workspace's minted tokens. preference_token
// is deliberately outside RLS (it IS the token→tenant resolver), so the app
// pool reads it directly here — the assertion is that the refused mint wrote
// NOTHING, which a scoped read could not distinguish from a hidden row.
func livePreferenceTokens(t *testing.T, e *Env) int {
	t.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM preference_token WHERE workspace_id = $1`, e.WS).Scan(&n); err != nil {
		t.Fatalf("counting preference tokens: %v", err)
	}
	return n
}

// A seat whose row scope does not reach the recipient is refused the mint,
// and the refusal is the row-scope answer (404, existence-hiding) rather
// than a silent "no unsubscribe surface" — answering that would transmit
// marketing mail with no working List-Unsubscribe URL.
func TestPreferenceTokenMintRefusesAnInvisibleRecipient(t *testing.T) {
	e := Setup(t)
	store := consent.NewStore(e.Pool)

	seedRecipient(t, e, "Foreign Recipient", "foreign@recipient.test", &e.Rep2)
	seedRecipient(t, e, "Own Recipient", "own@recipient.test", &e.Rep1)

	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, ownPersonPerms())

	token, found, err := store.PreferenceTokenForEmail(rep1, "foreign@recipient.test")
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("minting a token for a recipient outside the caller's row scope = (%q, %v, %v), want ErrNotFound",
			token, found, err)
	}
	if n := livePreferenceTokens(t, e); n != 0 {
		t.Fatalf("the refused mint left %d preference token(s) behind, want 0 — a refusal that still writes the credential is not a refusal", n)
	}

	// Positive control: the gate narrows the mint, it does not break it. The
	// same seat mints for a recipient it CAN read, and the unbounded admin
	// mints for the one it cannot.
	own, found, err := store.PreferenceTokenForEmail(rep1, "own@recipient.test")
	if err != nil || !found || !strings.HasPrefix(own, "pref_") {
		t.Fatalf("minting for the caller's own recipient = (%q, %v, %v), want a pref_ token", own, found, err)
	}
	foreign, found, err := store.PreferenceTokenForEmail(e.Admin(), "foreign@recipient.test")
	if err != nil || !found || !strings.HasPrefix(foreign, "pref_") {
		t.Fatalf("admin minting for the foreign recipient = (%q, %v, %v), want a pref_ token", foreign, found, err)
	}
	if own == foreign {
		t.Fatal("two recipients share one preference token — a token must address exactly one person")
	}
}

// An address no person in the workspace carries still yields no token and no
// error: that send has nothing to unsubscribe from, and the consent gate
// ahead of it has already refused. The row-scope gate must not turn this into
// a refusal, or the send path becomes an in-CRM/not-in-CRM oracle.
func TestPreferenceTokenMintIsSilentForAnUnknownAddress(t *testing.T) {
	e := Setup(t)
	store := consent.NewStore(e.Pool)

	token, found, err := store.PreferenceTokenForEmail(e.Admin(), "stranger@nowhere.test")
	if err != nil || found || token != "" {
		t.Fatalf("minting for an unknown address = (%q, %v, %v), want no token and no error", token, found, err)
	}
}

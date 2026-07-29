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

// movePreferenceTokenClock backdates or closes a token's window. The clock
// the resolver reads is the DATABASE's, so the fixture moves the row instead
// of the clock — no sleep, and the assertions stay exact.
func movePreferenceTokenClock(t *testing.T, e *Env, token, setClause string) {
	t.Helper()
	tag, err := e.Pool.Exec(context.Background(),
		`UPDATE preference_token SET `+setClause+` WHERE token = $1`, token)
	if err != nil {
		t.Fatalf("ageing the token: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("ageing the token matched %d rows, want 1", tag.RowsAffected())
	}
}

func preferenceTokenRevoked(t *testing.T, e *Env, token string) bool {
	t.Helper()
	var revoked bool
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM preference_token WHERE token = $1`, token).Scan(&revoked); err != nil {
		t.Fatalf("reading the token's revocation: %v", err)
	}
	return revoked
}

// The token is a capability with a window, not a standing credential. Reuse
// itself is deliberate — the preference centre is revisitable, and one
// message's link must keep working after the next message goes out — so what
// is proven here is the BOUND: the resolver stops honouring a closed window,
// and the next send rotates rather than reviving it.
func TestPreferenceTokenExpiresAndTheNextSendRotatesIt(t *testing.T) {
	e := Setup(t)
	store := consent.NewStore(e.Pool)
	seedRecipient(t, e, "Bulk Recipient", "bulk@recipient.test", &e.Rep1)
	admin := e.Admin()

	first, found, err := store.PreferenceTokenForEmail(admin, "bulk@recipient.test")
	if err != nil || !found {
		t.Fatalf("first mint = (%q, %v, %v)", first, found, err)
	}
	// A later send inside the window reuses it: the recipient's older mail
	// must keep working, which is why this credential is not single-use.
	again, _, err := store.PreferenceTokenForEmail(admin, "bulk@recipient.test")
	if err != nil || again != first {
		t.Fatalf("a send inside the window minted %q, want the live token %q (err %v)", again, first, err)
	}

	movePreferenceTokenClock(t, e, first, `expires_at = now() - interval '1 second'`)
	if _, err := store.ResolvePreferenceToken(context.Background(), first); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an expired token still resolves → %v, want ErrNotFound", err)
	}

	rotated, found, err := store.PreferenceTokenForEmail(admin, "bulk@recipient.test")
	if err != nil || !found || rotated == first {
		t.Fatalf("the send after expiry returned (%q, %v, %v), want a fresh token", rotated, found, err)
	}
	if _, err := store.ResolvePreferenceToken(context.Background(), rotated); err != nil {
		t.Fatalf("the rotated token does not resolve: %v", err)
	}
	// Revoked, not merely stale: this rotation is the production writer
	// revoked_at was declared for and never had, and a superseded token must
	// stay dead rather than linger as a row that only looks live.
	if !preferenceTokenRevoked(t, e, first) {
		t.Fatal("rotation left the superseded token unrevoked")
	}
	if _, err := store.ResolvePreferenceToken(context.Background(), first); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("the superseded token resolves again after rotation → %v", err)
	}
}

// The sliding refresh alone would leave the population most at risk — an
// active bulk-mail subscriber, whose every message renews the same link —
// holding one permanent credential. Past the age ceiling the send rotates
// even though the refreshed window is still open.
func TestPreferenceTokenRotatesPastItsAgeCeiling(t *testing.T) {
	e := Setup(t)
	store := consent.NewStore(e.Pool)
	seedRecipient(t, e, "Long Subscriber", "subscriber@recipient.test", &e.Rep1)
	admin := e.Admin()

	first, _, err := store.PreferenceTokenForEmail(admin, "subscriber@recipient.test")
	if err != nil {
		t.Fatalf("first mint: %v", err)
	}
	// Old enough to retire, and still well inside its window — so only the
	// ceiling can produce the rotation this asserts.
	movePreferenceTokenClock(t, e, first,
		`created_at = now() - interval '1 year', expires_at = now() + interval '29 days'`)
	if _, err := store.ResolvePreferenceToken(context.Background(), first); err != nil {
		t.Fatalf("the aged token is inside its window and must still resolve until rotated: %v", err)
	}

	rotated, _, err := store.PreferenceTokenForEmail(admin, "subscriber@recipient.test")
	if err != nil {
		t.Fatalf("the send after the ceiling: %v", err)
	}
	if rotated == first {
		t.Fatal("a token past the age ceiling was refreshed instead of rotated — an active recipient keeps one permanent credential")
	}
	if !preferenceTokenRevoked(t, e, first) {
		t.Fatal("the retired token was not revoked, so the old link still resolves")
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

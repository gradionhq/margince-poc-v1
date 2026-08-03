// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package identity

// What the Settings list SHOWS, which is a different question from what the
// passport table holds. Two kinds of row live in that table — the passports a
// human minted and the credentials connections were issued — and a connection
// replaces its credential on every renewal. So the list is grouped, and these
// tests pin both halves of that grouping: a connection appears once however
// often it has rotated, and the human's own passports are never folded into
// each other by the same grouping.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// rotate renews a connection through the module's own rotation path, so the
// rows it leaves are exactly the ones a real renewal leaves: the previous
// passport revoked, a fresh one minted under the same grant.
func (e *revocationEnv) rotate(t *testing.T, fixture *connectFixture) {
	t.Helper()
	_, refreshed, err := e.svc.rotateRefreshToken(e.wsCtx(e.admin), refreshRequest{
		Token: fixture.refresh, ClientID: fixture.clientID,
	})
	if err != nil {
		t.Fatalf("rotating the connection: %v", err)
	}
	fixture.refresh = refreshed
}

// A connection is ONE row in the list, however many times it has renewed.
// Without the grouping this is the list's worst failure mode and the least
// visible one: nothing is wrong on the day it ships, and a week later the
// human's own passports are buried under rotation debris carrying the same
// name and no way to tell which is live.
func TestAConnectionIsListedOncePerConnectionNotOncePerRotation(t *testing.T) {
	e := setupRevocationEnv(t, "passport-list-rotation")
	fixture := e.connectOAuth(t)

	e.rotate(t, &fixture)
	e.rotate(t, &fixture)

	// The rotations really did happen, or the assertion below would pass on a
	// list that had nothing to collapse. Two, not three: issueGrant records the
	// consent alone, and each rotation is what mints a passport under it — the
	// first has no predecessor to retire.
	var passports int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM passport WHERE oauth_grant_id = $1`, fixture.grantID).Scan(&passports); err != nil {
		t.Fatalf("counting the connection's passports: %v", err)
	}
	if passports != 2 {
		t.Fatalf("the grant has %d passports, want 2: the rotations must leave a predecessor behind for this test to mean anything",
			passports)
	}

	rows, err := e.svc.ListPassports(e.wsCtx(e.admin), e.admin)
	if err != nil {
		t.Fatalf("listing passports: %v", err)
	}
	var connections []PassportRow
	for _, row := range rows {
		if row.Connection != nil {
			connections = append(connections, row)
		}
	}
	if len(connections) != 1 {
		t.Fatalf("the list shows %d connection rows for one connection, want 1", len(connections))
	}
	// The row shown is the LIVE credential, not whichever predecessor sorted
	// first. A list that showed a rotated-out passport would offer Disconnect on
	// a credential that is already dead and report the connection as revoked.
	if connections[0].RevokedAt != nil {
		t.Fatal("the listed connection is a revoked passport: the row shown must be the newest under the grant, which is the live one")
	}
	if connections[0].Connection.ClientID != fixture.clientID {
		t.Fatalf("listed client_id = %q, want %q", connections[0].Connection.ClientID, fixture.clientID)
	}
	// connected_at is the GRANT's age. Reading it off the passport would make a
	// connection look newer than the consent that authorized it every time the
	// client renewed — and the listed passport here is two rotations younger
	// than the grant, so the two values cannot coincide by accident.
	var grantCreated time.Time
	if err := e.owner.QueryRow(context.Background(),
		`SELECT created_at FROM oauth_grant WHERE id = $1`, fixture.grantID).Scan(&grantCreated); err != nil {
		t.Fatalf("reading the grant's age: %v", err)
	}
	if !connections[0].Connection.ConnectedAt.Equal(grantCreated) {
		t.Fatalf("connected_at = %v, want the grant's own %v — a connection is as old as the consent that authorized it, not as its newest credential",
			connections[0].Connection.ConnectedAt, grantCreated)
	}
	if !connections[0].CreatedAt.After(grantCreated) {
		t.Fatalf("the listed passport (%v) is not younger than its grant (%v): the rotations this test depends on did not move the clock",
			connections[0].CreatedAt, grantCreated)
	}
}

// The grouping must not reach the human's OWN passports. It coalesces to the
// passport's own id because DISTINCT ON treats NULLs as equal, and this is the
// test that fails if that coalesce is ever simplified away: every minted
// passport has a NULL grant, so the bare column would collapse all of them into
// one row and silently hide credentials a human still holds.
func TestMintedPassportsAreNeverFoldedIntoEachOther(t *testing.T) {
	e := setupRevocationEnv(t, "passport-list-unbound")
	first := e.mintLendable(t, e.admin, []string{"read"})
	second := e.mintLendable(t, e.admin, []string{"read", "write"})

	rows, err := e.svc.ListPassports(e.wsCtx(e.admin), e.admin)
	if err != nil {
		t.Fatalf("listing passports: %v", err)
	}
	listed := map[ids.PassportID]bool{}
	for _, row := range rows {
		if row.Connection != nil {
			t.Fatalf("passport %s is reported as a connection although no grant issued it", row.ID)
		}
		listed[row.ID] = true
	}
	if !listed[first] || !listed[second] {
		t.Fatalf("the list shows %d of the 2 minted passports: both must appear", len(listed))
	}
}

// A minted passport and a connection whose grant id EQUALS that passport's id
// are two rows, not one. The pair is astronomically unlikely to arise on its
// own — two independent uuidv7 defaults — so it is constructed here, which is
// the only way to prove the grouping key discriminates the two namespaces
// rather than merely making a collision improbable. Drop the leading
// `oauth_grant_id IS NULL` from the DISTINCT ON and one of these rows vanishes.
func TestAPassportAndAGrantSharingAnIDAreStillTwoRows(t *testing.T) {
	e := setupRevocationEnv(t, "passport-list-collision")
	minted := e.mintLendable(t, e.admin, []string{"read"})
	ctx := context.Background()

	clientID := "client-" + ids.NewV7().String()
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO oauth_client (workspace_id, client_id, client_name, redirect_uris)
		VALUES ($1, $2, 'collision', ARRAY['https://client.example/cb'])`,
		e.admin.WorkspaceID, clientID); err != nil {
		t.Fatalf("registering the client: %v", err)
	}
	// The grant takes the minted passport's id as its OWN id: the collision the
	// grouping key has to survive.
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO oauth_grant (id, workspace_id, client_id, user_id, scopes, refresh_allowed)
		VALUES ($1, $2, $3, $4, ARRAY['read']::text[], false)`,
		minted, e.admin.WorkspaceID, clientID, e.admin.UserID); err != nil {
		t.Fatalf("issuing the colliding grant: %v", err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO passport (workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at, oauth_grant_id)
		VALUES ($1, $2, $2, 'oauth:collision', ARRAY['read']::text[], $3, now() + interval '30 days', $4)`,
		e.admin.WorkspaceID, e.admin.UserID, "collision-hash-"+minted.String(), minted); err != nil {
		t.Fatalf("minting the connection's credential: %v", err)
	}

	rows, err := e.svc.ListPassports(e.wsCtx(e.admin), e.admin)
	if err != nil {
		t.Fatalf("listing passports: %v", err)
	}
	var mintedSeen, connectionSeen bool
	for _, row := range rows {
		if row.ID == minted {
			mintedSeen = true
		}
		if row.Connection != nil && row.Connection.ClientID == clientID {
			connectionSeen = true
		}
	}
	if !mintedSeen || !connectionSeen {
		t.Fatalf("minted listed=%v connection listed=%v: a grant id equal to a passport id must not fold the two into one row",
			mintedSeen, connectionSeen)
	}
}

// The provenance a human actually reads: which of their passports a connection
// came from, by name. The label is resolved at READ time, which is why the
// rename below happens AFTER the lend — a snapshot taken at consent would keep
// the old name and pass a weaker version of this test. The audit row is what
// holds the dated fact; this column holds the current one.
func TestAConnectionNamesThePassportItWasLentFrom(t *testing.T) {
	e := setupRevocationEnv(t, "passport-list-provenance")
	lent := e.mintLendable(t, e.admin, []string{"read"})
	ctx := context.Background()
	if _, err := e.owner.Exec(ctx,
		`UPDATE passport SET label = 'the name at consent' WHERE id = $1`, lent); err != nil {
		t.Fatalf("labelling the lent passport: %v", err)
	}
	fixture := e.connectOAuthLent(t, e.admin, &lent, "provenance")
	// The grant records the consent; the credential under it is minted by the
	// exchange, which the rotation path stands in for here. Without one there
	// is no row for the connection to be listed on at all.
	e.rotate(t, &fixture)

	// Renamed after the fact. A read-time join reports the new name; a snapshot
	// reports the old one.
	const renamed = "the name today"
	if _, err := e.owner.Exec(ctx,
		`UPDATE passport SET label = $2 WHERE id = $1`, lent, renamed); err != nil {
		t.Fatalf("renaming the lent passport: %v", err)
	}

	rows, err := e.svc.ListPassports(e.wsCtx(e.admin), e.admin)
	if err != nil {
		t.Fatalf("listing passports: %v", err)
	}
	var connection *PassportConnectionRow
	for _, row := range rows {
		if row.Connection != nil && row.Connection.ClientID == fixture.clientID {
			connection = row.Connection
		}
	}
	if connection == nil {
		t.Fatal("the connection is absent from the list")
	}
	if connection.LentPassportID == nil || *connection.LentPassportID != lent {
		t.Fatalf("lent passport = %v, want %s", connection.LentPassportID, lent)
	}
	if connection.LentPassportLabel == nil || *connection.LentPassportLabel != renamed {
		t.Fatalf("lent passport label = %v, want %q — the label is resolved when the list is read, not snapshotted at consent",
			connection.LentPassportLabel, renamed)
	}
	// The grant asked for refresh, so its credential turning over is a renewal
	// and not the end of the connection — the fact the UI needs to tell a
	// connection between credentials from one that is over.
	if !connection.Renewable {
		t.Fatal("the connection reports itself non-renewable although its grant allows refresh")
	}
}

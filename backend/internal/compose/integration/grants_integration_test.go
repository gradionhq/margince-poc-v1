// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Manual record grants (A52/ADR-0039): a share widens the subject's
// row scope for exactly one record, revocation binds on the next
// query, and only humans share directly — agent shares queue behind
// the approval gate.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The widening itself is a platform/auth property, so it is asserted at
// the store layer where scoped principals are cheap to mint.
func TestRecordGrantWidensRowScopeAndRevokes(t *testing.T) {
	e := SetupSearch(t)
	foreign := e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Shared Secret', $3, 'manual', 'human:x')`, e.Rep3)

	repCtx := e.AsTeamRep(e.Rep1, e.Team1)
	peopleStore := people.NewStore(e.DB())

	// Before the grant: team scope hides rep3's record from rep1.
	if _, err := peopleStore.GetPerson(repCtx, PersonIDOf(foreign), storekit.LiveOnly); err == nil {
		t.Fatal("foreign person visible before any grant")
	}
	// A search misses it too.
	page, err := e.Store.Search(repCtx, search.Input{Query: "Shared Secret"})
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("pre-grant search: %v %+v", err, page.Hits)
	}

	grantID := e.Seed(t, `INSERT INTO record_grant (id, workspace_id, record_type, record_id, subject_type, subject_id, access, granted_by)
		VALUES ($1, $2, 'person', $3, 'user', $4, 'read', $5)`, foreign, e.Rep1, e.Rep3)

	// After: the direct read, the search branch, and the link probe all
	// see the record through the SAME widened predicate.
	if _, err := peopleStore.GetPerson(repCtx, PersonIDOf(foreign), storekit.LiveOnly); err != nil {
		t.Fatalf("granted person still hidden: %v", err)
	}
	page, err = e.Store.Search(repCtx, search.Input{Query: "Shared Secret"})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("post-grant search: %v %+v", err, page.Hits)
	}

	// Revocation binds on the next query.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `DELETE FROM record_grant WHERE id = $1`, grantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := peopleStore.GetPerson(repCtx, PersonIDOf(foreign), storekit.LiveOnly); err == nil {
		t.Fatal("revoked grant still widens visibility")
	}
}

// Scope-intersection (crm.yaml: "A grant can never exceed the granting
// principal's own access"). The visibility arm counts every live grant
// regardless of its access, because write satisfies read — so a `read` share
// is enough to pass EnsureLinkTarget, and without a separate probe the person
// it was shared with could hand on write: to a colleague, or by re-asserting
// their own grant onto themselves.
//
// The re-assert half is what makes this urgent rather than theoretical. A
// first `write` grant to a subject who already holds `read` used to be refused
// by the unique constraint; the upsert accepts it, so the only thing standing
// between a read share and a write one is this rule.
func TestAShareIsNotALicenceToReShare(t *testing.T) {
	e := SetupSearch(t)
	// Owned by rep3 in team2, so rep1 in team1 has no path to it but a share.
	foreign := e.Seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Out Of Scope', $3, 'manual', 'human:x')`, e.Rep3)
	colleague := e.Seed(t, `INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'colleague@search.test', 'Colleague')`)
	svc := identity.NewService(e.Pool)
	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	share := func(as context.Context, in identity.CreateGrantInput) error {
		in.RecordType, in.RecordID, in.SubjectType = "person", foreign, "user"
		_, err := svc.CreateRecordGrant(as, in)
		return err
	}
	// The owner shares through the real writer — a hand-inserted row would set
	// a scene production cannot reach.
	owner := grantingPrincipal(e, e.Rep3, principal.RowScopeOwn, nil)
	// A team-scoped rep who may update people: the ordinary shape of a
	// colleague, and the only thing that ever kept them off a record outside
	// their team is the row scope, which the share below widens.
	rep := grantingPrincipal(e, e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1})

	if err := share(owner, identity.CreateGrantInput{
		SubjectID: e.Rep1, Access: "read", ExpiresAt: &expiry,
	}); err != nil {
		t.Fatalf("owner shares read, time-boxed → %v", err)
	}

	// Rep1 can now READ the record. Three things they must still not do, and
	// the third is the one the upsert would otherwise have handed them.
	for _, tc := range []struct {
		name string
		in   identity.CreateGrantInput
	}{
		{"pass the record on to a colleague", identity.CreateGrantInput{SubjectID: colleague, Access: "read"}},
		{"widen their own share to write", identity.CreateGrantInput{SubjectID: e.Rep1, Access: "write"}},
		{"clear the expiry that time-boxed it", identity.CreateGrantInput{SubjectID: e.Rep1, Access: "read"}},
	} {
		if err := share(rep, tc.in); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("%s → %v, want permission-denied", tc.name, err)
		}
	}

	// The allow arm, without which every assertion above would pass against a
	// probe that refused everyone: the OWNER may still administer the share,
	// including clearing the expiry they set.
	if err := share(owner, identity.CreateGrantInput{SubjectID: e.Rep1, Access: "read"}); err != nil {
		t.Fatalf("owner re-asserts their own share → %v", err)
	}

	// The time box survived every refusal and yielded only to the owner. Read
	// back, because a 403 raised after the upsert ran is indistinguishable from
	// one raised instead of it.
	var access string
	var expiresAt *time.Time
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT access, expires_at FROM record_grant WHERE record_id = $1 AND subject_id = $2`,
			foreign, e.Rep1).Scan(&access, &expiresAt)
	}); err != nil {
		t.Fatal(err)
	}
	if access != "read" {
		t.Errorf("the share reads %q after three refused widenings, want read", access)
	}
	if expiresAt != nil {
		t.Errorf("expires_at = %v after the owner cleared it, want null", expiresAt)
	}
	var colleagueGrants int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM record_grant WHERE record_id = $1 AND subject_id = $2`,
			foreign, colleague).Scan(&colleagueGrants)
	}); err != nil {
		t.Fatal(err)
	}
	if colleagueGrants != 0 {
		t.Errorf("%d grants reached a colleague the sharer never named", colleagueGrants)
	}
}

// grantingPrincipal mints a human who may read and update people at one row
// scope — the two permissions sharing a record needs, and nothing else, so a
// refusal in these tests can only come from the rule under test.
func grantingPrincipal(e *SearchEnv, user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.WS)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
		TeamIDs: teams,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"person": {Read: true, Update: true}},
			RowScope: scope,
		},
	})
}

func TestRecordGrantHTTPLifecycle(t *testing.T) {
	e := setupRelationships(t)

	var grant struct {
		ID string `json:"id"`
	}
	// Sharing with a random subject refuses (the subject must exist).
	if status := e.Call(t, "POST", "/v1/record-grants", apptest.AnyMap{
		"record_type": "person", "record_id": e.personID,
		"subject_type": "user", "subject_id": "00000000-0000-7000-8000-00000000dead",
		"access": "read",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("grant to missing subject → %d, want 404", status)
	}
	subject := meUserID(t, e)
	if status := e.Call(t, "POST", "/v1/record-grants", apptest.AnyMap{
		"record_type": "person", "record_id": e.personID,
		"subject_type": "user", "subject_id": subject,
		"access": "write", "reason": "deal desk assist",
	}, nil, &grant); status != http.StatusCreated {
		t.Fatalf("create grant → %d", status)
	}

	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/record-grants?record_type=person&record_id="+e.personID, nil, nil, &listed); status != http.StatusOK || len(listed.Data) != 1 {
		t.Fatalf("list grants → %d %+v", status, listed)
	}
	if status := e.Call(t, "DELETE", "/v1/record-grants/"+grant.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke → %d", status)
	}
	// The share and the revocation are both audited facts.
	var shares, unshares int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE action = 'record_share'),
		        count(*) FILTER (WHERE action = 'record_unshare') FROM audit_log`).Scan(&shares, &unshares); err != nil {
		t.Fatal(err)
	}
	if shares != 1 || unshares != 1 {
		t.Fatalf("share audit trail: %d/%d, want 1/1", shares, unshares)
	}
}

// Re-asserting a grant is the contract's own word for it: `createRecordGrant`
// is "Idempotent on (record_type, record_id, subject_type, subject_id) —
// re-asserting upgrades/downgrades access and resets expires_at", and 201 is
// documented as "Grant created (or updated)". The natural key carries the
// identity, so the second call has to reach the SAME row rather than mint a
// second one or refuse — asserting the status alone would pass against an
// insert that quietly duplicated the grant.
func TestReAssertingAGrantUpdatesTheSameRow(t *testing.T) {
	e := setupRelationships(t)
	subject := meUserID(t, e)

	assert := func(body apptest.AnyMap) (int, grantBody) {
		var got grantBody
		body["record_type"], body["record_id"] = "person", e.personID
		body["subject_type"], body["subject_id"] = "user", subject
		return e.Call(t, "POST", "/v1/record-grants", body, nil, &got), got
	}

	expiry := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	status, first := assert(apptest.AnyMap{
		"access": "read", "reason": "quarter review", "expires_at": expiry.Format(time.RFC3339),
	})
	if status != http.StatusCreated {
		t.Fatalf("create grant → %d", status)
	}
	if first.ExpiresAt == nil || !first.ExpiresAt.Equal(expiry) {
		t.Fatalf("created grant's expiry = %v, want %v — the reset below proves nothing without it",
			first.ExpiresAt, expiry)
	}

	// An upgrade: same tuple, wider access, no expiry and no reason. Every
	// field the contract names moves, and `expires_at` moving to NULL is the
	// half a COALESCE-shaped upsert would silently refuse to do.
	status, second := assert(apptest.AnyMap{"access": "write"})
	if status != http.StatusCreated {
		t.Fatalf("re-assert → %d, want 201 (the contract declares no 409 for this operation)", status)
	}
	if second.ID != first.ID {
		t.Fatalf("re-assert minted a new grant %s, want the original %s", second.ID, first.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at moved %s → %s; a re-assert is not a new sharing relationship",
			first.CreatedAt, second.CreatedAt)
	}
	if second.Access != "write" {
		t.Errorf("access after upgrade = %q, want write", second.Access)
	}
	if second.ExpiresAt != nil {
		t.Errorf("expires_at = %v after a re-assert that supplied none; the contract says it resets", *second.ExpiresAt)
	}
	if second.Reason != nil {
		t.Errorf("reason = %q survived a re-assert that supplied none", *second.Reason)
	}

	// And back down again: the contract says upgrades AND downgrades.
	if status, third := assert(apptest.AnyMap{"access": "read"}); status != http.StatusCreated || third.Access != "read" {
		t.Fatalf("downgrade → %d %q, want 201 read", status, third.Access)
	}

	// One row throughout — the whole point of the natural key.
	var rows int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM record_grant WHERE record_id = $1 AND subject_id = $2`,
		e.personID, subject).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d grant rows for one (record, subject) pair, want 1", rows)
	}
}

// Every assertion is audited, and a re-assert audits what it DISPLACED.
// A before-image of nil on an update reads as a first-ever share, which is
// the one thing the record timeline must not say about a downgrade.
func TestReAssertingAGrantAuditsWhatItDisplaced(t *testing.T) {
	e := setupRelationships(t)
	subject := meUserID(t, e)
	share := func(access string) {
		t.Helper()
		if status := e.Call(t, "POST", "/v1/record-grants", apptest.AnyMap{
			"record_type": "person", "record_id": e.personID,
			"subject_type": "user", "subject_id": subject, "access": access,
		}, nil, nil); status != http.StatusCreated {
			t.Fatalf("share %s → %d", access, status)
		}
	}
	share("write")
	share("read")

	// Counted rather than ordered: both rows are written by separate
	// transactions milliseconds apart, and a test that leans on their
	// timestamps to tell them apart is a flake waiting for a fast machine.
	// The images identify themselves.
	var firstShares, downgrades, total int
	if err := e.Owner.QueryRow(t.Context(), `
		SELECT count(*),
		       count(*) FILTER (WHERE before IS NULL AND after ->> 'access' = 'write'),
		       count(*) FILTER (WHERE before ->> 'access' = 'write' AND after ->> 'access' = 'read')
		  FROM audit_log WHERE action = 'record_share'`).
		Scan(&total, &firstShares, &downgrades); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("%d record_share rows, want 2 — every assertion is audited, re-asserts included", total)
	}
	if firstShares != 1 {
		t.Errorf("%d first-share rows (before IS NULL, after write), want 1", firstShares)
	}
	if downgrades != 1 {
		t.Errorf("%d downgrade rows (before write → after read), want 1; a re-assert that audits before=NULL reads as a first share", downgrades)
	}
}

type grantBody struct {
	ID        string     `json:"id"`
	Access    string     `json:"access"`
	Reason    *string    `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// meUserID reads the session's own user id, which is the only subject these
// tests can share with: the bootstrap seeds one human.
func meUserID(t *testing.T, e *relEnv) string {
	t.Helper()
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status := e.Call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("me → %d", status)
	}
	return me.User.ID
}

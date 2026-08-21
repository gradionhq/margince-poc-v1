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
// the store layer where scoped principals are cheap to mint. A contact is
// readable by every seat unless its capture is private, so the record a
// share opens here is a capture-private contact of rep3's.
func TestRecordGrantWidensRowScopeAndRevokes(t *testing.T) {
	e := SetupSearch(t)
	foreign := e.SeedID(t, `INSERT INTO person (id, full_name, owner_id, visibility, source, captured_by) VALUES ($1, 'Shared Secret', $2, 'owner', 'manual', 'human:x')`, e.Rep3)

	repCtx := e.AsTeamRep(e.Rep1, e.Team1)
	peopleStore := people.NewStore(e.DB())

	// Before the grant: capture privacy hides rep3's record from rep1.
	if _, err := peopleStore.GetPerson(repCtx, PersonIDOf(foreign), storekit.LiveOnly); err == nil {
		t.Fatal("foreign person visible before any grant")
	}
	// A search misses it too.
	page, err := e.Store.Search(repCtx, search.Input{Query: "Shared Secret"})
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("pre-grant search: %v %+v", err, page.Hits)
	}

	grantID := e.SeedID(t, `INSERT INTO record_grant (id, record_type, record_id, subject_type, subject_id, access, granted_by)
		VALUES ($1, 'person', $2, 'user', $3, 'read', $4)`, foreign, e.Rep1, e.Rep3)

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
// ADR-0039's scope-intersection rule: "a granter can never share wider than
// they hold." The rule is now applied at EVERY access level, and this test says
// so deliberately, because it is a CHANGE: passing `read` on from a read share
// used to be a supported flow, with a screen state of its own.
//
// What changed the reading is that a grant assertion is an upsert on
// (record_type, record_id, subject_type, subject_id) and restates the whole
// grant — access, expiry, reason and granted_by all take the new request's
// values. So a caller admitted at `read` is not only passing on sight they
// hold; on an existing grant they are rewriting its TERMS, including terms
// somebody else set. Narrower-or-equal is a claim about the access column, and
// the row carries more than that column.
//
// The gate that admits any of this is EnsureLinkTarget, and the visibility arm
// counts every live grant regardless of access, so a read-share holder passes
// it. The probe behind it is what decides, and it now asks the same question
// whatever access is being asserted: can this caller change the row themselves.
func TestAShareIsPassedOnOnlyByACallerWhoCouldChangeTheRow(t *testing.T) {
	e := SetupSearch(t)
	// Owned by rep3 in team2, so rep1 in team1 has no path to it but a share.
	foreign := e.SeedID(t, `INSERT INTO person (id, full_name, owner_id, source, captured_by) VALUES ($1, 'Out Of Scope', $2, 'manual', 'human:x')`, e.Rep3)
	colleague := e.SeedID(t, `INSERT INTO app_user (id, email, display_name) VALUES ($1, 'colleague@search.test', 'Colleague')`)
	svc := identity.NewService(e.Pool)

	// Every grant below goes through the real writer, including the ones that
	// set the scene: a hand-inserted row would prove the rule against a state
	// production cannot reach.
	share := func(as context.Context, subject ids.UUID, access string) error {
		_, err := svc.CreateRecordGrant(as, identity.CreateGrantInput{
			RecordType: "person", RecordID: foreign,
			SubjectType: "user", SubjectID: subject, Access: access,
		})
		return err
	}
	owner := grantingPrincipal(e, e.Rep3, principal.RowScopeOwn, nil)
	rep := grantingPrincipal(e, e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1})

	if err := share(owner, e.Rep1, "read"); err != nil {
		t.Fatalf("owner shares read → %v", err)
	}

	// Holding only `read`, rep1 is refused at BOTH access levels, and in both
	// directions: onto a colleague, and onto themselves by re-asserting their
	// own grant. `read` is the arm that changed — it was allowed before.
	if err := share(rep, colleague, "read"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("passing read on from a read share → %v, want permission-denied: "+
			"the assertion restates the whole grant, including its term", err)
	}
	if err := share(rep, colleague, "write"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("granting write from a read share → %v, want permission-denied", err)
	}
	if err := share(rep, e.Rep1, "write"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("re-asserting one's own read grant as write → %v, want permission-denied", err)
	}
	if err := share(rep, e.Rep1, "read"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("re-asserting one's own read grant as read → %v, want permission-denied: "+
			"this is the assertion that could restate the grant's expiry", err)
	}

	// The allow arm, without which every refusal above would pass against a
	// probe that simply refused everything: once the owner upgrades rep1 to
	// write, the same caller may pass write on. Same record, same caller, one
	// column different — so what the refusals measure is the authority, not the
	// call.
	if err := share(owner, e.Rep1, "write"); err != nil {
		t.Fatalf("owner upgrades the share to write → %v", err)
	}
	if err := share(rep, colleague, "write"); err != nil {
		t.Errorf("passing write on while holding write → %v, want allowed", err)
	}

	// Read back, because a 403 raised after the upsert ran is indistinguishable
	// from one raised instead of it.
	var access string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT access FROM record_grant WHERE record_id = $1 AND subject_id = $2`,
			foreign, colleague).Scan(&access)
	}); err != nil {
		t.Fatal(err)
	}
	if access != "write" {
		t.Fatalf("the colleague's grant reads %q, want write", access)
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

// A grant assertion restates the whole grant — access, expiry, reason and
// granted_by all take the new request's values, which is documented and
// deliberate. That makes the assertion a write of the grant's TERM, not only of
// its width, so the authority it needs is authority over the record.
//
// The two record types here are the ones that make the rule non-trivial. On
// `deal` (and `lead`) every seat reads every row, so any rule phrased as "the
// caller must have some claim on this record" is satisfied by everyone and
// closes nothing. Write authority is owner- or grant-scoped even there, which
// is why that is the question asked.
func TestARecordsTermsAreRestatedOnlyByACallerWhoCouldChangeIt(t *testing.T) {
	e := SetupSearch(t)
	svc := identity.NewService(e.Pool)
	colleague := e.SeedID(t, `INSERT INTO app_user (id, email, display_name) VALUES ($1, 'termcolleague@search.test', 'Term Colleague')`)

	pipeline := e.SeedID(t, `INSERT INTO pipeline (id, name) VALUES ($1, 'Terms')`)
	stage := e.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position) VALUES ($1, $2, 'Open', 1)`, pipeline)
	org := e.SeedID(t, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'Terms GmbH', 'manual', 'human:x')`)

	// Both owned by rep3 in team2. rep1 in team1 holds no owner or team claim
	// on either, and on the deal that is the ONLY thing standing between them
	// and the row — the read is workspace-wide.
	deal := e.SeedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, owner_id, source, captured_by)
	                     VALUES ($1, 'Terms Deal', $2, $3, $4, 'manual', 'human:x')`, pipeline, stage, e.Rep3)
	project := e.SeedID(t, `INSERT INTO project (id, name, organization_id, owner_id, source, captured_by)
	                        VALUES ($1, 'Terms Project', $2, $3, 'manual', 'human:x')`, org, e.Rep3)

	// The same seat shape grantingPrincipal mints, widened to the two record
	// types under test: read and update on each, and nothing else, so a refusal
	// below can only come from the rule this test is about.
	seat := func(user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
		ctx := principal.WithWorkspaceID(context.Background(), e.WS)
		return principal.WithActor(ctx, principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
			TeamIDs: teams,
			Permissions: principal.Permissions{
				Objects: map[string]principal.ObjectGrant{
					"deal":    {Read: true, Update: true},
					"project": {Read: true, Update: true},
				},
				RowScope: scope,
			},
		})
	}
	owner := seat(e.Rep3, principal.RowScopeOwn, nil)
	rep := seat(e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1})

	for _, record := range []struct {
		recordType string
		id         ids.UUID
	}{{"deal", deal}, {"project", project}} {
		t.Run(record.recordType, func(t *testing.T) {
			until := time.Now().Add(time.Hour).UTC()
			share := func(as context.Context, subject ids.UUID, access string, expires *time.Time) error {
				_, err := svc.CreateRecordGrant(as, identity.CreateGrantInput{
					RecordType: record.recordType, RecordID: record.id,
					SubjectType: "user", SubjectID: subject, Access: access,
					ExpiresAt: expires,
				})
				return err
			}

			// The owner shares, on a term of their choosing.
			if err := share(owner, e.Rep1, "read", &until); err != nil {
				t.Fatalf("owner shares read until a deadline → %v", err)
			}

			// The holder cannot restate it — not onto themselves, and not onto
			// anybody else. Their whole claim on the row is the grant they are
			// restating.
			if err := share(rep, e.Rep1, "read", nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("the holder restating their own grant → %v, want permission-denied", err)
			}
			if err := share(rep, colleague, "read", nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
				t.Errorf("the holder passing the record on → %v, want permission-denied", err)
			}

			// Read the column back. A refusal raised AFTER the upsert ran is
			// indistinguishable from one raised instead of it, and the whole
			// question here is whether the stored term moved.
			var stored *time.Time
			if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
				return tx.QueryRow(context.Background(),
					`SELECT expires_at FROM record_grant
					  WHERE record_type = $1 AND record_id = $2 AND subject_id = $3`,
					record.recordType, record.id, e.Rep1).Scan(&stored)
			}); err != nil {
				t.Fatal(err)
			}
			if stored == nil {
				t.Fatal("the grant's expiry was cleared by a caller who cannot change the record")
			}
			if !stored.Truncate(time.Second).Equal(until.Truncate(time.Second)) {
				t.Errorf("the stored expiry is %v, want the term its owner set (%v)", stored, until)
			}

			// The allow arm. Without it every refusal above would pass against a
			// probe that refused everyone, which would break sharing rather than
			// gate it: the owner still restates their own grant, expiry and all.
			if err := share(owner, e.Rep1, "read", nil); err != nil {
				t.Errorf("the record's owner restating the grant they made → %v, want allowed", err)
			}
			if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
				return tx.QueryRow(context.Background(),
					`SELECT expires_at FROM record_grant
					  WHERE record_type = $1 AND record_id = $2 AND subject_id = $3`,
					record.recordType, record.id, e.Rep1).Scan(&stored)
			}); err != nil {
				t.Fatal(err)
			}
			if stored != nil {
				t.Errorf("the owner's own re-assert left the expiry at %v — the documented "+
					"reset, which this change must not break, did not happen", stored)
			}
		})
	}
}

// The two arms that make the rule's REACH visible, and neither is about the
// share's beneficiary — which every other case in this file is.
//
// REACHING the assertion needs no prior relationship to the record: the object
// grant (the default rep role holds it on every type here) and the VISIBILITY
// probe, which on deal and lead degrades to "does this row exist" because those
// tables are read by every seat and the scope clause renders empty.
//
// What refuses is the separate write-authority probe inside EnsureCanGrant, and
// that one does NOT degrade — its predicate is owner-or-live-write-grant on
// every table. The distinction is the whole rule: a caller can always reach the
// door, and is turned away at it.
func TestAStrangerToTheRecordCannotRestateItsGrants(t *testing.T) {
	e := SetupSearch(t)
	svc := identity.NewService(e.Pool)

	pipeline := e.SeedID(t, `INSERT INTO pipeline (id, name) VALUES ($1, 'Stranger')`)
	stage := e.SeedID(t, `INSERT INTO stage (id, pipeline_id, name, position) VALUES ($1, $2, 'Open', 1)`, pipeline)
	deal := e.SeedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, owner_id, source, captured_by)
	                     VALUES ($1, 'Stranger Deal', $2, $3, $4, 'manual', 'human:x')`, pipeline, stage, e.Rep3)
	beneficiary := e.SeedID(t, `INSERT INTO app_user (id, email, display_name) VALUES ($1, 'beneficiary@search.test', 'Beneficiary')`)
	worker := e.SeedID(t, `INSERT INTO app_user (id, email, display_name, seat_type) VALUES ($1, 'worker@search.test', 'Worker', 'full')`)

	seat := func(user ids.UUID, scope principal.RowScope, teams []ids.UUID) context.Context {
		ctx := principal.WithWorkspaceID(context.Background(), e.WS)
		return principal.WithActor(ctx, principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:" + user.String(), UserID: user,
			TeamIDs: teams,
			Permissions: principal.Permissions{
				Objects:  map[string]principal.ObjectGrant{"deal": {Read: true, Update: true}},
				RowScope: scope,
			},
		})
	}
	owner := seat(e.Rep3, principal.RowScopeOwn, nil)
	// Rep1 holds NO grant on this deal and never did. The only thing they have
	// is the object permission every rep seat carries.
	stranger := seat(e.Rep1, principal.RowScopeTeam, []ids.UUID{e.Team1})

	share := func(as context.Context, subject ids.UUID, access string, expires *time.Time) error {
		_, err := svc.CreateRecordGrant(as, identity.CreateGrantInput{
			RecordType: "deal", RecordID: deal,
			SubjectType: "user", SubjectID: subject, Access: access,
			ExpiresAt: expires,
		})
		return err
	}
	readBack := func(t *testing.T, subject ids.UUID) (string, *time.Time) {
		t.Helper()
		var access string
		var expires *time.Time
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT access, expires_at FROM record_grant
				  WHERE record_type = 'deal' AND record_id = $1 AND subject_id = $2`,
				deal, subject).Scan(&access, &expires)
		}); err != nil {
			t.Fatal(err)
		}
		return access, expires
	}

	until := time.Now().Add(time.Hour).UTC()
	if err := share(owner, beneficiary, "read", &until); err != nil {
		t.Fatalf("owner shares read with a deadline → %v", err)
	}

	t.Run("a stranger cannot clear somebody else's deadline", func(t *testing.T) {
		if err := share(stranger, beneficiary, "read", nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("a caller holding no grant on this deal restated one → %v, want permission-denied", err)
		}
		if _, expires := readBack(t, beneficiary); expires == nil {
			t.Error("the deadline was cleared by a caller with no relationship to the record")
		}
	})

	t.Run("a stranger cannot downgrade a colleague's write grant", func(t *testing.T) {
		// The mirror of what mayRevoke already refuses. Its own comment gives
		// the reason: "anyone the record was ever shared with — read-only —
		// could delete a colleague's write grant on it, which is not an
		// escalation but is a way to take work away from people who are doing
		// it." Revoking was gated. Re-asserting `read` over a stored `write`
		// reaches the same end through SET access = EXCLUDED.access, and was
		// not — the write-seat check returns early for a non-write assert, and
		// so did the authority probe.
		if err := share(owner, worker, "write", nil); err != nil {
			t.Fatalf("owner shares write with a colleague → %v", err)
		}
		if err := share(stranger, worker, "read", nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
			t.Errorf("a stranger downgraded a colleague's write grant → %v, want permission-denied", err)
		}
		if access, _ := readBack(t, worker); access != "write" {
			t.Errorf("the colleague's grant reads %q — their write authority was taken "+
				"by a caller who cannot change the record", access)
		}
	})
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// Manual record grants (A52/ADR-0039): a share widens the subject's
// row scope for exactly one record, revocation binds on the next
// query, and only humans share directly — agent shares queue behind
// the approval gate.

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// The widening itself is a platform/auth property, so it is asserted at
// the store layer where scoped principals are cheap to mint.
func TestRecordGrantWidensRowScopeAndRevokes(t *testing.T) {
	e := setupSearch(t)
	foreign := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, owner_id, source, captured_by) VALUES ($1, $2, 'Shared Secret', $3, 'manual', 'human:x')`, e.rep3)

	repCtx := e.asTeamRep(e.rep1, e.team1)
	peopleStore := people.NewStore(e.pool)

	// Before the grant: team scope hides rep3's record from rep1.
	if _, err := peopleStore.GetPerson(repCtx, foreign, storekit.LiveOnly); err == nil {
		t.Fatal("foreign person visible before any grant")
	}
	// A search misses it too.
	page, err := e.store.Search(repCtx, search.Input{Query: "Shared Secret"})
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("pre-grant search: %v %+v", err, page.Hits)
	}

	grantID := e.seed(t, `INSERT INTO record_grant (id, workspace_id, record_type, record_id, subject_type, subject_id, access, granted_by)
		VALUES ($1, $2, 'person', $3, 'user', $4, 'read', $5)`, foreign, e.rep1, e.rep3)

	// After: the direct read, the search branch, and the link probe all
	// see the record through the SAME widened predicate.
	if _, err := peopleStore.GetPerson(repCtx, foreign, storekit.LiveOnly); err != nil {
		t.Fatalf("granted person still hidden: %v", err)
	}
	page, err = e.store.Search(repCtx, search.Input{Query: "Shared Secret"})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("post-grant search: %v %+v", err, page.Hits)
	}

	// Revocation binds on the next query.
	if err := database.WithWorkspaceTx(e.admin(), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `DELETE FROM record_grant WHERE id = $1`, grantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := peopleStore.GetPerson(repCtx, foreign, storekit.LiveOnly); err == nil {
		t.Fatal("revoked grant still widens visibility")
	}
}

func TestRecordGrantHTTPLifecycle(t *testing.T) {
	e := setupRelationships(t)

	var grant struct {
		ID string `json:"id"`
	}
	// Sharing with a random subject refuses (the subject must exist).
	if status := e.call(t, "POST", "/v1/record-grants", anyMap{
		"record_type": "person", "record_id": e.personID,
		"subject_type": "user", "subject_id": "00000000-0000-7000-8000-00000000dead",
		"access": "read",
	}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("grant to missing subject → %d, want 404", status)
	}
	var admin struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status := e.call(t, "GET", "/v1/me", nil, nil, &admin); status != http.StatusOK {
		t.Fatalf("me → %d", status)
	}
	if status := e.call(t, "POST", "/v1/record-grants", anyMap{
		"record_type": "person", "record_id": e.personID,
		"subject_type": "user", "subject_id": admin.User.ID,
		"access": "write", "reason": "deal desk assist",
	}, nil, &grant); status != http.StatusCreated {
		t.Fatalf("create grant → %d", status)
	}
	// Duplicate share → 409.
	if status := e.call(t, "POST", "/v1/record-grants", anyMap{
		"record_type": "person", "record_id": e.personID,
		"subject_type": "user", "subject_id": admin.User.ID,
		"access": "read",
	}, nil, nil); status != http.StatusConflict {
		t.Fatalf("duplicate grant → %d, want 409", status)
	}

	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/record-grants?record_type=person&record_id="+e.personID, nil, nil, &listed); status != http.StatusOK || len(listed.Data) != 1 {
		t.Fatalf("list grants → %d %+v", status, listed)
	}
	if status := e.call(t, "DELETE", "/v1/record-grants/"+grant.ID, nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke → %d", status)
	}
	// The share and the revocation are both audited facts.
	var shares, unshares int
	if err := e.owner.QueryRow(t.Context(),
		`SELECT count(*) FILTER (WHERE action = 'record_share'),
		        count(*) FILTER (WHERE action = 'record_unshare') FROM audit_log`).Scan(&shares, &unshares); err != nil {
		t.Fatal(err)
	}
	if shares != 1 || unshares != 1 {
		t.Fatalf("share audit trail: %d/%d, want 1/1", shares, unshares)
	}
}

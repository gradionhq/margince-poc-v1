// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// A LinkedIn match, staged and decided through the approvals engine.
//
// This is the path the bespoke confirm/reject endpoints used to serve, and
// testing it end to end is the point: the store method the effect calls has its
// own tests, but nothing there proves the kind is registered, that deciding
// reaches the effect, or that a refusal is remembered the next time the stager
// runs — which is the branch's headline claim.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// linkedInMatchFixture seeds one connection whose name folds onto a contact's
// but does not equal it — the tier that needs a human, since an exact name now
// confirms itself.
func linkedInMatchFixture(ctx context.Context, t *testing.T, e *integration.Env) ids.UUID {
	t.Helper()
	var orgID ids.UUID
	seedAsAdmin(t, e, func(c context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c, `
			INSERT INTO organization (workspace_id, display_name, source, captured_by)
			VALUES (`+wsGUC+`, 'Acme GmbH', 'manual', 'human:test') RETURNING id`).Scan(&orgID)
	}, "seeding the account")

	person, err := e.People.CreatePerson(ctx, people.CreatePersonInput{
		FullName: "Andreas Muller", Source: "manual",
	})
	if err != nil {
		t.Fatalf("seeding the contact: %v", err)
	}
	employAt(t, e, ids.UUID(person.Id), orgID)

	seedAsAdmin(t, e, func(c context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(c, `
			INSERT INTO linkedin_connection
			    (workspace_id, owner_user_id, full_name, normalized_name, company_name,
			     normalized_company, profile_url, matched_org_id, source)
			VALUES (`+wsGUC+`, $1, 'Andreas Müller', 'andreas muller', 'Acme GmbH',
			        'acme', 'https://www.linkedin.com/in/amueller', $2, 'csv_export')`,
			e.Rep1, orgID)
		return err
	}, "seeding the connection")
	return ids.UUID(person.Id)
}

func TestApprovingAStagedLinkedInMatchLinksTheConnectionAndWritesTheURL(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	person := linkedInMatchFixture(ctx, t, e)

	store := people.NewStore(e.Pool)
	if _, err := store.MatchLinkedInConnections(ctx, e.Rep1); err != nil {
		t.Fatalf("matching: %v", err)
	}
	svc := approvalsServiceWithEffects(e.Pool)
	staged, err := StageLinkedInMatches(ctx, e.Pool, svc, store)
	if err != nil {
		t.Fatalf("staging: %v", err)
	}
	if staged != 1 {
		t.Fatalf("staged %d proposals, want the one folded-name match", staged)
	}

	id := onlyPendingLinkedInMatch(t, e)
	if _, err := svc.Decide(ctx, id, true, nil); err != nil {
		t.Fatalf("approving: %v", err)
	}

	// The effect ran: the connection is linked and the contact carries the
	// connection's own LinkedIn address.
	var status string
	var matched *ids.UUID
	var handle *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `
			SELECT match_status, matched_person_id FROM linkedin_connection
			 WHERE normalized_name = 'andreas muller'`).Scan(&status, &matched); err != nil {
			return err
		}
		return tx.QueryRow(context.Background(), `
			SELECT handle FROM person_social
			 WHERE person_id = $1 AND platform = 'linkedin'`, person).Scan(&handle)
	}); err != nil {
		t.Fatalf("reading the outcome: %v", err)
	}
	if status != "confirmed" || matched == nil || *matched != person {
		t.Errorf("the connection is %q → %v after approval, want confirmed → %s", status, matched, person)
	}
	if handle == nil || *handle != "https://www.linkedin.com/in/amueller" {
		t.Errorf("the contact carries %v, want the connection's own profile URL", handle)
	}
}

func TestARefusedLinkedInMatchIsNeverProposedAgain(t *testing.T) {
	// The branch's headline claim. Without it a member re-decides the same
	// wrong guess after every export refresh, which is the fastest way to
	// teach somebody to approve without reading.
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	linkedInMatchFixture(ctx, t, e)

	store := people.NewStore(e.Pool)
	if _, err := store.MatchLinkedInConnections(ctx, e.Rep1); err != nil {
		t.Fatalf("matching: %v", err)
	}
	svc := approvalsServiceWithEffects(e.Pool)
	if _, err := StageLinkedInMatches(ctx, e.Pool, svc, store); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if _, err := svc.Decide(ctx, onlyPendingLinkedInMatch(t, e), false, nil); err != nil {
		t.Fatalf("rejecting: %v", err)
	}

	// The stager runs again, exactly as a re-import or the hourly sweep would.
	staged, err := StageLinkedInMatches(ctx, e.Pool, svc, store)
	if err != nil {
		t.Fatalf("re-staging: %v", err)
	}
	if staged != 0 {
		t.Errorf("re-staging proposed %d matches, want 0 — a refusal must survive the next import", staged)
	}
}

// onlyPendingLinkedInMatch returns the single pending proposal of this kind,
// failing loudly on any other count so a test cannot silently decide the wrong
// row.
func onlyPendingLinkedInMatch(t *testing.T, e *integration.Env) ids.ApprovalID {
	t.Helper()
	var ids_ []ids.ApprovalID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `
			SELECT id FROM approval WHERE kind = 'linkedin_match' AND status = 'pending'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.ApprovalID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids_ = append(ids_, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the staged proposals: %v", err)
	}
	if len(ids_) != 1 {
		t.Fatalf("%d pending linkedin_match proposals, want exactly 1", len(ids_))
	}
	return ids_[0]
}

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
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
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
	staged, err := StageLinkedInMatches(ctx, svc, store)
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
	if _, err := StageLinkedInMatches(ctx, svc, store); err != nil {
		t.Fatalf("staging: %v", err)
	}
	if _, err := svc.Decide(ctx, onlyPendingLinkedInMatch(t, e), false, nil); err != nil {
		t.Fatalf("rejecting: %v", err)
	}

	// The stager runs again, exactly as a re-import or the hourly sweep would.
	staged, err := StageLinkedInMatches(ctx, svc, store)
	if err != nil {
		t.Fatalf("re-staging: %v", err)
	}
	if staged != 0 {
		t.Errorf("re-staging proposed %d matches, want 0 — a refusal must survive the next import", staged)
	}
}

// A suggestion the EVENT path produces has to reach the inbox in that same
// pass. It used to be matched, logged and dropped: the ghost row carried
// `suggested` and no approval existed, so the member was never asked and
// nothing in the product could tell them a question was waiting.
//
// No sweep runs here, deliberately — that is the whole claim. An hourly job
// that repairs the event path is a feature that works an hour late, and only
// for the owners that enumeration happens to reach.
func TestAPersonEventStagesTheLinkedInSuggestionItProduced(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	person := linkedInMatchFixture(ctx, t, e)
	// The pass runs under the ghost OWNER's own resolved authority, so the
	// owner needs a real grant — a member the resolver reports as holding
	// nothing is skipped, and the test would prove nothing about this path.
	grantReadPeopleRole(t, e, e.Rep1, "all")

	matcher := NewLinkedInMatchGen(e.Pool, people.NewStore(e.Pool), identity.NewService(e.Pool),
		slog.New(slog.DiscardHandler))
	if err := matcher.HandleEvent(context.Background(),
		envelopeFor(e.WS, "person.created", "person", person)); err != nil {
		t.Fatalf("handling person.created: %v", err)
	}

	// Exactly one, and it is the folded-name match: onlyPendingLinkedInMatch
	// fails loudly on any other count.
	onlyPendingLinkedInMatch(t, e)
}

// The sweep's rescue duty. A ghost the matcher already moved to `suggested`
// without staging is the residue the event path used to leave behind, and it
// was invisible twice over: `suggested` is not `unmatched`, so an owner holding
// nothing else dropped out of the sweep's enumeration entirely and no later
// pass could reach them.
func TestTheSweepStagesALinkedInSuggestionNobodyWasEverAskedAbout(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	linkedInMatchFixture(ctx, t, e)
	grantReadPeopleRole(t, e, e.Rep1, "all")

	// The residue, seeded through the real matcher rather than by hand: match
	// and do NOT stage, which is exactly the state the old event path left.
	store := people.NewStore(e.Pool)
	if _, err := store.MatchLinkedInConnections(ctx, e.Rep1); err != nil {
		t.Fatalf("matching: %v", err)
	}
	if pending := pendingLinkedInMatchCount(t, e); pending != 0 {
		t.Fatalf("%d proposals before the sweep, want 0 — the fixture is not the unstaged "+
			"residue this test is about", pending)
	}

	sweep := newLinkedInRematchWorker(e.Pool, store, identity.NewService(e.Pool),
		slog.New(slog.DiscardHandler))
	if _, err := sweep.sweepWorkspace(context.Background(), e.WS); err != nil {
		t.Fatalf("sweeping: %v", err)
	}

	onlyPendingLinkedInMatch(t, e)
}

// A contact edit must not cancel a LinkedIn match waiting to be decided.
//
// The proposal's claim is "this imported connection is this contact", and no
// field on the contact can make that false. Pinning the person's version bound
// the approval to content nobody judged, so any edit between staging and
// decision — a title correction, an owner change, a second match applying —
// failed the redemption's re-check and the member's yes did nothing.
func TestAContactEditDoesNotCancelAWaitingLinkedInMatch(t *testing.T) {
	e := integration.Setup(t)
	ctx := e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms)
	person := linkedInMatchFixture(ctx, t, e)

	store := people.NewStore(e.Pool)
	if _, err := store.MatchLinkedInConnections(ctx, e.Rep1); err != nil {
		t.Fatalf("matching: %v", err)
	}
	svc := approvalsServiceWithEffects(e.Pool)
	if _, err := StageLinkedInMatches(ctx, svc, store); err != nil {
		t.Fatalf("staging: %v", err)
	}
	id := onlyPendingLinkedInMatch(t, e)

	// Through the real writer, so the row's version moves exactly the way any
	// edit in the product moves it.
	title := "Head of Procurement"
	if _, err := store.UpdatePerson(ctx, ids.From[ids.PersonKind](person), people.UpdatePersonInput{
		Title: &title, Source: "manual",
	}); err != nil {
		t.Fatalf("editing the contact: %v", err)
	}

	if _, err := svc.Decide(ctx, id, true, nil); err != nil {
		t.Fatalf("approving after the edit: %v — a contact edit must not cancel a waiting match", err)
	}

	var status string
	var matched *ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT match_status, matched_person_id FROM linkedin_connection
			 WHERE normalized_name = 'andreas muller'`).Scan(&status, &matched)
	}); err != nil {
		t.Fatalf("reading the outcome: %v", err)
	}
	if status != "confirmed" || matched == nil || *matched != person {
		t.Errorf("the connection is %q → %v after an approved match, want confirmed → %s — "+
			"the approval was released but its effect did not run", status, matched, person)
	}
}

// pendingLinkedInMatchCount counts the pending proposals of this kind, for the
// assertions that are about there being none.
func pendingLinkedInMatchCount(t *testing.T, e *integration.Env) int {
	t.Helper()
	var n int
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM approval WHERE kind = 'linkedin_match' AND status = 'pending'`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting the staged proposals: %v", err)
	}
	return n
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

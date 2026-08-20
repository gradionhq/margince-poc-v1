// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package collections

// The two vocabulary leaves that reach a row OTHER than the one being selected:
// the ownership dial's team half, which walks team_membership, and an account's
// relationship to us, which is multi-valued and lives in its own table.
//
// Both are correlated subqueries, so what they compile to can be read in the unit
// lane but what they SELECT cannot. These are the scenarios only real Postgres
// answers — and the harness already seeds the shape they need: Rep1 and Rep2
// share Team1, Rep3 sits in Team2.

import (
	"testing"

	collectionsmod "github.com/gradionhq/margince/backend/internal/modules/collections"
	peoplemod "github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ownedOrg goes through CreateOrganization so the owner_id the team leaf joins on
// is the one production stamps. A hand-inserted row could carry an owner no
// membership edge reaches, and the join would then be proven against a shape the
// product cannot produce.
func (f fixture) ownedOrg(t *testing.T, name string, owner ids.UUID) ids.UUID {
	t.Helper()
	seat := ids.From[ids.UserKind](owner)
	org, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{
		DisplayName: name, OwnerID: &seat, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create organization %q: %v", name, err)
	}
	return ids.UUID(org.Id)
}

// "Owned by my team" is the form a manager's saved view actually takes, and the
// engine had only the person half of the dial until now. The team is walked
// through team_membership, so this is the whole membership edge under test and
// not just a column comparison.
func TestATeamFilterSelectsEveryMembersRecords(t *testing.T) {
	f := setupFixture(t)
	firstMember := f.ownedOrg(t, "Held by Rep1", f.e.Rep1)
	secondMember := f.ownedOrg(t, "Held by Rep2", f.e.Rep2)
	otherTeam := f.ownedOrg(t, "Held by Rep3", f.e.Rep3)

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "my team's accounts", EntityType: "organization", ListType: "dynamic",
		Definition: map[string]any{
			"field": "owner_team_id", "op": "eq", "value": f.e.Team1.String(),
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}

	got := memberIDs(t, f, list.ID)
	// Both members of the team, not just the one who happens to be the caller:
	// the dial names a team, and a filter that answered only the viewer's own
	// rows would be `owner_id` wearing a different label.
	if !got[firstMember] || !got[secondMember] {
		t.Errorf("the team filter missed a member's records: %v", got)
	}
	if got[otherTeam] {
		t.Error("the team filter reached another team's records")
	}
}

// `exists: false` on the team leaf finds the records no team's review covers, and
// an unowned record is one of them.
//
// The OTHER arm — an owner who exists and belongs to no team — is the one that
// distinguishes this leaf from `owner_id exists: false`, and it is asserted
// directly below rather than here, so that a change breaking one of the two
// reasons a record is uncovered names which one.
func TestAnUnownedRecordIsCoveredByNoTeam(t *testing.T) {
	f := setupFixture(t)
	inATeam := f.ownedOrg(t, "Held by Rep1", f.e.Rep1)
	unowned, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{
		DisplayName: "Nobody's account", Source: "manual",
	})
	if err != nil {
		t.Fatalf("create unowned organization: %v", err)
	}
	// The create stamps the seeding seat as owner; this test wants the
	// unowned state, so the owner is nulled explicitly.
	f.e.WsExec(t, "UPDATE organization SET owner_id = NULL WHERE id = $1", ids.UUID(unowned.Id))

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "no team covers these", EntityType: "organization", ListType: "dynamic",
		Definition: map[string]any{
			"field": "owner_team_id", "op": "exists", "value": false,
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}

	got := memberIDs(t, f, list.ID)
	if !got[ids.UUID(unowned.Id)] {
		t.Error("an account with no owner at all is not counted as covered by no team")
	}
	if got[inATeam] {
		t.Error("an account whose owner IS in a team is reported as covered by none")
	}
}

// The arm that distinguishes this leaf from `owner_id exists: false`: an owner
// who EXISTS and belongs to no team.
//
// A wrong implementation compiling the leaf to `t.owner_id IS NULL` passes every
// other assertion in this file — both spellings look plausible in a diff, and
// only a row whose owner is real but teamless tells them apart.
//
// No new fixture is needed for it, which is worth stating because the opposite
// was believed: the harness seeds FOUR app_user rows and gives memberships to
// three, so AdminUser is already a seat in no team. The precondition is asserted
// rather than assumed — the day somebody puts that seat in a team, this test says
// so in one line instead of failing as a filter bug.
func TestARecordWhoseOwnerIsInNoTeamIsCoveredByNoTeam(t *testing.T) {
	f := setupFixture(t)
	if inATeam := f.e.WsCount(t,
		"SELECT count(*) FROM team_membership WHERE user_id = $1", f.e.AdminUser); inATeam != 0 {
		t.Fatalf("the admin seat now belongs to %d team(s), so it can no longer stand for a teamless owner — give this test a seat that belongs to none", inATeam)
	}

	teamlessOwner := f.ownedOrg(t, "Held by a seat in no team", f.e.AdminUser)
	inATeam := f.ownedOrg(t, "Held by Rep1", f.e.Rep1)

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "no team covers these either", EntityType: "organization", ListType: "dynamic",
		Definition: map[string]any{
			"field": "owner_team_id", "op": "exists", "value": false,
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}

	got := memberIDs(t, f, list.ID)
	if !got[teamlessOwner] {
		t.Error("an account whose owner is real but belongs to no team is not counted as covered by no team — which is what the leaf compiling to `owner_id IS NULL` would produce")
	}
	if got[inATeam] {
		t.Error("an account whose owner IS in a team is reported as covered by none")
	}
	// The owner is genuinely there, so this is not the unowned arm wearing a
	// different name.
	if owned := f.e.WsCount(t,
		"SELECT count(*) FROM organization WHERE id = $1 AND owner_id IS NOT NULL", teamlessOwner); owned != 1 {
		t.Error("the record under test has no owner, so it proves the unowned arm a second time rather than the teamless-owner one")
	}
}

// An account can be a customer and a supplier at once, so the leaf selects
// accounts that are AT LEAST the named type. A filter that meant "is only this"
// would answer a different and much smaller question.
func TestARelationshipFilterSelectsAccountsThatAreAtLeastThatType(t *testing.T) {
	f := setupFixture(t)
	both := f.customerAndSupplier(t, "Acme, both")
	customerOnly := f.withRelationshipTypes(t, "Beta, customer only", []string{"customer"})

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "suppliers", EntityType: "organization", ListType: "dynamic",
		Definition: map[string]any{
			"field": "relationship_type", "op": "eq", "value": "supplier",
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}

	got := memberIDs(t, f, list.ID)
	if !got[both] {
		t.Error("an account that is a customer AND a supplier is not selected as a supplier")
	}
	if got[customerOnly] {
		t.Error("a customer with no supplier row is selected as a supplier")
	}
}

// A withdrawn relationship is one the account no longer has. Without the
// archived_at guard inside the wrapper, a segment would keep selecting accounts
// on a fact somebody deliberately took back.
func TestAWithdrawnRelationshipStopsMatching(t *testing.T) {
	f := setupFixture(t)
	account := f.customerAndSupplier(t, "Acme, both")

	list, err := f.lists.CreateList(f.ctx, collectionsmod.CreateListInput{
		Name: "suppliers", EntityType: "organization", ListType: "dynamic",
		Definition: map[string]any{
			"field": "relationship_type", "op": "eq", "value": "supplier",
		},
	})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	assertSoleMember(t, f, list.ID, account)

	// Through the real reconcile path: the desired live set is now customer
	// alone, which archives the supplier row rather than deleting it.
	f.setRelationshipTypes(t, account, []string{"customer"})

	if got := memberIDs(t, f, list.ID); got[account] {
		t.Error("an account whose supplier relationship was withdrawn is still selected as a supplier")
	}
}

// customerAndSupplier is the multi-valued fixture both relationship scenarios
// need, named once so they cannot drift into testing different shapes.
//
// Supplier rather than partner, and not arbitrarily: `partner` carries a product
// invariant of its own — an account is a partner only while a partner programme
// row backs it, and reconcile refuses the type without one. Borrowing that
// fixture would make these tests fail for a reason that has nothing to do with
// the vocabulary leaf under test.
func (f fixture) customerAndSupplier(t *testing.T, name string) ids.UUID {
	t.Helper()
	return f.withRelationshipTypes(t, name, []string{"customer", "supplier"})
}

func (f fixture) withRelationshipTypes(t *testing.T, name string, types []string) ids.UUID {
	t.Helper()
	org, err := f.people.CreateOrganization(f.ctx, peoplemod.CreateOrganizationInput{
		DisplayName: name, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create organization %q: %v", name, err)
	}
	f.setRelationshipTypes(t, ids.UUID(org.Id), types)
	return ids.UUID(org.Id)
}

// setRelationshipTypes drives the real update path, so the rows under test are
// the ones production writes — including the archive-not-delete behaviour a
// withdrawal depends on.
func (f fixture) setRelationshipTypes(t *testing.T, org ids.UUID, types []string) {
	t.Helper()
	if _, err := f.people.UpdateOrganization(
		f.ctx,
		ids.From[ids.OrganizationKind](org),
		peoplemod.UpdateOrganizationInput{RelationshipTypes: &types},
	); err != nil {
		t.Fatalf("set relationship types %v: %v", types, err)
	}
}

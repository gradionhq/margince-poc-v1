// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The two counts every company row carries (PO-EXT-10, AC-companies-2/3),
// proven over a real migrated Postgres: contact_count follows the
// current-primary employment edges the real writer makes, open_deal_count
// follows the 0065 view, the list and the single read agree, and a role
// without computed_field:read is shown a contact count but no deal count.

import (
	"encoding/json"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestOrganizationCounts_ListAndSingleReadAgreeWithTheEdges(t *testing.T) {
	e := Setup(t)
	acme := e.SeedOrg(t, "Acme Counts", nil)
	quiet := e.SeedOrg(t, "Quiet Counts", nil)
	staff := e.SeedPerson(t, "Works At Acme", nil)
	second := e.SeedPerson(t, "Also At Acme", nil)
	leaver := e.SeedPerson(t, "Left Acme", nil)

	employ := func(person, org ids.UUID, current bool) {
		t.Helper()
		personID := ids.From[ids.PersonKind](person)
		orgID := ids.From[ids.OrganizationKind](org)
		if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
			Kind: "employment", PersonID: &personID, OrganizationID: &orgID,
			IsCurrentPrimary: current, Source: "manual",
		}); err != nil {
			t.Fatalf("seeding the employment edge: %v", err)
		}
	}
	employ(staff, acme, true)
	employ(second, acme, true)
	// A past employer is not a contact: the column answers who works here.
	employ(leaver, acme, false)

	pipeline, open := pipelineFixtureFor(e.Admin(), t, e.Deals)
	for _, name := range []string{"D1", "D2", "D3"} {
		if _, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
			Name: name, AmountMinor: int64Ptr(1000), Currency: strPtr("EUR"),
			PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(acme)), Source: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}

	byID := func(rows []crmcontracts.Organization, id ids.UUID) crmcontracts.Organization {
		t.Helper()
		for _, o := range rows {
			if ids.UUID(o.Id) == id {
				return o
			}
		}
		t.Fatalf("organization %s missing from the page", id)
		return crmcontracts.Organization{}
	}
	page, _, err := e.People.ListOrganizations(e.Admin(), people.ListOrganizationsInput{})
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, "list acme", byID(page, acme), 2, 3)
	// Zero is a number here, not an absence: a reader must be able to tell
	// "no contacts" from "not shown".
	assertCounts(t, "list quiet", byID(page, quiet), 0, 0)

	single, err := e.People.GetOrganization(e.Admin(), orgIDOf(acme), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, "single acme", single, 2, 3)
}

// A role without computed_field:read is STATE-4 for the deal count: the key
// is absent from the wire, not zero. The contact count is an edge fact and
// stays.
func TestOrganizationCounts_UngatedRoleSeesContactsButNoDealCount(t *testing.T) {
	e := Setup(t)
	acme := e.SeedOrg(t, "Gated Counts", nil)
	staff := e.SeedPerson(t, "Works At Gated", nil)
	pID := ids.From[ids.PersonKind](staff)
	oID := ids.From[ids.OrganizationKind](acme)
	if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
		Kind: "employment", PersonID: &pID, OrganizationID: &oID, IsCurrentPrimary: true, Source: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	ctx := e.As(e.Rep1, nil, computedFieldNoGrantPerms)

	page, _, err := e.People.ListOrganizations(ctx, people.ListOrganizationsInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) == 0 {
		t.Fatal("the ungated role must still list organizations")
	}
	var got *crmcontracts.Organization
	for i := range page {
		if ids.UUID(page[i].Id) == acme {
			got = &page[i]
		}
	}
	if got == nil {
		t.Fatal("seeded organization missing from the ungated page")
	}
	if got.ContactCount == nil || *got.ContactCount != 1 {
		t.Fatalf("contact_count = %v, want 1 for the ungated role", got.ContactCount)
	}
	if got.OpenDealCount != nil {
		t.Fatalf("open_deal_count = %d, want it withheld for a role without computed_field:read", *got.OpenDealCount)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["open_deal_count"]; present {
		t.Fatalf("want the open_deal_count KEY absent from the wire, got %v", wire["open_deal_count"])
	}
	if _, present := wire["contact_count"]; !present {
		t.Fatal("want contact_count on the wire for the ungated role")
	}
}

// The contact count is a read under the caller's person row scope: a number
// that moved when a colleague captured a private contact would disclose that
// contact. The deal count is the account's whole open pipeline, as the
// company page's tile sums it (PO-EXT-10, founder decision 2026-08-18).
func TestOrganizationCounts_FollowTheCallersRowScope(t *testing.T) {
	e := Setup(t)
	// The account is unowned, so it is visible at every scope tier.
	acme := e.SeedOrg(t, "Shared Counts", nil)
	mine := e.SeedPerson(t, "Rep1 Contact", &e.Rep1)
	theirs := e.SeedPerson(t, "Rep3 Contact", &e.Rep3)
	employ := func(person ids.UUID) {
		t.Helper()
		personID := ids.From[ids.PersonKind](person)
		orgID := ids.From[ids.OrganizationKind](acme)
		if _, err := e.People.CreateRelationship(e.Admin(), people.CreateRelationshipInput{
			Kind: "employment", PersonID: &personID, OrganizationID: &orgID,
			IsCurrentPrimary: true, Source: "manual",
		}); err != nil {
			t.Fatalf("seeding the employment edge: %v", err)
		}
	}
	employ(mine)
	employ(theirs)

	pipeline, open := pipelineFixtureFor(e.Admin(), t, e.Deals)
	for _, owner := range []ids.UUID{e.Rep1, e.Rep3} {
		ownerID := ids.From[ids.UserKind](owner)
		if _, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
			Name: "Deal of " + owner.String(), AmountMinor: int64Ptr(1000), Currency: strPtr("EUR"),
			PipelineID: pipeline, StageID: open, OrganizationID: orgIDPtr(orgIDOf(acme)),
			OwnerID: &ownerID, Source: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}

	admin, err := e.People.GetOrganization(e.Admin(), orgIDOf(acme), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, "admin sees the workspace", admin, 2, 2)

	perms := rollupOrgReadPerms(principal.RowScopeOwn)
	perms.Objects["computed_field"] = principal.ObjectGrant{Read: true}
	perms.Objects["person"] = principal.ObjectGrant{Read: true}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)
	own, err := e.People.GetOrganization(rep, orgIDOf(acme), storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertCounts(t, "own-scope rep: own contacts, whole pipeline", own, 1, 2)
	page, _, err := e.People.ListOrganizations(rep, people.ListOrganizationsInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range page {
		if ids.UUID(o.Id) == acme {
			assertCounts(t, "own-scope rep on the list", o, 1, 2)
			return
		}
	}
	t.Fatal("shared account missing from the rep's page")
}

func assertCounts(t *testing.T, label string, o crmcontracts.Organization, contacts, deals int) {
	t.Helper()
	if o.ContactCount == nil {
		t.Fatalf("%s: contact_count absent, want %d", label, contacts)
	}
	if *o.ContactCount != contacts {
		t.Fatalf("%s: contact_count = %d, want %d", label, *o.ContactCount, contacts)
	}
	if o.OpenDealCount == nil {
		t.Fatalf("%s: open_deal_count absent, want %d", label, deals)
	}
	if *o.OpenDealCount != deals {
		t.Fatalf("%s: open_deal_count = %d, want %d", label, *o.OpenDealCount, deals)
	}
}

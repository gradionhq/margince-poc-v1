// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A deal may only be attributed to a company that IS a partner.
//
// The commission ledger prices from the partner row's margin tier, so a deal
// pointed at an ordinary customer reads as credited and can never earn
// anything — the failure is silent, which is what makes it worth a gate rather
// than a note. Both write paths carry it, because a rule one verb enforces and
// the other does not is not a rule.

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestADealMayOnlyNameARealPartner(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	admin := e.As(e.AdminUser, nil, commissionAdminPerms)

	// Two ordinary companies. One is made a partner; the other stays a plain
	// customer, which is exactly what somebody picks by mistake.
	partnerOrg := orgIDOf(e.SeedOrg(t, "Northgate Partners", nil))
	plainOrg := orgIDOf(e.SeedOrg(t, "Just A Customer", nil))
	tier := "tier2_20"
	if _, err := e.People.UpsertPartner(admin, people.UpsertPartnerInput{
		OrganizationID: partnerOrg,
		PartnerRole:    "consulting",
		MarginTier:     &tier,
	}); err != nil {
		t.Fatalf("making the organization a partner: %v", err)
	}

	t.Run("create refuses a company that is not a partner", func(t *testing.T) {
		_, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
			Name: "Misattributed", PipelineID: pipeline, StageID: open, Source: "ui",
			PartnerOrganizationID: &plainOrg,
		})
		var notPartner *people.NotAPartnerError
		if !errors.As(err, &notPartner) {
			t.Fatalf("CreateDeal naming a non-partner → %v, want NotAPartnerError", err)
		}
		// The refusal names the field the caller can act on, and says what to
		// do — a bare 422 would leave them guessing which of two org fields
		// was wrong.
		field, code, _ := notPartner.FieldFault()
		if field != "partner_org_id" || code != "not_a_partner" {
			t.Errorf("fault = (%s, %s), want (partner_org_id, not_a_partner)", field, code)
		}
	})

	t.Run("update refuses a company that is not a partner", func(t *testing.T) {
		deal := ids.From[ids.DealKind](e.SeedDeal(t, "Repointed", pipeline, open, &e.Rep1))
		_, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{
			PartnerOrganizationID: &plainOrg,
		})
		var notPartner *people.NotAPartnerError
		if !errors.As(err, &notPartner) {
			t.Fatalf("UpdateDeal naming a non-partner → %v, want NotAPartnerError", err)
		}
	})

	// The gate narrows what may be named; it does not break the feature. A
	// refusal test that never admits anything passes just as happily against an
	// authority that refuses everyone.
	t.Run("both paths still accept a real partner", func(t *testing.T) {
		created, err := e.Deals.CreateDeal(admin, deals.CreateDealInput{
			Name: "Sourced properly", PipelineID: pipeline, StageID: open, Source: "ui",
			PartnerOrganizationID: &partnerOrg,
		})
		if err != nil {
			t.Fatalf("CreateDeal naming a real partner → %v, want ok", err)
		}
		if created.PartnerOrgId == nil {
			t.Error("the partner named at birth did not reach the row")
		}

		deal := ids.From[ids.DealKind](e.SeedDeal(t, "Repointed properly", pipeline, open, &e.Rep1))
		updated, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{
			PartnerOrganizationID: &partnerOrg,
		})
		if err != nil {
			t.Fatalf("UpdateDeal naming a real partner → %v, want ok", err)
		}
		if updated.PartnerOrgId == nil {
			t.Error("the partner named on update did not reach the row")
		}
	})
}

// Merging one company into another repoints its deals' partners with raw SQL,
// outside the store and so outside the check above. That is safe only because
// the merge carries the partner row to the survivor as well — this holds it to
// that, because a merge that moved the deals and left the programme behind
// would orphan every attribution silently, which is the exact failure the
// check exists to prevent.
func TestMergingAPartnerLeavesItsDealsNamingAPartner(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	admin := e.As(e.AdminUser, nil, commissionAdminPerms)

	tier := "tier2_20"
	source := e.SeedPartnerOrg(t, "Partner Source", &tier, nil)
	target := e.SeedOrg(t, "Plain Target", nil)
	sourceID := ids.From[ids.OrganizationKind](source)

	deal := ids.From[ids.DealKind](e.SeedDeal(t, "Sourced by the merged partner", pipeline, open, &e.Rep1))
	if _, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{
		PartnerOrganizationID: &sourceID,
	}); err != nil {
		t.Fatalf("attributing the deal to the source partner: %v", err)
	}

	if _, err := e.People.MergeOrganization(e.Admin(), orgIDOf(source), orgIDOf(target)); err != nil {
		t.Fatalf("merging the partner into the plain company: %v", err)
	}

	// The deal now names the survivor, and the survivor carries the programme
	// — so the attribution the merge produced is one the store would accept.
	moved, err := e.Deals.GetDeal(admin, deal, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("reading the merged deal: %v", err)
	}
	if moved.PartnerOrgId == nil || ids.UUID(*moved.PartnerOrgId) != target {
		t.Fatalf("partner = %v, want the survivor %v", moved.PartnerOrgId, target)
	}

	// The survivor is asked through the store rather than by reading the table:
	// re-naming it must be ACCEPTED, which is the same question the check asks
	// and the one that matters — a merge that left an attribution the product
	// would now refuse has broken the invariant even if the row looks fine.
	survivor := orgIDOf(target)
	if _, err := e.Deals.UpdateDeal(admin, deal, deals.UpdateDealInput{
		PartnerOrganizationID: &survivor,
	}); err != nil {
		t.Errorf("re-naming the survivor as partner → %v; the merge left an attribution the store refuses", err)
	}
}

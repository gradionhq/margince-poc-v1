// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A deal is customer identity: every seat of the workspace reads every deal.
// The records it POINTS AT are not — an organization can be capture-private to
// the colleague who captured it, and a project keeps its own own/team scope. So
// the deal's references are withheld from a reader who could not open them, and
// named in masked_fields, exactly as the write path already refuses to SET a
// reference the caller cannot see (auth.EnsureLinkTarget).

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dealReferenceFixture is one deal pointing at a hidden organization pair and
// one pointing at an out-of-scope project, both linked through the real writer.
type dealReferenceFixture struct {
	hiddenRefs ids.DealID
	hiddenProj ids.DealID
	openOrg    ids.UUID
	privateOrg ids.UUID
	wonStage   ids.StageID
}

func seedDealReferenceFixture(t *testing.T, e *Env) dealReferenceFixture {
	t.Helper()
	pipeline, open, won := DealFixture(t, e)
	admin := e.Admin()

	// Seeded workspace-visible so the admin's link write passes its own
	// EnsureLinkTarget gate; capture privacy lands afterwards, which is the
	// order a connector-captured contact reaches this state in anyway.
	privateOrg := e.SeedOrg(t, "Meridian Labs", &e.Rep3)
	partnerOrg := e.SeedPartnerOrg(t, "Northgate Partners", nil, &e.Rep3)
	openOrg := e.SeedOrg(t, "Kestrel Foods", nil)

	hiddenRefs := ids.From[ids.DealKind](e.SeedDeal(t, "Meridian renewal", pipeline, open, &e.Rep1))
	privateOrgID, partnerOrgID := orgIDOf(privateOrg), orgIDOf(partnerOrg)
	if _, err := e.Deals.UpdateDeal(admin, hiddenRefs, deals.UpdateDealInput{
		OrganizationID:        &privateOrgID,
		PartnerOrganizationID: &partnerOrgID,
	}); err != nil {
		t.Fatalf("linking the deal to its organizations: %v", err)
	}
	e.MakeCapturePrivate(t, "organization", privateOrg, e.Rep3)
	e.MakeCapturePrivate(t, "organization", partnerOrg, e.Rep3)

	// The project is Team2's; a deal and its project must name the same
	// company, so the anchor org stays workspace-visible and only the project
	// is out of Rep1's reach.
	project := seedProject(admin, t, e, "Kestrel rollout", strPtr("KES-1"), openOrg, &e.Rep3)
	hiddenProj := ids.From[ids.DealKind](e.SeedDeal(t, "Kestrel expansion", pipeline, open, &e.Rep1))
	openOrgID := orgIDOf(openOrg)
	if _, err := e.Deals.UpdateDeal(admin, hiddenProj, deals.UpdateDealInput{
		OrganizationID: &openOrgID,
		ProjectID:      &project.ID,
	}); err != nil {
		t.Fatalf("linking the deal to its project: %v", err)
	}
	return dealReferenceFixture{
		hiddenRefs: hiddenRefs, hiddenProj: hiddenProj,
		openOrg: openOrg, privateOrg: privateOrg, wonStage: won,
	}
}

func TestADealDoesNotNameRecordsItsReaderCannotRead(t *testing.T) {
	e := Setup(t)
	fx := seedDealReferenceFixture(t, e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)

	// The reader can open neither organization, so neither id is handed back.
	got, err := e.Deals.GetDeal(rep, fx.hiddenRefs, 0)
	if err != nil {
		t.Fatalf("a rep reading a deal whose organizations are private: %v", err)
	}
	if got.OrganizationId != nil {
		t.Errorf("organization_id = %v, want withheld: it names a capture-private organization the reader cannot open", got.OrganizationId)
	}
	if got.PartnerOrgId != nil {
		t.Errorf("partner_org_id = %v, want withheld", got.PartnerOrgId)
	}
	assertMaskNames(t, got, "organization_id", "partner_org_id")

	// The project is out of the reader's row scope; its anchor company is not.
	proj, err := e.Deals.GetDeal(rep, fx.hiddenProj, 0)
	if err != nil {
		t.Fatalf("a rep reading a deal whose project is another team's: %v", err)
	}
	if proj.ProjectId != nil {
		t.Errorf("project_id = %v, want withheld: the project is outside the reader's row scope", proj.ProjectId)
	}
	if proj.OrganizationId == nil || ids.UUID(*proj.OrganizationId) != fx.openOrg {
		t.Errorf("organization_id = %v, want the workspace-visible company the reader CAN open", proj.OrganizationId)
	}
	assertMaskNames(t, proj, "project_id")

	// A reader who can see all three still receives all three.
	full, err := e.Deals.GetDeal(e.Admin(), fx.hiddenProj, 0)
	if err != nil || full.ProjectId == nil || full.OrganizationId == nil || full.MaskedFields != nil {
		t.Errorf("the admin's read = org %v project %v masked %v (%v), want every reference", full.OrganizationId, full.ProjectId, full.MaskedFields, err)
	}
}

// TestTheDealListWithholdsTheSameReferencesAsTheGet proves the page path, not
// only the single-row one: a list is where an existence oracle is cheapest.
func TestTheDealListWithholdsTheSameReferencesAsTheGet(t *testing.T) {
	e := Setup(t)
	fx := seedDealReferenceFixture(t, e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)

	page, _, err := e.Deals.ListDeals(rep, deals.ListDealsInput{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[ids.UUID]bool{}
	for i := range page {
		d := page[i]
		seen[ids.UUID(d.Id)] = true
		switch ids.UUID(d.Id) {
		case fx.hiddenRefs.UUID:
			if d.OrganizationId != nil || d.PartnerOrgId != nil {
				t.Errorf("the list handed out a private organization: org %v partner %v", d.OrganizationId, d.PartnerOrgId)
			}
			assertMaskNames(t, d, "organization_id", "partner_org_id")
		case fx.hiddenProj.UUID:
			if d.ProjectId != nil {
				t.Errorf("the list handed out another team's project: %v", d.ProjectId)
			}
		}
	}
	if !seen[fx.hiddenRefs.UUID] || !seen[fx.hiddenProj.UUID] {
		t.Errorf("the list shows %v, want both deals — withholding a reference must not drop the row", seen)
	}
}

// assertMaskNames checks masked_fields carries exactly the given names: a null
// a reader cannot distinguish from an empty field is the half-fix this whole
// seam exists to avoid.
func assertMaskNames(t *testing.T, d crmcontracts.Deal, want ...string) {
	t.Helper()
	if d.MaskedFields == nil {
		t.Errorf("masked_fields is absent, want %v named — a withheld null must say it was withheld", want)
		return
	}
	got := map[string]bool{}
	for _, f := range *d.MaskedFields {
		got[f] = true
	}
	for _, f := range want {
		if !got[f] {
			t.Errorf("masked_fields = %v, want it to name %s", *d.MaskedFields, f)
		}
	}
	if len(got) != len(want) {
		t.Errorf("masked_fields = %v, want exactly %v", *d.MaskedFields, want)
	}
}

// A mutation RESPONSE is a read. Every entry point that hands a deal back must
// withhold the same references the GET does, or a no-op PATCH becomes the
// second door onto the id the GET just refused — and the "it lifts on write
// authority" argument the amount mask makes is not available here: being
// allowed to change the DEAL says nothing about being allowed to read the
// ORGANIZATION it names.
//
// A table over the entry points rather than one case each, so a sixth deal
// mutation is a compile-time addition to this list, not a silent omission.
func TestEveryDealMutationResponseWithholdsTheSameReferences(t *testing.T) {
	e := Setup(t)
	fx := seedDealReferenceFixture(t, e)

	perms := AccountRepPerms
	perms.Objects = map[string]principal.ObjectGrant{
		"deal":                  {Create: true, Read: true, Update: true, Delete: true},
		"organization":          {Read: true},
		"pipeline":              {Read: true},
		"installation_settings": {Read: true},
	}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)

	renamed := "Meridian renewal, retitled"
	// A won deal owes a contract or a reason there is none; this suite is about
	// the response's references, not that rule, so it satisfies it and moves on.
	wonWithout := "imported"
	cases := []struct {
		name string
		call func() (crmcontracts.Deal, error)
	}{
		{"a patch that changes nothing still echoes the row", func() (crmcontracts.Deal, error) {
			return e.Deals.UpdateDeal(rep, fx.hiddenRefs, deals.UpdateDealInput{})
		}},
		{"a patch that changes something", func() (crmcontracts.Deal, error) {
			return e.Deals.UpdateDeal(rep, fx.hiddenRefs, deals.UpdateDealInput{Name: &renamed})
		}},
		{"advancing the deal", func() (crmcontracts.Deal, error) {
			return e.Deals.AdvanceDeal(rep, fx.hiddenRefs, deals.AdvanceDealInput{ToStageID: fx.wonStage, WonWithoutContractReason: &wonWithout})
		}},
		{"archiving the deal", func() (crmcontracts.Deal, error) {
			return e.Deals.ArchiveDeal(rep, fx.hiddenRefs)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got.OrganizationId != nil || got.PartnerOrgId != nil {
				t.Errorf("%s handed back org %v partner %v, want both withheld",
					tc.name, got.OrganizationId, got.PartnerOrgId)
			}
			assertMaskNames(t, got, "organization_id", "partner_org_id")
		})
	}
}

// Filtering by an id is asking whether it is there, so the list must not
// confirm through its filter what its projection withholds. The answer is the
// empty page a company with no deals gives — never a 404, which would itself
// say the organization is real.
func TestFilteringByAnUnreadableReferenceAnswersTheEmptyPage(t *testing.T) {
	e := Setup(t)
	fx := seedDealReferenceFixture(t, e)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, AccountRepPerms)

	hiddenOrg := ids.From[ids.OrganizationKind](fx.privateOrg)
	page, _, err := e.Deals.ListDeals(rep, deals.ListDealsInput{OrganizationID: &hiddenOrg})
	if err != nil {
		t.Fatalf("filtering by an organization the caller cannot read: %v", err)
	}
	if len(page) != 0 {
		t.Errorf("the filter returned %d deal(s), want none — an unnarrowed filter confirms the binding the read withholds", len(page))
	}

	// The same filter for a company the reader CAN open still works, or the
	// fix would have closed the oracle by breaking the feature.
	openOrg := ids.From[ids.OrganizationKind](fx.openOrg)
	visible, _, err := e.Deals.ListDeals(rep, deals.ListDealsInput{OrganizationID: &openOrg})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || ids.UUID(visible[0].Id) != fx.hiddenProj.UUID {
		t.Errorf("filtering by a readable company returned %d deal(s), want the one deal on it", len(visible))
	}
}

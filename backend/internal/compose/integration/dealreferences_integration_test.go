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
)

// dealReferenceFixture is one deal pointing at a hidden organization pair and
// one pointing at an out-of-scope project, both linked through the real writer.
type dealReferenceFixture struct {
	hiddenRefs ids.DealID
	hiddenProj ids.DealID
	openOrg    ids.UUID
}

func seedDealReferenceFixture(t *testing.T, e *Env) dealReferenceFixture {
	t.Helper()
	pipeline, open, _ := DealFixture(t, e)
	admin := e.Admin()

	// Seeded workspace-visible so the admin's link write passes its own
	// EnsureLinkTarget gate; capture privacy lands afterwards, which is the
	// order a connector-captured contact reaches this state in anyway.
	privateOrg := e.SeedOrg(t, "Meridian Labs", &e.Rep3)
	partnerOrg := e.SeedOrg(t, "Northgate Partners", &e.Rep3)
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
	return dealReferenceFixture{hiddenRefs: hiddenRefs, hiddenProj: hiddenProj, openOrg: openOrg}
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

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// partner_org_id and partner_attribution are one fact stored in two columns,
// and the schema's deal_partner_attribution_pairing CHECK rejects either half
// alone. These tests hold the store to that rule BEFORE the database sees the
// row, because a caller deserves "you left out the partner" rather than a
// constraint violation.

import (
	"errors"
	"os"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// attributionMigration is the migration that introduced the pair, read here so
// the vocabulary this package enforces is checked against the vocabulary the
// table actually admits rather than against a second copy of the list.
const attributionMigration = "../../../migrations/core/1787224728_deal_partner_attribution.up.sql"

func TestMigrationAdmitsExactlyTheAttributionsTheStoreAccepts(t *testing.T) {
	raw, err := os.ReadFile(attributionMigration)
	if err != nil {
		t.Fatalf("reading the attribution migration: %v", err)
	}
	sql := string(raw)
	for _, v := range []string{attributionSourced, attributionInfluenced} {
		if !strings.Contains(sql, "'"+v+"'") {
			t.Errorf("the store accepts %q but the migration's CHECK does not admit it", v)
		}
	}
	if err := validPartnerAttribution("partner_of"); err == nil {
		t.Error("a value outside the two-word vocabulary was accepted; the CHECK would reject the row")
	}
}

// orgIDPtr builds the id argument a caller supplies when naming a partner.
func orgIDPtr(t *testing.T) *ids.OrganizationID {
	t.Helper()
	id := ids.New[ids.OrganizationKind]()
	return &id
}

// dealNamingPartner is a stored deal that already carries both halves of the
// pair — the pre-image an update patches against.
func dealNamingPartner(attribution string) crmcontracts.Deal {
	partner := openapi_types.UUID(ids.New[ids.OrganizationKind]().UUID)
	return crmcontracts.Deal{PartnerOrgId: &partner, PartnerAttribution: &attribution}
}

// What a deal claims about the partner it names, for each way a caller can
// leave the claim unsaid. The link itself goes through auth.EnsureLinkTarget,
// which needs a real transaction — the integration lane covers that half; this
// covers the decision it feeds.
func TestWhatADealClaimsAboutThePartnerItNames(t *testing.T) {
	influenced := attributionInfluenced
	for name, tc := range map[string]struct {
		current crmcontracts.Deal
		in      UpdateDealInput
		want    string
	}{
		"a bare partner link is the sourced motion": {
			current: crmcontracts.Deal{},
			in:      UpdateDealInput{PartnerOrganizationID: orgIDPtr(t)},
			want:    attributionSourced,
		},
		"an explicit claim wins over the default": {
			current: crmcontracts.Deal{},
			in:      UpdateDealInput{PartnerOrganizationID: orgIDPtr(t), PartnerAttribution: &influenced},
			want:    attributionInfluenced,
		},
		"re-pointing at another partner keeps the claim already made": {
			current: dealNamingPartner(attributionInfluenced),
			in:      UpdateDealInput{PartnerOrganizationID: orgIDPtr(t)},
			want:    attributionInfluenced,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := resolvedAttribution(tc.current, tc.in); got != tc.want {
				t.Errorf("attribution = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAttributionWithoutAPartnerIsRefused(t *testing.T) {
	p := storekit.NewPatch()
	sourced := attributionSourced
	in := UpdateDealInput{PartnerAttribution: &sourced}

	err := applyPartnerAttributionPatch(t.Context(), nil, crmcontracts.Deal{}, in, p)

	var unpaired *PartnerAttributionUnpairedError
	if !errors.As(err, &unpaired) {
		t.Fatalf("error = %v, want PartnerAttributionUnpairedError — there is no partner to attribute this to", err)
	}
	field, code, _ := unpaired.FieldFault()
	if field != partnerAttributionField || code != "partner_attribution_unpaired" {
		t.Errorf("fault = (%s, %s), want (%s, partner_attribution_unpaired)", field, code, partnerAttributionField)
	}
	if _, set := p.After()[partnerAttributionField]; set {
		t.Error("a refused attribution still reached the patch")
	}
}

func TestReAttributingADealThatAlreadyNamesAPartnerMovesOnlyTheClaim(t *testing.T) {
	p := storekit.NewPatch()
	influenced := attributionInfluenced
	in := UpdateDealInput{PartnerAttribution: &influenced}

	if err := applyPartnerAttributionPatch(t.Context(), nil, dealNamingPartner(attributionSourced), in, p); err != nil {
		t.Fatalf("re-attributing a deal that names a partner: %v", err)
	}
	if got := p.After()[partnerAttributionField]; got != attributionInfluenced {
		t.Errorf("attribution = %v, want %q", got, attributionInfluenced)
	}
	if _, moved := p.After()["partner_org_id"]; moved {
		t.Error("the partner link moved; only the attribution was being changed")
	}
}

func TestAnUnknownAttributionIsRefusedBeforeTheDatabaseSeesIt(t *testing.T) {
	p := storekit.NewPatch()
	bogus := "co_sold"
	// The vocabulary is checked before the link is resolved, so this refusal
	// does not depend on a transaction being present.
	in := UpdateDealInput{PartnerAttribution: &bogus}

	err := applyPartnerAttributionPatch(t.Context(), nil, dealNamingPartner(attributionSourced), in, p)

	var invalid *PartnerAttributionValueError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want PartnerAttributionValueError", err)
	}
	if _, code, _ := invalid.FieldFault(); code != "partner_attribution_invalid" {
		t.Errorf("code = %s, want partner_attribution_invalid", code)
	}
	if _, set := p.After()[partnerAttributionField]; set {
		t.Error("a refused attribution still reached the patch")
	}
}

func TestTouchingNeitherHalfLeavesThePairAlone(t *testing.T) {
	p := storekit.NewPatch()

	if err := applyPartnerAttributionPatch(t.Context(), nil, dealNamingPartner(attributionSourced), UpdateDealInput{}, p); err != nil {
		t.Fatalf("an update naming neither half: %v", err)
	}
	if len(p.After()) != 0 {
		t.Errorf("patch wrote %v; an update that mentions no partner field must not touch the pair", p.After())
	}
}

func TestAWithheldPartnerTakesItsAttributionWithIt(t *testing.T) {
	d := dealNamingPartner(attributionSourced)

	withheldFields{filterPartnerOrgID}.applyTo(&d)

	if d.PartnerAttribution != nil {
		t.Errorf("attribution = %q survived a withheld partner — it discloses that SOME partner sourced the deal", *d.PartnerAttribution)
	}
	if d.PartnerOrgId != nil {
		t.Error("the partner link survived its own mask")
	}
}

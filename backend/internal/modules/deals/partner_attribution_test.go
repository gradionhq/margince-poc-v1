// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// partner_org_id and partner_attribution are one fact stored in two columns,
// and the schema's deal_partner_attribution_pairing CHECK rejects either half
// alone. These tests hold the store to that rule BEFORE the database sees the
// row, because a caller deserves "you left out the partner" rather than a
// constraint violation.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attributionMigration is the migration that introduced the pair, read here so
// the vocabulary this package enforces is checked against the vocabulary the
// table actually admits rather than against a second copy of the list.
const attributionMigration = "../../../migrations/core/1787226902_deal_partner_attribution.up.sql"

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

// unreachablePartnerCheck is the seam for a path that must not reach it: every
// case below is refused, or leaves the pair alone, BEFORE the partner is
// resolved. A call here means the refusal moved after the database read it was
// meant to happen instead of.
func unreachablePartnerCheck(t *testing.T) EnsurePartner {
	t.Helper()
	return func(context.Context, pgx.Tx, ids.OrganizationID) error {
		t.Error("the partner check ran on a path that never names a valid partner")
		return nil
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
	claim := crmcontracts.DealPartnerAttribution(attribution)
	return crmcontracts.Deal{PartnerOrgId: &partner, PartnerAttribution: &claim}
}

// samePartnerRestated is an update that names the partner the deal already
// carries — a no-op on the link, which must not be read as a change of partner.
func samePartnerRestated(attribution string) struct {
	current crmcontracts.Deal
	in      UpdateDealInput
} {
	current := dealNamingPartner(attribution)
	same := ids.From[ids.OrganizationKind](ids.UUID(*current.PartnerOrgId))
	return struct {
		current crmcontracts.Deal
		in      UpdateDealInput
	}{current: current, in: UpdateDealInput{PartnerOrganizationID: &same}}
}

// What a deal claims about the partner it names, for each way a caller can
// leave the claim unsaid. The link itself goes through auth.EnsureLinkTarget,
// which needs a real transaction — the integration lane covers that half; this
// covers the decision it feeds.
func TestWhatADealClaimsAboutThePartnerItNames(t *testing.T) {
	influenced := attributionInfluenced
	keptPartner := samePartnerRestated(attributionInfluenced)
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
		// An attribution describes a PARTNER, so it does not follow the deal to
		// whoever is named next: inheriting "influenced" would decide that the
		// new partner earns nothing on the strength of a claim about somebody
		// else.
		"pointing at a DIFFERENT partner starts the claim over": {
			current: dealNamingPartner(attributionInfluenced),
			in:      UpdateDealInput{PartnerOrganizationID: orgIDPtr(t)},
			want:    attributionSourced,
		},
		"naming the partner the deal already has keeps its claim": {
			current: keptPartner.current,
			in:      keptPartner.in,
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

	err := applyPartnerAttributionPatch(t.Context(), nil, crmcontracts.Deal{}, in, p, unreachablePartnerCheck(t))

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

func TestAnUnknownAttributionIsRefusedBeforeTheDatabaseSeesIt(t *testing.T) {
	p := storekit.NewPatch()
	bogus := "co_sold"
	// The vocabulary is checked before the link is resolved, so this refusal
	// does not depend on a transaction being present.
	in := UpdateDealInput{PartnerAttribution: &bogus}

	err := applyPartnerAttributionPatch(t.Context(), nil, dealNamingPartner(attributionSourced), in, p, unreachablePartnerCheck(t))

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

	if err := applyPartnerAttributionPatch(t.Context(), nil, dealNamingPartner(attributionSourced), UpdateDealInput{}, p, unreachablePartnerCheck(t)); err != nil {
		t.Fatalf("an update naming neither half: %v", err)
	}
	if len(p.After()) != 0 {
		t.Errorf("patch wrote %v; an update that mentions no partner field must not touch the pair", p.After())
	}
}

// A deal can name its partner at birth. The rules are the update path's, minus
// the pre-image: there is no earlier claim for a newborn deal to keep.
func TestWhatANewbornDealClaimsAboutThePartnerItNames(t *testing.T) {
	influenced := attributionInfluenced
	for name, tc := range map[string]struct {
		in   CreateDealInput
		want *string
	}{
		"a bare partner link is the sourced motion": {
			in:   CreateDealInput{PartnerOrganizationID: orgIDPtr(t)},
			want: &[]string{attributionSourced}[0],
		},
		"an explicit claim wins over the default": {
			in:   CreateDealInput{PartnerOrganizationID: orgIDPtr(t), PartnerAttribution: &influenced},
			want: &influenced,
		},
		// The pairing CHECK admits both columns populated or neither, so a deal
		// born without a partner must carry no attribution at all.
		"no partner leaves both halves empty": {
			in:   CreateDealInput{},
			want: nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := birthAttribution(tc.in)
			if err != nil {
				t.Fatalf("birthAttribution: %v", err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("attribution = %q, want none — the pair must be empty together", *got)
			case tc.want != nil && got == nil:
				t.Errorf("attribution = none, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("attribution = %q, want %q", *got, *tc.want)
			}
		})
	}
}

func TestANewbornDealsAttributionWithoutAPartnerIsRefused(t *testing.T) {
	sourced := attributionSourced

	_, err := birthAttribution(CreateDealInput{PartnerAttribution: &sourced})

	var unpaired *PartnerAttributionUnpairedError
	if !errors.As(err, &unpaired) {
		t.Fatalf("error = %v, want PartnerAttributionUnpairedError — there is no partner to attribute this to", err)
	}
	if field, code, _ := unpaired.FieldFault(); field != partnerAttributionField || code != "partner_attribution_unpaired" {
		t.Errorf("fault = (%s, %s), want (%s, partner_attribution_unpaired)", field, code, partnerAttributionField)
	}
}

func TestANewbornDealsUnknownAttributionIsRefusedBeforeTheDatabaseSeesIt(t *testing.T) {
	bogus := "co_sold"

	_, err := birthAttribution(CreateDealInput{PartnerOrganizationID: orgIDPtr(t), PartnerAttribution: &bogus})

	var invalid *PartnerAttributionValueError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want PartnerAttributionValueError", err)
	}
	if _, code, _ := invalid.FieldFault(); code != "partner_attribution_invalid" {
		t.Errorf("code = %s, want partner_attribution_invalid", code)
	}
}

// The create body's partner fields must reach the store input. They were once
// absent from CreateDealRequest entirely, and because the schema admits extra
// top-level keys the request was accepted with 201 while both fields were
// dropped — a caller told its write had succeeded when the partner was gone.
func TestACreateBodyCarriesItsPartnerThroughToTheStore(t *testing.T) {
	partner := openapi_types.UUID(ids.New[ids.OrganizationKind]().UUID)
	claim := crmcontracts.CreateDealRequestPartnerAttribution(attributionInfluenced)

	in, err := dealCreateInput(crmcontracts.CreateDealRequest{
		Name:               "Northgate rollout",
		PipelineId:         openapi_types.UUID(ids.New[ids.PipelineKind]().UUID),
		StageId:            openapi_types.UUID(ids.New[ids.StageKind]().UUID),
		Source:             "ui",
		PartnerOrgId:       &partner,
		PartnerAttribution: &claim,
	})
	if err != nil {
		t.Fatalf("dealCreateInput: %v", err)
	}
	if in.PartnerOrganizationID == nil {
		t.Fatal("the partner named in the body never reached the store input")
	}
	if ids.UUID(partner) != in.PartnerOrganizationID.UUID {
		t.Errorf("partner = %v, want %v", in.PartnerOrganizationID.UUID, ids.UUID(partner))
	}
	if in.PartnerAttribution == nil || *in.PartnerAttribution != attributionInfluenced {
		t.Errorf("attribution = %v, want %q", in.PartnerAttribution, attributionInfluenced)
	}
	// The declared fields are consumed by name, so they must not ALSO arrive as
	// custom fields — that is the path that silently dropped them.
	for _, key := range []string{"partner_org_id", partnerAttributionField} {
		if _, stray := in.CustomFields[key]; stray {
			t.Errorf("%s reached CustomFields; a declared field must not fall through to the catalog", key)
		}
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

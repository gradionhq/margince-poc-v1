// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"errors"
	"reflect"
	"testing"
)

func TestCompareCompanySiteReadClassifiesEveryRefreshRelationship(t *testing.T) {
	read := SiteRead{
		ProfileFields: []DeepReadField{
			{Field: fieldDisplayName, Value: "Acme"},
			{Field: fieldIndustry, Value: "Industrial automation"},
			{Field: fieldOfferSummary, Value: "Heat pumps"},
		},
		Facts: []DeepReadFact{
			{Category: "market", Field: "geographies", Value: "DACH", ValueKey: "dach"},
			{Category: "proof", Field: "customer_proof", Value: "500 sites", ValueKey: ""},
		},
	}
	company := Company{
		DisplayName:        "Acme",
		OrganizationSource: companySourceHuman,
		ProfileFields: []CompanyProfileField{
			{Field: fieldIndustry, Value: "Manufacturing", Source: companySourceHuman},
			{Field: fieldOfferSummary, Value: "Boilers", Source: companySourceSiteRead},
		},
		Facts: []CompanyFact{
			{Category: "proof", Field: "customer_proof", Value: "100 sites", ValueKey: "", Source: companySourceHuman},
		},
	}

	comparisons := compareCompanySiteRead(read, &company)
	got := make(map[string]string, len(comparisons))
	for _, comparison := range comparisons {
		got[comparison.Key] = comparison.Classification
	}
	want := map[string]string{
		fieldDisplayName:          siteReadComparisonUnchanged,
		fieldIndustry:             siteReadComparisonHumanConflict,
		fieldOfferSummary:         siteReadComparisonMachineChange,
		"market/geographies/dach": siteReadComparisonNew,
		"proof/customer_proof/":   siteReadComparisonHumanConflict,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("comparison classes = %#v, want %#v", got, want)
	}
	for i := 1; i < len(comparisons); i++ {
		previous, current := comparisons[i-1], comparisons[i]
		if previous.ValueKind > current.ValueKind ||
			(previous.ValueKind == current.ValueKind && previous.Key > current.Key) {
			t.Fatalf("comparisons are not deterministically sorted: %#v", comparisons)
		}
	}
}

func TestResolveSiteReadConflictsRequiresAndAppliesExplicitDecisions(t *testing.T) {
	read := SiteRead{
		ProfileFields: []DeepReadField{{Field: fieldIndustry, Value: "Industrial automation"}},
		Facts:         []DeepReadFact{{Category: "proof", Field: "customer_proof", Value: "500 sites"}},
	}
	company := Company{
		ProfileFields: []CompanyProfileField{{Field: fieldIndustry, Value: "Manufacturing", Source: companySourceHuman}},
		Facts:         []CompanyFact{{Category: "proof", Field: "customer_proof", Value: "100 sites", Source: companySourceHuman}},
	}
	base := ConfirmCompanySiteReadInput{
		DisplayName: "Acme",
		Fields:      map[string]*string{fieldIndustry: stringPointer("Industrial automation")},
		SelectedFactKeys: []string{
			"proof/customer_proof/",
		},
	}

	_, err := resolveSiteReadConflicts(read, &company, base)
	var invalid *InvalidSiteReadResolutionError
	if !errors.As(err, &invalid) {
		t.Fatalf("missing resolutions error = %v, want InvalidSiteReadResolutionError", err)
	}

	customProof := "750 verified sites"
	resolved, err := resolveSiteReadConflicts(read, &company, ConfirmCompanySiteReadInput{
		DisplayName:      base.DisplayName,
		Fields:           base.Fields,
		SelectedFactKeys: base.SelectedFactKeys,
		Resolutions: []SiteReadResolution{
			{Key: fieldIndustry, Action: siteReadResolutionKeep},
			{Key: "proof/customer_proof/", Action: siteReadResolutionUse, Value: &customProof},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.skipProfileFields[fieldIndustry] || resolved.Fields[fieldIndustry] != nil {
		t.Fatalf("keep-current did not remove the proposed profile write: %+v", resolved)
	}
	if len(resolved.SelectedFactKeys) != 0 || len(resolved.humanFactEdits) != 1 ||
		resolved.humanFactEdits[0].value != customProof {
		t.Fatalf("custom fact resolution = %+v", resolved)
	}
}

func TestResolveSiteReadConflictsRejectsStaleAndDuplicateKeys(t *testing.T) {
	read := SiteRead{ProfileFields: []DeepReadField{{Field: fieldIndustry, Value: "Industrial automation"}}}
	company := Company{ProfileFields: []CompanyProfileField{{Field: fieldIndustry, Value: "Manufacturing", Source: companySourceHuman}}}
	cases := map[string][]SiteReadResolution{
		"stale":     {{Key: fieldOfferSummary, Action: siteReadResolutionKeep}},
		"duplicate": {{Key: fieldIndustry, Action: siteReadResolutionKeep}, {Key: fieldIndustry, Action: siteReadResolutionAccept}},
	}
	for name, resolutions := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := resolveSiteReadConflicts(read, &company, ConfirmCompanySiteReadInput{
				DisplayName: "Acme", Fields: map[string]*string{}, Resolutions: resolutions,
			})
			var invalid *InvalidSiteReadResolutionError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want InvalidSiteReadResolutionError", err)
			}
		})
	}
}

func stringPointer(value string) *string { return &value }

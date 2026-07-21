// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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

func TestResolveSiteReadConflictsAppliesEveryResolutionAction(t *testing.T) {
	read := SiteRead{
		ProfileFields: []DeepReadField{
			{Field: fieldDisplayName, Value: "Acme Robotics"},
			{Field: fieldIndustry, Value: "Industrial automation"},
			{Field: fieldOfferSummary, Value: "Autonomous factories"},
		},
		Facts: []DeepReadFact{
			{Category: "proof", Field: "customer_proof", Value: "500 sites"},
			{Category: "market", Field: "geographies", Value: "DACH", ValueKey: "dach"},
		},
	}
	company := Company{
		DisplayName:        "Acme GmbH",
		OrganizationSource: companySourceHuman,
		ProfileFields: []CompanyProfileField{
			{Field: fieldIndustry, Value: "Manufacturing", Source: companySourceHuman},
			{Field: fieldOfferSummary, Value: "Factory software", Source: companySourceHuman},
		},
		Facts: []CompanyFact{
			{Category: "proof", Field: "customer_proof", Value: "100 sites", Source: companySourceHuman},
			{Category: "market", Field: "geographies", Value: "Germany", ValueKey: "dach", Source: companySourceHuman},
		},
	}
	customIndustry := "Factory intelligence"
	resolved, err := resolveSiteReadConflicts(read, &company, ConfirmCompanySiteReadInput{
		DisplayName: "Acme Robotics",
		Fields: map[string]*string{
			fieldIndustry:     stringPointer("Industrial automation"),
			fieldOfferSummary: stringPointer("Autonomous factories"),
		},
		SelectedFactKeys: []string{"proof/customer_proof/", "market/geographies/dach"},
		Resolutions: []SiteReadResolution{
			{Key: fieldDisplayName, Action: siteReadResolutionAccept},
			{Key: fieldIndustry, Action: siteReadResolutionUse, Value: &customIndustry},
			{Key: fieldOfferSummary, Action: siteReadResolutionKeep},
			{Key: "proof/customer_proof/", Action: siteReadResolutionAccept},
			{Key: "market/geographies/dach", Action: siteReadResolutionKeep},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.DisplayName != "Acme Robotics" || !resolved.overwriteProfileFields[fieldDisplayName] {
		t.Fatalf("accepted display name = %q, overwrite=%v", resolved.DisplayName, resolved.overwriteProfileFields)
	}
	if resolved.Fields[fieldIndustry] == nil || *resolved.Fields[fieldIndustry] != customIndustry {
		t.Fatalf("custom industry = %#v", resolved.Fields[fieldIndustry])
	}
	if !resolved.skipProfileFields[fieldOfferSummary] || resolved.Fields[fieldOfferSummary] != nil {
		t.Fatalf("kept offer summary was not removed: %+v", resolved)
	}
	if !reflect.DeepEqual(resolved.SelectedFactKeys, []string{"proof/customer_proof/"}) ||
		!resolved.overwriteFactKeys["proof/customer_proof/"] {
		t.Fatalf("fact decisions = keys %#v overwrite %#v", resolved.SelectedFactKeys, resolved.overwriteFactKeys)
	}
}

func TestResolveSiteReadConflictsRejectsInvalidResolutionValues(t *testing.T) {
	read := SiteRead{ProfileFields: []DeepReadField{{Field: fieldIndustry, Value: "Industrial automation"}}}
	company := Company{ProfileFields: []CompanyProfileField{{Field: fieldIndustry, Value: "Manufacturing", Source: companySourceHuman}}}
	value := "unexpected"
	blank := "   "
	cases := map[string]SiteReadResolution{
		"keep with value":   {Key: fieldIndustry, Action: siteReadResolutionKeep, Value: &value},
		"accept with value": {Key: fieldIndustry, Action: siteReadResolutionAccept, Value: &value},
		"use without value": {Key: fieldIndustry, Action: siteReadResolutionUse},
		"use blank value":   {Key: fieldIndustry, Action: siteReadResolutionUse, Value: &blank},
		"unknown action":    {Key: fieldIndustry, Action: "discard"},
	}
	for name, resolution := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := resolveSiteReadConflicts(read, &company, ConfirmCompanySiteReadInput{
				DisplayName: "Acme",
				Fields:      map[string]*string{},
				Resolutions: []SiteReadResolution{resolution},
			})
			var invalid *InvalidSiteReadResolutionError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want InvalidSiteReadResolutionError", err)
			}
		})
	}
}

func TestSplitConfirmedProfileKeepsSelectedLegalEntityWebsiteGrounding(t *testing.T) {
	legalName := "NFQ Solutions GmbH"
	address := "Deliusstraße 7, 24114 Kiel, Germany"
	vat := "DE346175276"
	in := ConfirmCompanySiteReadInput{
		DisplayName: "Gradion",
		Fields: map[string]*string{
			fieldLegalName:         &legalName,
			fieldRegisteredAddress: &address,
			fieldRegisterVat:       &vat,
		},
	}
	entities := []SiteReadLegalEntity{
		{Name: "Gradion Pte. Ltd.", SourceURL: "https://gradion.com/imprint"},
		{
			Name: "NFQ Solutions GmbH", RegisteredAddress: address, RegisterNumber: vat,
			EvidenceSnippet: "NFQ Solutions GmbH, Deliusstraße 7, VAT DE346175276",
			SourceURL:       "https://gradion.com/de/imprint",
		},
	}

	site, human := splitConfirmedProfile(nil, entities, in)
	if len(human) != 1 || human[fieldDisplayName] == nil {
		t.Fatalf("human fields = %#v, want only display_name", human)
	}
	if len(site) != 3 {
		t.Fatalf("site fields = %#v, want the three selected legal fields", site)
	}
	for _, field := range site {
		if field.SourceURL != "https://gradion.com/de/imprint" || field.EvidenceSnippet == "" {
			t.Fatalf("site field lost legal-page evidence: %#v", field)
		}
	}
}

func TestSplitConfirmedProfileDoesNotGroundMixedOrAmbiguousLegalBlocks(t *testing.T) {
	name := "Acme GmbH"
	register := "HRB 2"
	address := "Address from the other entity"
	in := ConfirmCompanySiteReadInput{Fields: map[string]*string{
		fieldLegalName: &name, fieldRegisteredAddress: &address, fieldRegisterVat: &register,
	}}
	entities := []SiteReadLegalEntity{
		{Name: name, RegisteredAddress: address, RegisterNumber: "HRB 1", SourceURL: "https://acme.test/imprint"},
		{Name: name, RegisteredAddress: "Second address", RegisterNumber: register, SourceURL: "https://acme.test/imprint"},
	}

	site, human := splitConfirmedProfile(nil, entities, in)
	if len(site) != 0 || len(human) != 4 {
		t.Fatalf("mixed identity produced site fields %#v and human fields %#v", site, human)
	}
}

func TestApplyResolvedHumanFactsWritesAuditableValues(t *testing.T) {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	tx := &recordingSiteReadTx{}
	edits := []resolvedHumanFact{
		{
			proposal: DeepReadFact{Category: "signal", Field: "named_customer", ValueKey: "old-customer"},
			value:    "New Customer",
		},
		{
			proposal: DeepReadFact{Category: "company", Field: "employee_range", ValueKey: ""},
			value:    "51-200",
		},
	}
	applied, err := applyResolvedHumanFacts(ctx, tx, ids.New[ids.OrganizationKind](), "human:user", edits)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 || len(tx.calls) != 4 {
		t.Fatalf("applied = %#v, SQL calls = %d", applied, len(tx.calls))
	}
	if got := tx.calls[1].args[5]; got != NormalizeFactValueKey("New Customer") {
		t.Fatalf("multi-value key = %v, want normalized custom value", got)
	}
	if got := applied[0][auditKeySource]; got != companySourceHuman {
		t.Fatalf("audit source = %v, want %q", got, companySourceHuman)
	}
}

func TestApplyResolvedHumanFactsReturnsDatabaseErrors(t *testing.T) {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	edit := []resolvedHumanFact{{
		proposal: DeepReadFact{Category: "company", Field: "employee_range"},
		value:    "51-200",
	}}
	cases := []struct {
		name   string
		failAt int
		want   string
	}{
		{name: "delete", failAt: 1, want: "replace human organization fact"},
		{name: "insert", failAt: 2, want: "save human organization fact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyResolvedHumanFacts(ctx, &recordingSiteReadTx{failAt: tc.failAt},
				ids.New[ids.OrganizationKind](), "human:user", edit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want message containing %q", err, tc.want)
			}
		})
	}
}

type siteReadExecCall struct {
	args []any
}

type recordingSiteReadTx struct {
	pgx.Tx
	calls  []siteReadExecCall
	failAt int
}

func (tx *recordingSiteReadTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	tx.calls = append(tx.calls, siteReadExecCall{args: args})
	if tx.failAt == len(tx.calls) {
		return pgconn.CommandTag{}, errors.New("forced database failure")
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func stringPointer(value string) *string { return &value }

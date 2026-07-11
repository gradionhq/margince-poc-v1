// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The custom-field half of the contract→store mapping: a `cf_*` key in a
// request body survives the generated types' AdditionalProperties
// catch-all into the store input on BOTH surfaces (the HTTP handlers and
// the SoR provider decode through these same functions), for person and
// organization alike. Which keys actually land is the store's decision
// (active catalog columns only, drop-on-mismatch) — the mapping must
// stay a faithful carrier, never a filter.

import (
	"encoding/json"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

//craft:ignore naked-any the decode target is whichever generated contract request struct the case exercises — the same seam shape as datasource.StrictDecode
func decodeInto(t *testing.T, raw string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
}

func TestPersonCreateInputCarriesCustomFieldKeys(t *testing.T) {
	var req crmcontracts.CreatePersonRequest
	decodeInto(t, `{"full_name":"Ada","source":"ui","cf_tier":"gold","cf_seats":12}`, &req)

	in, err := personCreateInput(req)
	if err != nil {
		t.Fatalf("personCreateInput: %v", err)
	}
	if got := in.CustomFields["cf_tier"]; got != "gold" {
		t.Errorf(`CustomFields["cf_tier"] = %v, want "gold"`, got)
	}
	if got := in.CustomFields["cf_seats"]; got != float64(12) {
		t.Errorf(`CustomFields["cf_seats"] = %v (%T), want float64(12)`, got, got)
	}
}

func TestPersonUpdateInputCarriesCustomFieldKeys(t *testing.T) {
	var req crmcontracts.UpdatePersonRequest
	decodeInto(t, `{"title":"CTO","cf_tier":"silver"}`, &req)

	in := personUpdateInput(req, nil)
	if got := in.CustomFields["cf_tier"]; got != "silver" {
		t.Errorf(`CustomFields["cf_tier"] = %v, want "silver"`, got)
	}
}

func TestOrganizationCreateInputCarriesCustomFieldKeys(t *testing.T) {
	var req crmcontracts.CreateOrganizationRequest
	decodeInto(t, `{"display_name":"Acme","source":"ui","cf_region":"emea"}`, &req)

	in, err := organizationCreateInput(req)
	if err != nil {
		t.Fatalf("organizationCreateInput: %v", err)
	}
	if got := in.CustomFields["cf_region"]; got != "emea" {
		t.Errorf(`CustomFields["cf_region"] = %v, want "emea"`, got)
	}
}

func TestOrganizationUpdateInputCarriesCustomFieldKeys(t *testing.T) {
	var req crmcontracts.UpdateOrganizationRequest
	decodeInto(t, `{"industry":"robotics","cf_region":"apac"}`, &req)

	in := organizationUpdateInput(req, nil)
	if got := in.CustomFields["cf_region"]; got != "apac" {
		t.Errorf(`CustomFields["cf_region"] = %v, want "apac"`, got)
	}
}

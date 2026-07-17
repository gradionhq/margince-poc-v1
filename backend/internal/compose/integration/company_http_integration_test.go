// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The company form over the real wire: the 404 that IS the onboarding signal,
// the identity block's field-by-field 422s, the website's normalisation to a
// bare domain, and the human-only posture that lets an agent propose the
// company but never make it true. The store's own write shape is proved in
// compose/company_integration_test.go; this suite owns the transport.

import (
	"net/http"
	"strconv"
	"testing"
)

type companyProfileDTO struct {
	OrganizationID    string  `json:"organization_id"`
	DisplayName       string  `json:"display_name"`
	Website           *string `json:"website"`
	LegalName         *string `json:"legal_name"`
	RegisteredAddress *string `json:"registered_address"`
	RegisterVat       *string `json:"register_vat"`
	Industry          *string `json:"industry"`
	Icp               *string `json:"icp"`
	Usp               *string `json:"usp"`
}

// companyProblem is the RFC 7807 body this surface answers: the sentinel code
// plus, for a validation refusal, the field-level list naming what to fix.
type companyProblem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	} `json:"details"`
}

// orAbsent renders an optional wire field for a failure message: its value, or
// the fact that the field was absent — never a pointer address.
func orAbsent(value *string) string {
	if value == nil {
		return "<absent>"
	}
	return strconv.Quote(*value)
}

// wellFormedCompany is a submission every required field of which is filled —
// the base a test perturbs one field of, so a 422 can only be about that field.
func wellFormedCompany() anyMap {
	return anyMap{
		"display_name":       "Acme GmbH",
		"legal_name":         "Acme Gesellschaft mit beschränkter Haftung",
		"registered_address": "Torstraße 1, 10119 Berlin",
		"register_vat":       "DE123456789",
		"industry":           "B2B SaaS",
	}
}

// The gate's whole contract: a bare installation has not described itself, and
// the SAME GET answers the company once a human has saved it.
func TestCompanyIs404UntilAHumanSavesItOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	if status := e.call(t, "GET", "/v1/company", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("GET /company on a bare installation → %d, want 404 (the onboarding signal)", status)
	}

	body := wellFormedCompany()
	body["website"] = "https://www.acme.example/about"
	body["icp"] = "RevOps at SaaS scale-ups"
	var saved companyProfileDTO
	if status := e.call(t, "PUT", "/v1/company", body, nil, &saved); status != http.StatusOK {
		t.Fatalf("PUT /company → %d, want 200", status)
	}
	// The website is stored and returned as the bare domain — the same handle a
	// read-back resolves organizations by — so a full URL normalises on the way in.
	if saved.Website == nil || *saved.Website != "acme.example" {
		t.Fatalf("saved website = %s, want the bare domain acme.example", orAbsent(saved.Website))
	}

	var got companyProfileDTO
	if status := e.call(t, "GET", "/v1/company", nil, nil, &got); status != http.StatusOK {
		t.Fatalf("GET /company after save → %d, want 200", status)
	}
	if got.OrganizationID != saved.OrganizationID || got.DisplayName != "Acme GmbH" {
		t.Fatalf("GET /company = %+v, want the company just saved", got)
	}
	if got.Icp == nil || *got.Icp != "RevOps at SaaS scale-ups" {
		t.Fatalf("saved icp did not round-trip: %s", orAbsent(got.Icp))
	}
	// A field nobody filled is ABSENT on the wire, never the empty answer the
	// form would render as a value someone chose.
	if got.Usp != nil {
		t.Fatalf("an unsent field came back as %q, want absent", *got.Usp)
	}
}

// The identity block is what the installation IS: each required field, missing
// or whitespace-only, is a 422 that NAMES that field — the human is told which
// answer is missing rather than left to guess.
func TestCompanyRequiredFieldsAreNamedIndividually(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	required := []string{"display_name", "legal_name", "registered_address", "register_vat", "industry"}
	cases := []struct {
		name    string
		missing bool
		value   string
	}{
		{name: "missing", missing: true},
		{name: "whitespace-only", value: "   \t "},
	}
	for _, field := range required {
		for _, tc := range cases {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				body := wellFormedCompany()
				if tc.missing {
					delete(body, field)
				} else {
					body[field] = tc.value
				}
				var problem companyProblem
				status := e.call(t, "PUT", "/v1/company", body, nil, &problem)
				if status != http.StatusUnprocessableEntity {
					t.Fatalf("PUT /company without %s → %d, want 422", field, status)
				}
				if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != field {
					t.Fatalf("422 for %s names %+v, want exactly that field", field, problem.Details.Errors)
				}
			})
		}
	}

	// Nothing above was persisted: a refused submission leaves the
	// installation undescribed.
	if status := e.call(t, "GET", "/v1/company", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("a refused save created a company anyway (GET → %d, want 404)", status)
	}
}

// Website is optional and forgiving of what a human types, but an answer that
// could not name a host is refused rather than stored.
func TestCompanyWebsiteIsOptionalAndNormalised(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// The typed-by-hand path: no website read at all, no website typed either.
	var typed companyProfileDTO
	if status := e.call(t, "PUT", "/v1/company", wellFormedCompany(), nil, &typed); status != http.StatusOK {
		t.Fatalf("a company typed by hand with no website → %d, want 200", status)
	}
	if typed.Website != nil {
		t.Fatalf("no website was typed, yet one came back: %q", *typed.Website)
	}

	var problem companyProblem
	body := wellFormedCompany()
	body["website"] = "not a url at all"
	if status := e.call(t, "PUT", "/v1/company", body, nil, &problem); status != http.StatusUnprocessableEntity {
		t.Fatalf("an unparseable website → %d, want 422", status)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "website" {
		t.Fatalf("bad-website 422 names %+v, want website", problem.Details.Errors)
	}

	// A bare domain is as acceptable as a full URL.
	bare := wellFormedCompany()
	bare["website"] = "acme.example"
	var withBare companyProfileDTO
	if status := e.call(t, "PUT", "/v1/company", bare, nil, &withBare); status != http.StatusOK {
		t.Fatalf("a bare-domain website → %d, want 200", status)
	}
	if withBare.Website == nil || *withBare.Website != "acme.example" {
		t.Fatalf("bare-domain website = %s, want acme.example", orAbsent(withBare.Website))
	}
}

// Saving twice is an UPDATE of the installation's own company — never a second
// one. An optional field sent empty is cleared; one omitted is left as it was.
func TestCompanySavingTwiceUpdatesTheAnchorOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	first := wellFormedCompany()
	first["icp"] = "RevOps at SaaS scale-ups"
	first["usp"] = "Evidence, not guesses"
	var saved companyProfileDTO
	if status := e.call(t, "PUT", "/v1/company", first, nil, &saved); status != http.StatusOK {
		t.Fatalf("first save → %d", status)
	}

	// Second save: a new name, icp cleared, usp not mentioned at all.
	second := wellFormedCompany()
	second["display_name"] = "Acme SE"
	second["icp"] = ""
	var again companyProfileDTO
	if status := e.call(t, "PUT", "/v1/company", second, nil, &again); status != http.StatusOK {
		t.Fatalf("second save → %d", status)
	}
	if again.OrganizationID != saved.OrganizationID {
		t.Fatalf("the second save minted a rival company (%s != %s)", again.OrganizationID, saved.OrganizationID)
	}
	if again.DisplayName != "Acme SE" {
		t.Fatalf("the second save did not update the name: %q", again.DisplayName)
	}
	if again.Icp != nil {
		t.Fatalf("an optional field sent empty is still present: %q", *again.Icp)
	}
	if again.Usp == nil || *again.Usp != "Evidence, not guesses" {
		t.Fatalf("an omitted field was not left as it was: %s, want the first save's value", orAbsent(again.Usp))
	}

	// The customer-record surface still holds exactly the one organization: the
	// anchor was updated, not duplicated.
	var orgs struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/organizations", nil, nil, &orgs); status != http.StatusOK {
		t.Fatalf("list organizations → %d", status)
	}
	if len(orgs.Data) != 1 || orgs.Data[0].ID != saved.OrganizationID {
		t.Fatalf("saving twice left %d organizations, want the one anchor", len(orgs.Data))
	}
}

// human-only is the point: an agent may PROPOSE the company (/coldstart/preview)
// but never make it true, exactly as it may stage an approval and never approve
// it. A write-scoped passport is still refused.
func TestCompanyPutRefusesAnAgentPassport(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "read-back agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	var problem companyProblem
	status := e.call(t, "PUT", "/v1/company", wellFormedCompany(), bearer, &problem)
	if status != http.StatusForbidden || problem.Code != "permission_denied" {
		t.Fatalf("agent PUT /company → %d %q, want 403 permission_denied", status, problem.Code)
	}
	// Refused means refused: the agent's submission did not become the company.
	if getStatus := e.call(t, "GET", "/v1/company", nil, nil, nil); getStatus != http.StatusNotFound {
		t.Fatalf("the agent's refused PUT still saved a company (GET → %d, want 404)", getStatus)
	}
}

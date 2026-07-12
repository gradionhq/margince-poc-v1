// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for the six /offer-templates operations plus
// renderOffer's still-unimplemented 501 (offers-depth arc 4a, T3): the
// handler (deals.Handlers) and its wire mapping that
// offertemplate_integration_test.go's store-level suite never drives —
// the two named 409 shapes (offer_template_name_duplicate,
// offer_template_default_conflict), the full-replace PUT's version-skew
// 409, and the JSON response shape. Rides the same real-handler-stack
// e2e harness as quotas_http_integration_test.go (TLS httptest server,
// session cookie, workspace header).

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type offerTemplateProblem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		ExistingID string `json:"existing_id"`
		Locale     string `json:"locale"`
	} `json:"details"`
}

func TestOfferTemplatesHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var createdID string
	t.Run("201 create with defaults", func(t *testing.T) {
		createdID = assertOfferTemplateCreate201(t, e)
	})
	t.Run("409 offer_template_name_duplicate", func(t *testing.T) {
		assertOfferTemplateNameDuplicate409(t, e)
	})
	t.Run("409 offer_template_default_conflict", func(t *testing.T) {
		assertOfferTemplateDefaultConflict409(t, e)
	})
	t.Run("200 get, 404 unknown id", func(t *testing.T) {
		assertOfferTemplateGet(t, e, createdID)
	})
	t.Run("200 list with locale filter", func(t *testing.T) {
		assertOfferTemplateList(t, e, createdID)
	})
	t.Run("update: 200 happy full-replace, 409 stale If-Match", func(t *testing.T) {
		assertOfferTemplateUpdate(t, e, createdID)
	})
	t.Run("200 archive returns the full entity, stays gettable", func(t *testing.T) {
		assertOfferTemplateArchive(t, e)
	})
	t.Run("422 missing required layout on create", func(t *testing.T) {
		assertOfferTemplateMissingLayout422(t, e)
	})
	t.Run("501 renderOffer stays unimplemented", func(t *testing.T) {
		assertRenderOfferNotImplemented(t, e)
	})
}

func assertOfferTemplateCreate201(t *testing.T, e *env) string {
	t.Helper()
	var created anyMap
	status := e.call(t, "POST", "/v1/offer-templates", anyMap{
		"name": "HTTP Standard DE", "layout": anyMap{"logo_url": "https://example.test/logo.png"},
	}, nil, &created)
	if status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, created)
	}
	if created["locale"] != "de-DE" || created["is_default"] != false {
		t.Errorf("create must default locale=de-DE is_default=false, got %+v", created)
	}
	if created["version"].(float64) != 1 || created["archived_at"] != nil {
		t.Errorf("a fresh template carries version 1 and no archived_at, got %+v", created)
	}
	return created["id"].(string)
}

func assertOfferTemplateNameDuplicate409(t *testing.T, e *env) {
	t.Helper()
	var problem offerTemplateProblem
	status := e.call(t, "POST", "/v1/offer-templates", anyMap{
		"name": "HTTP Standard DE", "layout": anyMap{},
	}, nil, &problem)
	if status != http.StatusConflict || problem.Code != "offer_template_name_duplicate" {
		t.Fatalf("duplicate name create = %d %+v, want 409 offer_template_name_duplicate", status, problem)
	}
	if problem.Details.ExistingID == "" {
		t.Error("offer_template_name_duplicate must carry details.existing_id")
	}
}

func assertOfferTemplateDefaultConflict409(t *testing.T, e *env) {
	t.Helper()
	var firstDefault anyMap
	status := e.call(t, "POST", "/v1/offer-templates", anyMap{
		"name": "HTTP DE Default", "layout": anyMap{}, "is_default": true,
	}, nil, &firstDefault)
	if status != http.StatusCreated {
		t.Fatalf("seed default template = %d %v", status, firstDefault)
	}

	var problem offerTemplateProblem
	status = e.call(t, "POST", "/v1/offer-templates", anyMap{
		"name": "HTTP DE Default Two", "layout": anyMap{}, "is_default": true,
	}, nil, &problem)
	if status != http.StatusConflict || problem.Code != "offer_template_default_conflict" {
		t.Fatalf("default conflict create = %d %+v, want 409 offer_template_default_conflict", status, problem)
	}
	if problem.Details.Locale != "de-DE" || problem.Details.ExistingID != firstDefault["id"] {
		t.Errorf("offer_template_default_conflict details = %+v, want locale=de-DE existing_id=%s", problem.Details, firstDefault["id"])
	}
}

func assertOfferTemplateGet(t *testing.T, e *env, id string) {
	t.Helper()
	var got anyMap
	if status := e.call(t, "GET", "/v1/offer-templates/"+id, nil, nil, &got); status != http.StatusOK || got["id"] != id {
		t.Fatalf("get = %d %+v, want 200 id=%s", status, got, id)
	}
	if status := e.call(t, "GET", "/v1/offer-templates/"+ids.NewV7().String(), nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("get unknown template = %d, want 404", status)
	}
}

func assertOfferTemplateList(t *testing.T, e *env, seededID string) {
	t.Helper()
	var deList struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/offer-templates?locale=de-DE", nil, nil, &deList); status != http.StatusOK {
		t.Fatalf("list locale=de-DE = %d", status)
	}
	found := false
	for _, tpl := range deList.Data {
		if tpl["id"] == seededID {
			found = true
		}
		if tpl["locale"] != "de-DE" {
			t.Errorf("locale=de-DE filter leaked a %v row", tpl["locale"])
		}
	}
	if !found {
		t.Errorf("locale=de-DE list = %+v, want the seeded template included", deList.Data)
	}
}

func assertOfferTemplateUpdate(t *testing.T, e *env, id string) {
	t.Helper()
	var updated anyMap
	status := e.call(t, "PUT", "/v1/offer-templates/"+id, anyMap{
		"name": "HTTP Standard DE v2", "locale": "de-DE", "is_default": false,
		"layout": anyMap{"footer_text": "v2"},
	}, map[string]string{"If-Match": "1"}, &updated)
	if status != http.StatusOK || updated["name"] != "HTTP Standard DE v2" || updated["version"].(float64) != 2 {
		t.Fatalf("update = %d %+v, want 200 name=... version=2", status, updated)
	}

	var stale offerTemplateProblem
	status = e.call(t, "PUT", "/v1/offer-templates/"+id, anyMap{
		"name": "HTTP Standard DE v3", "locale": "de-DE", "is_default": false, "layout": anyMap{},
	}, map[string]string{"If-Match": "1"}, &stale)
	if status != http.StatusConflict || stale.Code != "version_skew" {
		t.Fatalf("stale If-Match = %d %+v, want 409 version_skew", status, stale)
	}
}

func assertOfferTemplateArchive(t *testing.T, e *env) {
	t.Helper()
	var created anyMap
	status := e.call(t, "POST", "/v1/offer-templates", anyMap{
		"name": "HTTP Archivable", "layout": anyMap{},
	}, nil, &created)
	if status != http.StatusCreated {
		t.Fatalf("create to archive = %d %v", status, created)
	}
	id := created["id"].(string)

	var archived anyMap
	status = e.call(t, "DELETE", "/v1/offer-templates/"+id, nil, nil, &archived)
	if status != http.StatusOK || archived["archived_at"] == nil || archived["id"] != id {
		t.Fatalf("archive = %d %+v, want 200 + the full entity with archived_at set", status, archived)
	}
	var stillGettable anyMap
	if status := e.call(t, "GET", "/v1/offer-templates/"+id, nil, nil, &stillGettable); status != http.StatusOK || stillGettable["archived_at"] == nil {
		t.Fatalf("get archived template = %d %+v, want 200 with archived_at set", status, stillGettable)
	}
}

func assertOfferTemplateMissingLayout422(t *testing.T, e *env) {
	t.Helper()
	var problem struct {
		Code    string `json:"code"`
		Details struct {
			Errors []struct {
				Field string `json:"field"`
				Code  string `json:"code"`
			} `json:"errors"`
		} `json:"details"`
	}
	status := e.call(t, "POST", "/v1/offer-templates", anyMap{"name": "No Layout"}, nil, &problem)
	if status != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
		t.Fatalf("missing layout = %d %+v, want 422 validation_error", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "layout" {
		t.Fatalf("details.errors = %+v, want [{layout ...}]", problem.Details.Errors)
	}
}

// assertRenderOfferNotImplemented proves the render endpoint stays the
// explicit-501 shape until T4 wires the PDF renderer — a dedicated
// deal+offer is seeded fresh here because offerFixture (offers_integration_test.go)
// is this suite's only source of a valid deal/pipeline pair.
func assertRenderOfferNotImplemented(t *testing.T, e *env) {
	t.Helper()
	dealID := offerFixture(t, e)
	var offer anyMap
	status := e.call(t, "POST", "/v1/deals/"+dealID+"/offers", anyMap{
		"currency": "EUR", "source": "manual",
		"line_items": []anyMap{{"description": "Retainer", "quantity": 1, "unit_price_minor": 500000, "tax_rate": 19.0}},
	}, nil, &offer)
	if status != http.StatusCreated {
		t.Fatalf("create offer for render = %d %v", status, offer)
	}
	status = e.call(t, "POST", "/v1/offers/"+offer["id"].(string)+"/render", anyMap{}, nil, nil)
	if status != http.StatusNotImplemented {
		t.Fatalf("renderOffer = %d, want 501 (T4 wires the real renderer)", status)
	}
}

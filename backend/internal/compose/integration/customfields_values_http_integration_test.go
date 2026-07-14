// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The HTTP half of the custom-field VALUES coverage (the store-level
// semantics live in customfields_values_integration_test.go): proves
// the wire flatten over the real compose stack — cf_ keys travel
// TOP-LEVEL in request and response bodies through the generated types'
// additionalProperties — and that a picklist CHECK violation answers
// the typed 422, never a 500.

import (
	"net/http"
	"testing"
)

// assertWireCF asserts one top-level custom-field key on a decoded wire
// payload.
//
//craft:ignore naked-any want is whichever JSON-decoded shape the wire carries for the field's type (string/bool/float64) — the assertion seam mirrors env.call's out
func assertWireCF(t *testing.T, payload anyMap, key string, want any) {
	t.Helper()
	if payload[key] != want {
		t.Fatalf("wire %s = %v (%T), want top-level %v", key, payload[key], payload[key], want)
	}
}

// createWithCF posts one record body and returns the decoded response
// plus its id, asserting the 201.
func createWithCF(t *testing.T, e *env, path string, body anyMap) (anyMap, string) {
	t.Helper()
	var created anyMap
	if status := e.call(t, "POST", path, body, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST %s status = %d (%v)", path, status, created)
	}
	id, ok := created["id"].(string)
	if !ok {
		t.Fatalf("POST %s response carries no id: %v", path, created)
	}
	return created, id
}

func assertPersonWireRoundTrip(t *testing.T, e *env, col string) {
	t.Helper()
	created, id := createWithCF(t, e, "/v1/people", anyMap{
		"full_name": "Ada Lovelace", "source": "ui", col: "gold",
	})
	assertWireCF(t, created, col, "gold")

	var got anyMap
	if status := e.call(t, "GET", "/v1/people/"+id, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get person status = %d", status)
	}
	assertWireCF(t, got, col, "gold")

	var updated anyMap
	if status := e.call(t, "PATCH", "/v1/people/"+id, anyMap{col: "silver"}, nil, &updated); status != http.StatusOK {
		t.Fatalf("update person status = %d (%v)", status, updated)
	}
	assertWireCF(t, updated, col, "silver")

	var list struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/people", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("list people status = %d", status)
	}
	if len(list.Data) != 1 {
		t.Fatalf("list people returned %d rows, want 1", len(list.Data))
	}
	assertWireCF(t, list.Data[0], col, "silver")
}

func assertOrganizationWireRoundTrip(t *testing.T, e *env, col string) {
	t.Helper()
	created, id := createWithCF(t, e, "/v1/organizations", anyMap{
		"display_name": "Acme GmbH", "source": "ui", col: "emea",
	})
	assertWireCF(t, created, col, "emea")

	var got anyMap
	if status := e.call(t, "GET", "/v1/organizations/"+id, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get organization status = %d", status)
	}
	assertWireCF(t, got, col, "emea")
}

// assertDealWireRoundTrip mirrors assertPersonWireRoundTrip's full
// create/get/update/list shape for the deal object — one of the four
// core objects the fieldcatalog seam rides (person/organization/deal/lead).
func assertDealWireRoundTrip(t *testing.T, e *env, col string) {
	t.Helper()
	stages := discoverSeededPipeline(t, e)
	created, id := createWithCF(t, e, "/v1/deals", anyMap{
		"name": "Acme Renewal", "pipeline_id": stages.pipelineID, "stage_id": stages.open,
		"source": "ui", col: "enterprise",
	})
	assertWireCF(t, created, col, "enterprise")

	var got anyMap
	if status := e.call(t, "GET", "/v1/deals/"+id, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get deal status = %d", status)
	}
	assertWireCF(t, got, col, "enterprise")

	var updated anyMap
	if status := e.call(t, "PATCH", "/v1/deals/"+id, anyMap{col: "mid-market"}, nil, &updated); status != http.StatusOK {
		t.Fatalf("update deal status = %d (%v)", status, updated)
	}
	assertWireCF(t, updated, col, "mid-market")

	var list struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/deals", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("list deals status = %d", status)
	}
	if len(list.Data) != 1 {
		t.Fatalf("list deals returned %d rows, want 1", len(list.Data))
	}
	assertWireCF(t, list.Data[0], col, "mid-market")
}

// assertLeadWireRoundTrip mirrors the deal round trip for the lead object,
// the fourth fieldcatalog-riding core object — create/get/update/list all
// carry the cf key top-level over the wire.
func assertLeadWireRoundTrip(t *testing.T, e *env, col string) {
	t.Helper()
	created, id := createWithCF(t, e, "/v1/leads", anyMap{
		"full_name": "Grace Hopper", "source": "ui", col: "champion",
	})
	assertWireCF(t, created, col, "champion")

	var got anyMap
	if status := e.call(t, "GET", "/v1/leads/"+id, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get lead status = %d", status)
	}
	assertWireCF(t, got, col, "champion")

	var updated anyMap
	if status := e.call(t, "PATCH", "/v1/leads/"+id, anyMap{col: "detractor"}, nil, &updated); status != http.StatusOK {
		t.Fatalf("update lead status = %d (%v)", status, updated)
	}
	assertWireCF(t, updated, col, "detractor")

	var list struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/leads", nil, nil, &list); status != http.StatusOK {
		t.Fatalf("list leads status = %d", status)
	}
	if len(list.Data) != 1 {
		t.Fatalf("list leads returned %d rows, want 1", len(list.Data))
	}
	assertWireCF(t, list.Data[0], col, "detractor")
}

// sixTypeWireFields creates one active field of every closed type on the
// person object and returns each type's physical column name, keyed by
// type — the wire-level twin of TestCustomFieldValues_AllSixTypesRoundTrip.
func sixTypeWireFields(t *testing.T, e *env) map[string]string {
	t.Helper()
	specs := map[string]anyMap{
		"text":     {"object": "person", "label": "Note", "type": "text", "source": "ui"},
		"number":   {"object": "person", "label": "Score", "type": "number", "source": "ui"},
		"date":     {"object": "person", "label": "Renewal", "type": "date", "source": "ui"},
		"currency": {"object": "person", "label": "Budget", "type": "currency", "currency": "EUR", "source": "ui"},
		"picklist": {"object": "person", "label": "Route", "type": "picklist", "options": []string{"direct", "partner"}, "source": "ui"},
		"boolean":  {"object": "person", "label": "Strategic", "type": "boolean", "source": "ui"},
	}
	cols := make(map[string]string, len(specs))
	for kind, body := range specs {
		status, field, problem := createCustomField(t, e, body)
		if status != http.StatusCreated {
			t.Fatalf("create %s field status = %d: %+v", kind, status, problem)
		}
		cols[kind] = field.ColumnName
	}
	return cols
}

// assertSixTypesWireRoundTrip proves every closed field type carries its
// documented wire read shape (json-decoded string/float64/bool) through
// a real create-then-get over the compose stack — the store-level
// suite's AllSixTypesRoundTrip already proves the value semantics; this
// proves the same shapes survive the HTTP marshal/unmarshal round trip.
func assertSixTypesWireRoundTrip(t *testing.T, e *env) {
	t.Helper()
	cols := sixTypeWireFields(t, e)
	want := anyMap{
		cols["text"]:     "prefers morning calls",
		cols["number"]:   42.5,
		cols["date"]:     "2026-07-11",
		cols["currency"]: float64(129900),
		cols["picklist"]: "partner",
		cols["boolean"]:  true,
	}
	body := anyMap{"full_name": "Grace Hopper", "source": "ui"}
	for col, v := range want {
		body[col] = v
	}
	created, id := createWithCF(t, e, "/v1/people", body)
	for col, v := range want {
		assertWireCF(t, created, col, v)
	}

	var got anyMap
	if status := e.call(t, "GET", "/v1/people/"+id, nil, nil, &got); status != http.StatusOK {
		t.Fatalf("get person status = %d", status)
	}
	for col, v := range want {
		assertWireCF(t, got, col, v)
	}
}

func assertPicklistCheckViolation422(t *testing.T, e *env, col string) {
	t.Helper()
	var problem customFieldProblem
	status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Bad Option", "source": "ui", col: "bogus",
	}, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("create with invalid picklist option status = %d, want 422 (%+v)", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Code != "constraint_violated" {
		t.Fatalf("problem details = %+v, want one constraint_violated entry", problem.Details)
	}
}

func TestCustomFieldValuesHTTP(t *testing.T) {
	e := schemaWiredEnv(t)

	status, tier, problem := createCustomField(t, e, anyMap{
		"object": "person", "label": "Tier", "type": "picklist",
		"options": []string{"gold", "silver"}, "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create person field status = %d: %+v", status, problem)
	}
	status, region, problem := createCustomField(t, e, anyMap{
		"object": "organization", "label": "Region", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create organization field status = %d: %+v", status, problem)
	}
	status, segment, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Segment", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create deal field status = %d: %+v", status, problem)
	}
	status, persona, problem := createCustomField(t, e, anyMap{
		"object": "lead", "label": "Persona", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create lead field status = %d: %+v", status, problem)
	}

	t.Run("person create/get/update/list carry the key top-level", func(t *testing.T) {
		assertPersonWireRoundTrip(t, e, tier.ColumnName)
	})
	t.Run("organization round trip carries the key top-level", func(t *testing.T) {
		assertOrganizationWireRoundTrip(t, e, region.ColumnName)
	})
	t.Run("deal create/get/update/list carry the key top-level", func(t *testing.T) {
		assertDealWireRoundTrip(t, e, segment.ColumnName)
	})
	t.Run("lead create/get/update/list carry the key top-level", func(t *testing.T) {
		assertLeadWireRoundTrip(t, e, persona.ColumnName)
	})
	t.Run("picklist CHECK violation answers 422", func(t *testing.T) {
		assertPicklistCheckViolation422(t, e, tier.ColumnName)
	})
	t.Run("all six types round-trip their wire shape", func(t *testing.T) {
		assertSixTypesWireRoundTrip(t, e)
	})
}

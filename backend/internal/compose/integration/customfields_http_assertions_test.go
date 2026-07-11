// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The per-scenario assertion helpers TestCustomFieldsHTTP's subtests call
// (split from customfields_http_integration_test.go to stay under the
// repo's 500-LOC file cap — one suite, two files by concept: the test
// entry point + shared wire types/helpers there, the individual scenario
// bodies here).

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// assertValidationDetails proves the contract's exact multi-field 422 shape:
// details.errors[{field,code}], no fabricated message.
func assertValidationDetails(t *testing.T, e *env) {
	t.Helper()
	status, _, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Budget ceiling", "type": "currency", "source": "ui",
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %+v", status, problem)
	}
	if problem.Code != "validation_error" {
		t.Fatalf("code = %q, want validation_error", problem.Code)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "currency" ||
		problem.Details.Errors[0].Code != "required_for_type_currency" {
		t.Fatalf("details.errors = %+v, want exactly [{currency required_for_type_currency}]", problem.Details.Errors)
	}
}

// assertStructuralChangeRefused proves the contract's structural_change_refused
// 422 example verbatim, including details.route.
func assertStructuralChangeRefused(t *testing.T, e *env) {
	t.Helper()
	status, _, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Renewal formula", "type": "text", "source": "ui",
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %+v", status, problem)
	}
	if problem.Code != "structural_change_refused" {
		t.Fatalf("code = %q, want structural_change_refused", problem.Code)
	}
	if problem.Details.Route != "source_development_path" {
		t.Fatalf("details.route = %q, want source_development_path", problem.Details.Route)
	}
}

// assertDuplicateSlugConflict proves the per-workspace duplicate-slug 409:
// the same (object, label) pair refused the second time.
func assertDuplicateSlugConflict(t *testing.T, e *env) {
	t.Helper()
	body := anyMap{"object": "deal", "label": "Renewal Window", "type": "text", "source": "ui"}
	first, field, _ := createCustomField(t, e, body)
	if first != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201: %+v", first, field)
	}
	second, _, problem := createCustomField(t, e, body)
	if second != http.StatusConflict {
		t.Fatalf("second create status = %d, want 409: %+v", second, problem)
	}
}

// assertRetireUnknownID proves a nonexistent id answers the ordinary
// existence-hiding 404, not a schema-pool-shaped error.
func assertRetireUnknownID(t *testing.T, e *env) {
	t.Helper()
	var problem customFieldProblem
	status := e.call(t, "POST", "/v1/custom-fields/"+ids.NewV7().String()+"/retire", nil, nil, &problem)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %+v", status, problem)
	}
}

// assertListInvalidObject proves the closed object vocabulary is enforced
// on List too, with the same single-field details.errors shape.
func assertListInvalidObject(t *testing.T, e *env) {
	t.Helper()
	var problem customFieldProblem
	status := e.call(t, "GET", "/v1/custom-fields?object=bogus", nil, nil, &problem)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %+v", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "object" {
		t.Fatalf("details.errors = %+v, want a single object error", problem.Details.Errors)
	}
}

// assertNotPicklistOptionsEdit proves an options edit on a non-picklist
// field answers the contract's dedicated not_picklist code, not the
// generic validation_error.
func assertNotPicklistOptionsEdit(t *testing.T, e *env) {
	t.Helper()
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Not A Picklist", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}
	var notPicklist customFieldProblem
	optStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID+"/options",
		anyMap{"options": []string{"a"}}, nil, &notPicklist)
	if optStatus != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %+v", optStatus, notPicklist)
	}
	if notPicklist.Code != "not_picklist" {
		t.Fatalf("code = %q, want not_picklist", notPicklist.Code)
	}
}

// assertRenameConcurrencyErrors proves the If-Match plumbing: a malformed
// header is a 422 client error caught before the store is ever called,
// and a stale (correct-shape, wrong-value) version is the store's own
// ErrVersionSkew, wire-mapped to 409 like every other versioned PATCH.
func assertRenameConcurrencyErrors(t *testing.T, e *env) {
	t.Helper()
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Concurrency Subject", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}

	var malformed customFieldProblem
	malformedStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID,
		anyMap{"label": "Whatever"}, map[string]string{"If-Match": "not-a-number"}, &malformed)
	if malformedStatus != http.StatusUnprocessableEntity {
		t.Fatalf("malformed If-Match status = %d, want 422: %+v", malformedStatus, malformed)
	}

	var skew customFieldProblem
	skewStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID,
		anyMap{"label": "Whatever"}, map[string]string{"If-Match": "999999"}, &skew)
	if skewStatus != http.StatusConflict {
		t.Fatalf("stale If-Match status = %d, want 409: %+v", skewStatus, skew)
	}
	if skew.Code != "version_skew" {
		t.Fatalf("code = %q, want version_skew", skew.Code)
	}
}

// assertSixTypesCreate covers CUSTOM-FIELDS-PARAM-1: every closed field
// type creates end to end with the shape its type requires.
func assertSixTypesCreate(t *testing.T, e *env) {
	t.Helper()
	cases := []anyMap{
		{"object": "deal", "label": "Renewal Date", "type": "date", "source": "ui"},
		{"object": "deal", "label": "Seat Count", "type": "number", "source": "ui"},
		{"object": "deal", "label": "Deal Notes", "type": "text", "source": "ui"},
		{"object": "deal", "label": "Is Strategic", "type": "boolean", "source": "ui"},
		{"object": "deal", "label": "Budget Ceiling", "type": "currency", "currency": "USD", "source": "ui"},
		{"object": "deal", "label": "Procurement Route", "type": "picklist", "options": []string{"direct", "reseller", "marketplace"}, "source": "ui"},
	}
	for _, body := range cases {
		status, field, problem := createCustomField(t, e, body)
		if status != http.StatusCreated {
			t.Fatalf("create %v status = %d, want 201: %+v", body["type"], status, problem)
		}
		if field.Type != body["type"] {
			t.Errorf("type = %q, want %q", field.Type, body["type"])
		}
		if !cfColumnName.MatchString(field.ColumnName) {
			t.Errorf("column_name = %q, does not match %s", field.ColumnName, cfColumnName)
		}
		if field.Status != "active" {
			t.Errorf("status = %q, want active", field.Status)
		}
	}
}

// assertInjectionLabel proves BuildDDL/quoteLiteral's belt-and-suspenders
// escaping holds over the real HTTP path: a label carrying SQL-metachar
// text creates cleanly, the catalog stores the label byte-for-byte, the
// derived column identifier is alnum-only, and the person table — the
// object this field attaches to — is provably intact afterward.
func assertInjectionLabel(t *testing.T, e *env) {
	t.Helper()
	hostile := `Notes'); DROP TABLE person; --`
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "person", "label": hostile, "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}
	if field.Label != hostile {
		t.Fatalf("label = %q, want the exact original %q (catalog label is never sanitized)", field.Label, hostile)
	}
	if !cfColumnName.MatchString(field.ColumnName) {
		t.Fatalf("column_name = %q, does not match %s (the label must not leak into the derived identifier)", field.ColumnName, cfColumnName)
	}

	// The person table survives: an ordinary person write still round-trips.
	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Injection Survivor", "source": "ui",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("person table did not survive the injection attempt: create status = %d %v", status, person)
	}
}

// assertRenameStable proves CUSTOM-FIELDS-WIRE-3: rename updates label
// only, column_name never moves.
func assertRenameStable(t *testing.T, e *env) {
	t.Helper()
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Original Label", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}
	originalColumn := field.ColumnName

	var renamed customFieldWire
	renameStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID, anyMap{"label": "Renamed Label"}, nil, &renamed)
	if renameStatus != http.StatusOK {
		t.Fatalf("rename status = %d, want 200: %+v", renameStatus, renamed)
	}
	if renamed.Label != "Renamed Label" {
		t.Errorf("label = %q, want Renamed Label", renamed.Label)
	}
	if renamed.ColumnName != originalColumn {
		t.Errorf("column_name = %q, want unchanged %q across rename", renamed.ColumnName, originalColumn)
	}
}

// assertRetire proves CUSTOM-FIELDS-WIRE-4/AC-13: status flips, archived_at
// stays null — retire is a status flip, never an archive.
func assertRetire(t *testing.T, e *env) {
	t.Helper()
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "To Be Retired", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}

	var retired customFieldWire
	retireStatus := e.call(t, "POST", "/v1/custom-fields/"+field.ID+"/retire", nil, nil, &retired)
	if retireStatus != http.StatusOK {
		t.Fatalf("retire status = %d, want 200: %+v", retireStatus, retired)
	}
	if retired.Status != "retired" {
		t.Errorf("status = %q, want retired", retired.Status)
	}
	if retired.ArchivedAt != nil {
		t.Errorf("archived_at = %v, want null (retire is a status flip, not an archive)", *retired.ArchivedAt)
	}
}

// assertRetiredFieldFrozen proves retirement is terminal on the wire: a
// retired field refuses rename and options edits with the contract's 409
// conflict, while a repeat retire stays the 200 no-op.
func assertRetiredFieldFrozen(t *testing.T, e *env) {
	t.Helper()
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Frozen Route", "type": "picklist",
		"options": []string{"direct", "reseller"}, "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}
	var retired customFieldWire
	if s := e.call(t, "POST", "/v1/custom-fields/"+field.ID+"/retire", nil, nil, &retired); s != http.StatusOK {
		t.Fatalf("retire status = %d, want 200: %+v", s, retired)
	}

	var renameProblem customFieldProblem
	renameStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID,
		anyMap{"label": "Thawed Route"}, nil, &renameProblem)
	if renameStatus != http.StatusConflict || renameProblem.Code != "conflict" {
		t.Fatalf("rename on retired = %d/%q, want 409/conflict: %+v", renameStatus, renameProblem.Code, renameProblem)
	}

	var optionsProblem customFieldProblem
	optionsStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID+"/options",
		anyMap{"options": []string{"direct"}}, nil, &optionsProblem)
	if optionsStatus != http.StatusConflict || optionsProblem.Code != "conflict" {
		t.Fatalf("options on retired = %d/%q, want 409/conflict: %+v", optionsStatus, optionsProblem.Code, optionsProblem)
	}

	var again customFieldWire
	if s := e.call(t, "POST", "/v1/custom-fields/"+field.ID+"/retire", nil, nil, &again); s != http.StatusOK {
		t.Fatalf("repeat retire status = %d, want the 200 no-op: %+v", s, again)
	}
}

// assertUnknownCreateKey proves a typo'd request key answers the
// unknown_field 422 instead of being silently dropped — the create
// request schema deliberately has no additionalProperties catch-all.
func assertUnknownCreateKey(t *testing.T, e *env) {
	t.Helper()
	status, _, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Typo Subject", "type": "currency",
		"curency": "USD", "source": "ui",
	})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %+v", status, problem)
	}
	if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "body" ||
		problem.Details.Errors[0].Code != "unknown_field" {
		t.Fatalf("details.errors = %+v, want exactly [{body unknown_field}]", problem.Details.Errors)
	}
}

// assertOptionsLifecycle proves CUSTOM-FIELDS-PARAM-5: an options edit
// replaces the set and regenerates the CHECK, and emptying the set is
// refused with the contract's exact lastOption shape.
func assertOptionsLifecycle(t *testing.T, e *env) {
	t.Helper()
	status, field, problem := createCustomField(t, e, anyMap{
		"object": "deal", "label": "Deployment Region", "type": "picklist",
		"options": []string{"us-east", "eu-west", "apac"}, "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %+v", status, problem)
	}

	var updated customFieldWire
	optStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID+"/options",
		anyMap{"options": []string{"eu-west", "apac", "sa-east"}}, nil, &updated)
	if optStatus != http.StatusOK {
		t.Fatalf("options update status = %d, want 200: %+v", optStatus, updated)
	}
	if len(updated.Options) != 3 {
		t.Fatalf("options = %v, want 3 entries", updated.Options)
	}

	var lastOptionProblem customFieldProblem
	emptyStatus := e.call(t, "PATCH", "/v1/custom-fields/"+field.ID+"/options",
		anyMap{"options": []string{}}, nil, &lastOptionProblem)
	if emptyStatus != http.StatusUnprocessableEntity {
		t.Fatalf("empty-options status = %d, want 422: %+v", emptyStatus, lastOptionProblem)
	}
	if len(lastOptionProblem.Details.Errors) != 1 || lastOptionProblem.Details.Errors[0].Field != "options" ||
		lastOptionProblem.Details.Errors[0].Code != "min_one_required" {
		t.Fatalf("details.errors = %+v, want exactly [{options min_one_required}]", lastOptionProblem.Details.Errors)
	}
}

// assertListFiltering proves CUSTOM-FIELDS-WIRE-1: omitted status returns
// both lifecycle states, and object/status each narrow correctly.
func assertListFiltering(t *testing.T, e *env) {
	t.Helper()
	activeStatus, active, activeProblem := createCustomField(t, e, anyMap{
		"object": "lead", "label": "Lead Source Detail", "type": "text", "source": "ui",
	})
	if activeStatus != http.StatusCreated {
		t.Fatalf("create active field status = %d: %+v", activeStatus, activeProblem)
	}
	retiringStatus, retiring, retiringProblem := createCustomField(t, e, anyMap{
		"object": "lead", "label": "Lead Legacy Field", "type": "text", "source": "ui",
	})
	if retiringStatus != http.StatusCreated {
		t.Fatalf("create field-to-retire status = %d: %+v", retiringStatus, retiringProblem)
	}
	var retired customFieldWire
	if s := e.call(t, "POST", "/v1/custom-fields/"+retiring.ID+"/retire", nil, nil, &retired); s != http.StatusOK {
		t.Fatalf("retire status = %d, want 200: %+v", s, retired)
	}

	var both customFieldListWire
	if s := e.call(t, "GET", "/v1/custom-fields?object=lead", nil, nil, &both); s != http.StatusOK {
		t.Fatalf("list status = %d, want 200: %+v", s, both)
	}
	if !containsID(both.Data, active.ID) || !containsID(both.Data, retiring.ID) {
		t.Fatalf("omitted status must return both active and retired: %+v", both.Data)
	}

	var activeOnly customFieldListWire
	if s := e.call(t, "GET", "/v1/custom-fields?object=lead&status=active", nil, nil, &activeOnly); s != http.StatusOK {
		t.Fatalf("list active status = %d, want 200: %+v", s, activeOnly)
	}
	if !containsID(activeOnly.Data, active.ID) || containsID(activeOnly.Data, retiring.ID) {
		t.Fatalf("status=active must include only the active field: %+v", activeOnly.Data)
	}

	var retiredOnly customFieldListWire
	if s := e.call(t, "GET", "/v1/custom-fields?object=lead&status=retired", nil, nil, &retiredOnly); s != http.StatusOK {
		t.Fatalf("list retired status = %d, want 200: %+v", s, retiredOnly)
	}
	if containsID(retiredOnly.Data, active.ID) || !containsID(retiredOnly.Data, retiring.ID) {
		t.Fatalf("status=retired must include only the retired field: %+v", retiredOnly.Data)
	}

	var otherObject customFieldListWire
	if s := e.call(t, "GET", "/v1/custom-fields?object=activity", nil, nil, &otherObject); s != http.StatusOK {
		t.Fatalf("list other-object status = %d, want 200: %+v", s, otherObject)
	}
	if containsID(otherObject.Data, active.ID) || containsID(otherObject.Data, retiring.ID) {
		t.Fatalf("object filter leaked a lead field into the activity list: %+v", otherObject.Data)
	}
}

func containsID(fields []customFieldWire, id string) bool {
	for _, f := range fields {
		if f.ID == id {
			return true
		}
	}
	return false
}

// assertUnwired501 boots a SEPARATE server with no schema pool (the plain
// setup(t) default) and proves both runtime-DDL operations answer 501
// rather than nil-derefing — renameCustomField/retireCustomField/
// listCustomFields need no schema pool and are covered by the other
// subtests above, all of which ride the schema-wired server.
func assertUnwired501(t *testing.T) {
	t.Helper()
	unwired := setup(t)
	unwired.bootstrapWorkspace(t)

	var createProblem customFieldProblem
	createStatus := unwired.call(t, "POST", "/v1/custom-fields", anyMap{
		"object": "deal", "label": "Never Lands", "type": "date", "source": "ui",
	}, nil, &createProblem)
	if createStatus != http.StatusNotImplemented {
		t.Fatalf("create status = %d, want 501: %+v", createStatus, createProblem)
	}

	var optionsProblem customFieldProblem
	optionsStatus := unwired.call(t, "PATCH", "/v1/custom-fields/"+ids.NewV7().String()+"/options",
		anyMap{"options": []string{"a", "b"}}, nil, &optionsProblem)
	if optionsStatus != http.StatusNotImplemented {
		t.Fatalf("options status = %d, want 501: %+v", optionsStatus, optionsProblem)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for the five /custom-fields operations
// (customfields.Handlers, wired in compose/server.go over
// compose.WithSchemaPool). The store/engine-level suites
// (customfields_integration_test.go, customfields_lifecycle_integration_test.go)
// already prove the transaction shape, the collision resolution, and the
// RBAC/RLS gates over the Service directly; this suite proves the
// transport on top of it: request decode, the wire error shapes
// (structural_change_refused's details.route, the multi-field
// details.errors list), and that a server built WITHOUT the schema pool
// answers 501 rather than nil-derefing.
//
// Agent-tier (🟡) coverage: agentGate is agent-only middleware (humans
// never enter it — their direct call IS the approval, per
// compose/agentgate.go's own doc comment); every request this suite
// issues rides the bootstrap admin's human session cookie, so it never
// exercises the staged-approval path. That path is already covered at
// the gate/store level: TestEveryYellowToolHasADecisionGrantMapping (the
// decisionGrants fitness test) plus the compose agentgate/agentsplit
// suites prove createCustomField/retireCustomField/updateCustomFieldOptions
// resolve a decision grant and that renameCustomField is excluded from
// the per-field ownership split — minting a passport session here would
// duplicate that coverage without adding a transport-level assertion the
// gate itself doesn't already make (the gate runs BEFORE any handler in
// this package is reached).

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// customFieldWire mirrors the contract's CustomField field by field,
// decoded loosely so a wire-shape regression fails these assertions
// instead of silently zeroing.
type customFieldWire struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	Object      string   `json:"object"`
	Label       string   `json:"label"`
	Slug        string   `json:"slug"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	ColumnName  string   `json:"column_name"`
	Currency    *string  `json:"currency"`
	Options     []string `json:"options"`
	CreatedBy   string   `json:"created_by"`
	ArchivedAt  *string  `json:"archived_at"`
	Version     *int64   `json:"version"`
}

type customFieldListWire struct {
	Data []customFieldWire `json:"data"`
	Page struct {
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	} `json:"page"`
}

// customFieldProblem absorbs every 4xx/501 problem+json shape this
// surface answers: the plain sentinel mapping (code/detail only), the
// multi-field validation list, and structural_change_refused's route.
type customFieldProblem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
		Route string `json:"route"`
	} `json:"details"`
}

// cfColumnName is the cf_-prefixed physical identifier shape
// (customfields.ColumnName); every successful create must answer one,
// however hostile the source label.
var cfColumnName = regexp.MustCompile(`^cf_[a-z0-9_]+$`)

// schemaWiredEnv boots a harness server with the owner-privileged schema
// pool mounted (the integration stand-in for --schema-dsn) and leaves an
// admin session in the client jar — every subtest below rides this one
// bootstrapped workspace, since the custom-field catalog is workspace-shared
// admin config with no per-row scope to isolate between subtests.
func schemaWiredEnv(t *testing.T) *env {
	t.Helper()
	e := setupWithOptions(t, compose.WithSchemaPool(SchemaPool(t)))
	e.bootstrapWorkspace(t)
	return e
}

// createCustomField issues the create call and decodes the body as
// whichever shape the status code says is on the wire: a 2xx CustomField,
// or a 4xx/5xx problem+json. Deciding on the status code first (rather
// than trying both structs against the same bytes) sidesteps a real
// field-name collision: the problem envelope's `status` is the JSON
// number http.StatusCode, while CustomField's own `status` is the
// lifecycle string (active/retired) — unmarshaling one body into the
// other's struct would fail on that type mismatch, not just leave zeros.
func createCustomField(t *testing.T, e *env, body anyMap) (int, customFieldWire, customFieldProblem) {
	t.Helper()
	var raw json.RawMessage
	status := e.call(t, "POST", "/v1/custom-fields", body, nil, &raw)
	var field customFieldWire
	var problem customFieldProblem
	if len(raw) == 0 {
		return status, field, problem
	}
	if status >= http.StatusBadRequest {
		if err := json.Unmarshal(raw, &problem); err != nil {
			t.Fatalf("decoding problem body: %v (body: %s)", err, raw)
		}
		return status, field, problem
	}
	if err := json.Unmarshal(raw, &field); err != nil {
		t.Fatalf("decoding CustomField body: %v (body: %s)", err, raw)
	}
	return status, field, problem
}

func TestCustomFieldsHTTP(t *testing.T) {
	e := schemaWiredEnv(t)

	t.Run("all six types create 201", func(t *testing.T) {
		assertSixTypesCreate(t, e)
	})

	t.Run("422 validation details.errors shape", func(t *testing.T) {
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
	})

	t.Run("422 structural change refused", func(t *testing.T) {
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
	})

	t.Run("injection label sanitized, catalog label preserved, person table survives", func(t *testing.T) {
		assertInjectionLabel(t, e)
	})

	t.Run("rename keeps column_name stable", func(t *testing.T) {
		assertRenameStable(t, e)
	})

	t.Run("retire flips status, archived_at stays null", func(t *testing.T) {
		assertRetire(t, e)
	})

	t.Run("options add/remove and last-option 422", func(t *testing.T) {
		assertOptionsLifecycle(t, e)
	})

	t.Run("list: active+retired default, object filter, status filter", func(t *testing.T) {
		assertListFiltering(t, e)
	})

	t.Run("409 duplicate slug in the same workspace", func(t *testing.T) {
		body := anyMap{"object": "deal", "label": "Renewal Window", "type": "text", "source": "ui"}
		first, field, _ := createCustomField(t, e, body)
		if first != http.StatusCreated {
			t.Fatalf("first create status = %d, want 201: %+v", first, field)
		}
		second, _, problem := createCustomField(t, e, body)
		if second != http.StatusConflict {
			t.Fatalf("second create status = %d, want 409: %+v", second, problem)
		}
	})

	t.Run("rename: malformed If-Match and stale version", func(t *testing.T) {
		assertRenameConcurrencyErrors(t, e)
	})

	t.Run("404 retire unknown id", func(t *testing.T) {
		var problem customFieldProblem
		status := e.call(t, "POST", "/v1/custom-fields/"+ids.NewV7().String()+"/retire", nil, nil, &problem)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %+v", status, problem)
		}
	})

	t.Run("422 invalid object on list", func(t *testing.T) {
		var problem customFieldProblem
		status := e.call(t, "GET", "/v1/custom-fields?object=bogus", nil, nil, &problem)
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %+v", status, problem)
		}
		if len(problem.Details.Errors) != 1 || problem.Details.Errors[0].Field != "object" {
			t.Fatalf("details.errors = %+v, want a single object error", problem.Details.Errors)
		}
	})

	t.Run("422 not_picklist on a non-picklist field's options", func(t *testing.T) {
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
	})

	t.Run("501 when the server has no schema pool wired", func(t *testing.T) {
		assertUnwired501(t)
	})
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

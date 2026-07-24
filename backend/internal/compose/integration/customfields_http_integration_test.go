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
// the gate/store level: TestEveryConfirmationRequiredToolHasADecisionGrantMapping (the
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
		assertValidationDetails(t, e)
	})

	t.Run("422 structural change refused", func(t *testing.T) {
		assertStructuralChangeRefused(t, e)
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

	t.Run("409 rename and options on a retired field", func(t *testing.T) {
		assertRetiredFieldFrozen(t, e)
	})

	t.Run("422 unknown key on create", func(t *testing.T) {
		assertUnknownCreateKey(t, e)
	})

	t.Run("options add/remove and last-option 422", func(t *testing.T) {
		assertOptionsLifecycle(t, e)
	})

	t.Run("list: active+retired default, object filter, status filter", func(t *testing.T) {
		assertListFiltering(t, e)
	})

	t.Run("409 duplicate slug in the same workspace", func(t *testing.T) {
		assertDuplicateSlugConflict(t, e)
	})

	t.Run("rename: malformed If-Match and stale version", func(t *testing.T) {
		assertRenameConcurrencyErrors(t, e)
	})

	t.Run("404 retire unknown id", func(t *testing.T) {
		assertRetireUnknownID(t, e)
	})

	t.Run("422 invalid object on list", func(t *testing.T) {
		assertListInvalidObject(t, e)
	})

	t.Run("422 not_picklist on a non-picklist field's options", func(t *testing.T) {
		assertNotPicklistOptionsEdit(t, e)
	})

	t.Run("501 when the server has no schema pool wired", func(t *testing.T) {
		assertUnwired501(t)
	})
}

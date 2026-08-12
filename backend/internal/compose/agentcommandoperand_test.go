// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the eight bespoke commands (agentcommandoperand.go):
// the routed {id}'s existence-hiding 404, the second path operand's 422, and
// the staged target each decoder resolves to — the same proof shape
// agentcommand_test.go gives archive/patch, for a family whose operand lives
// in a SECOND path parameter rather than in the body.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// operandRequest builds a request for a route carrying the router's own {id}
// (as the raw path segment routeID — a malformed one is what proves the 404)
// plus an optional second path parameter the chi router would have bound —
// factKey, field, or person_id.
func operandRequest(method, path, routeID, extraParam, extraValue string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path+"/"+routeID, bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", routeID)
	if extraParam != "" {
		rctx.URLParams.Add(extraParam, extraValue)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// A malformed routed {id} answers 404 for every one of the eight, the same
// existence-hiding answer archiveCommand/patchCommand already give — proven
// once per decoder rather than assuming the shared routedID helper carries
// the property for free.
func TestAMalformedOperandRouteIDAnswersNotFound(t *testing.T) {
	cases := []struct {
		name   string
		decode func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error)
		req    *http.Request
	}{
		{"confirmOrganizationFact", confirmFactCommand, operandRequest(http.MethodPost, "/v1/organizations", "not-a-uuid", "factKey", "k", nil)},
		{"updateOrganizationFact", updateFactCommand, operandRequest(http.MethodPatch, "/v1/organizations", "not-a-uuid", "factKey", "k", []byte(`{"value":"v"}`))},
		{"confirmOrganizationProfileField", confirmProfileFieldCommand, operandRequest(http.MethodPost, "/v1/organizations", "not-a-uuid", "field", "icp", nil)},
		{"updateOrganizationProfileField", updateProfileFieldCommand, operandRequest(http.MethodPatch, "/v1/organizations", "not-a-uuid", "field", "icp", []byte(`{"value":"v"}`))},
		{"retireCustomField", retireCustomFieldCommand, operandRequest(http.MethodPost, "/v1/custom-fields", "not-a-uuid", "", "", nil)},
		{"updateCustomFieldOptions", updateCustomFieldOptionsCommand, operandRequest(http.MethodPatch, "/v1/custom-fields", "not-a-uuid", "", "", []byte(`{"options":["a"]}`))},
		{"setProjectStakeholder", setStakeholderCommand, operandRequest(http.MethodPut, "/v1/projects", "not-a-uuid", "", "", []byte(`{"person_id":"018f2a10-0000-7000-8000-000000000001","role":"champion"}`))},
		{"removeProjectStakeholder", removeStakeholderCommand, operandRequest(http.MethodDelete, "/v1/projects", "not-a-uuid", "person_id", ids.NewV7().String(), nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.decode(agentPolicy{Op: c.name}, restCommandDeps{records: seamRecord{}}, c.req, nil); !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("decoding a malformed id answered %v, want the not-found sentinel", err)
			}
		})
	}
}

// A missing second path operand — a request built without the segment the
// router would otherwise have bound — answers 422 naming it, not a panic on
// an empty FactKey/Field downstream.
func TestAMissingSecondPathOperandAnswers422(t *testing.T) {
	id := ids.NewV7().String()
	cases := []struct {
		name      string
		decode    func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error)
		body      []byte
		wantField string
	}{
		{"confirmOrganizationFact", confirmFactCommand, nil, "factKey"},
		{"updateOrganizationFact", updateFactCommand, []byte(`{"value":"v"}`), "factKey"},
		{"confirmOrganizationProfileField", confirmProfileFieldCommand, nil, "field"},
		{"updateOrganizationProfileField", updateProfileFieldCommand, []byte(`{"value":"v"}`), "field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// extraParam left empty: the route matched (a valid {id}), but the
			// second segment the router would bind was never set — the shape a
			// routing bug, not a malformed request, would produce.
			req := operandRequest(http.MethodPost, "/v1/organizations", id, "", "", c.body)
			_, err := c.decode(agentPolicy{Op: c.name}, restCommandDeps{records: seamRecord{}}, req, c.body)
			var detailed *httperr.DetailedError
			if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
				t.Fatalf("a missing %s answered %v, want a 422 naming it", c.wantField, err)
			}
			if len(detailed.Fields) != 1 || detailed.Fields[0].Field != c.wantField {
				t.Errorf("the 422 named field %+v, want %q", detailed.Fields, c.wantField)
			}
		})
	}
}

// A malformed person_id on removeProjectStakeholder is a 422, not the 404
// the routed {id} gets: it names WHICH edge, not whether the project exists,
// so its shape being wrong is the caller's mistake, never an existence leak.
func TestARemoveStakeholderMalformedPersonIDAnswers422(t *testing.T) {
	req := operandRequest(http.MethodDelete, "/v1/projects", ids.NewV7().String(), "person_id", "not-a-uuid", nil)
	_, err := removeStakeholderCommand(agentPolicy{Op: "removeProjectStakeholder"}, restCommandDeps{records: seamRecord{}}, req, nil)
	var detailed *httperr.DetailedError
	if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed person_id answered %v, want a 422", err)
	}
}

// Each of the eight stages against the routed record it names — proven
// through stageRefusal end to end, the same shape TestAPatchStagesItsRecordAndID
// proves for a whole-record patch.
func TestEachOperandCommandStagesTheRoutedRecord(t *testing.T) {
	orgID, projectID, cfID := ids.NewV7(), ids.NewV7(), ids.NewV7()
	cases := []struct {
		name           string
		pol            agentPolicy
		req            *http.Request
		body           []byte
		wantTargetType string
		wantTargetID   ids.UUID
	}{
		{
			"confirmOrganizationFact",
			agentPolicy{Op: "confirmOrganizationFact", Access: accessTool, Tool: "update_record", RecordType: recordTypeOrganization},
			operandRequest(http.MethodPost, "/v1/organizations", orgID.String(), "factKey", "named_customer:acme-inc", nil), nil,
			"organization", orgID,
		},
		{
			"updateOrganizationFact",
			agentPolicy{Op: "updateOrganizationFact", Access: accessTool, Tool: "update_record", RecordType: recordTypeOrganization},
			operandRequest(http.MethodPatch, "/v1/organizations", orgID.String(), "factKey", "named_customer:acme-inc", []byte(`{"value":"Acme Inc"}`)),
			[]byte(`{"value":"Acme Inc"}`), "organization", orgID,
		},
		{
			"confirmOrganizationProfileField",
			agentPolicy{Op: "confirmOrganizationProfileField", Access: accessTool, Tool: "update_record", RecordType: recordTypeOrganization},
			operandRequest(http.MethodPost, "/v1/organizations", orgID.String(), "field", "icp", nil), nil,
			"organization", orgID,
		},
		{
			"updateOrganizationProfileField",
			agentPolicy{Op: "updateOrganizationProfileField", Access: accessTool, Tool: "update_record", RecordType: recordTypeOrganization},
			operandRequest(http.MethodPatch, "/v1/organizations", orgID.String(), "field", "icp", []byte(`{"value":"Payments infra"}`)),
			[]byte(`{"value":"Payments infra"}`), "organization", orgID,
		},
		{
			"retireCustomField",
			agentPolicy{Op: "retireCustomField", Access: accessTool, Tool: "update_record", RecordType: recordTypeCustomField},
			operandRequest(http.MethodPost, "/v1/custom-fields", cfID.String(), "", "", nil), nil,
			"custom_field", cfID,
		},
		{
			"updateCustomFieldOptions",
			agentPolicy{Op: "updateCustomFieldOptions", Access: accessTool, Tool: "update_record", RecordType: recordTypeCustomField},
			operandRequest(http.MethodPatch, "/v1/custom-fields", cfID.String(), "", "", []byte(`{"options":["a","b"]}`)),
			[]byte(`{"options":["a","b"]}`), "custom_field", cfID,
		},
		{
			"setProjectStakeholder",
			agentPolicy{Op: "setProjectStakeholder", Access: accessTool, Tool: "update_record", RecordType: recordTypeProject},
			operandRequest(http.MethodPut, "/v1/projects", projectID.String(), "", "", []byte(`{"person_id":"018f2a10-0000-7000-8000-000000000001","role":"champion"}`)),
			[]byte(`{"person_id":"018f2a10-0000-7000-8000-000000000001","role":"champion"}`), "project", projectID,
		},
		{
			"removeProjectStakeholder",
			agentPolicy{Op: "removeProjectStakeholder", Access: accessTool, Tool: "update_record", RecordType: recordTypeProject},
			operandRequest(http.MethodDelete, "/v1/projects", projectID.String(), "person_id", ids.NewV7().String(), nil), nil,
			"project", projectID,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			staging := &capturingApprovals{}
			stageRefusal(httptest.NewRecorder(), c.req, staging, restCommandDeps{records: seamRecord{}}, c.pol, c.body)

			if staging.last.TargetType != c.wantTargetType || staging.last.TargetID != c.wantTargetID {
				t.Fatalf("staged target = (%s,%s), want (%s,%s)",
					staging.last.TargetType, staging.last.TargetID, c.wantTargetType, c.wantTargetID)
			}
		})
	}
}

// This is the actual behavior change registering these eight in restCommands
// buys over the route-walk fallback (stagedTargetByRoute): Guards now runs.
// An organization or project the caller cannot see stages NOTHING — the same
// proof shape TestAnArchiveOfAnUnseeableRecordStagesNothing gives archive —
// for one op from each seam-served family (organization, project); the two
// custom_field ops have no such proof because the seam has never served that
// type (TestEachOperandCommandStagesTheRoutedRecord above already stages
// them against `seamRecord{}` without incident, which is what proves Guards
// does not even attempt a read for them).
func TestAnOperandCommandOfAnUnseeableRecordStagesNothing(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "confirmOrganizationFact", Access: accessTool, Tool: "update_record", RecordType: recordTypeOrganization}
	req := operandRequest(http.MethodPost, "/v1/organizations", ids.NewV7().String(), "factKey", "named_customer:acme-inc", nil)
	rec := httptest.NewRecorder()

	stageRefusal(rec, req, staging, restCommandDeps{records: hiddenRecord{}}, pol, nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("confirming a fact on an organization the caller cannot see answered %d, want 404 — the "+
			"refusal must not tell a caller that a row they may not see exists", rec.Code)
	}
	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against an organization nobody can decide about", staging.last.Tool)
	}
}

// The other refusal Guards makes: an organization/project the caller CAN see
// but whose authority lives in another system of record — readable, and
// still unstageable, the same shape TestAnArchiveOfAnExternallyHeldRecordStagesNothing
// gives archive.
func TestAnOperandCommandOfARecordHeldElsewhereStagesNothing(t *testing.T) {
	staging := &capturingApprovals{}
	body := []byte(`{"person_id":"018f2a10-0000-7000-8000-000000000001","role":"champion"}`)
	pol := agentPolicy{Op: "setProjectStakeholder", Access: accessTool, Tool: "update_record", RecordType: recordTypeProject}
	req := operandRequest(http.MethodPut, "/v1/projects", ids.NewV7().String(), "", "", body)
	rec := httptest.NewRecorder()

	stageRefusal(rec, req, staging, restCommandDeps{records: mirroredRecord{}}, pol, body)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a project whose authority lives elsewhere — nobody "+
			"could ever release it", staging.last.Tool)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an externally-held target answered %d, want %d (unsupported_by_sor)", rec.Code, http.StatusUnprocessableEntity)
	}
}

// The record type each of these eight resolvers is hardcoded against
// (organizationSidecarRecordType, customFieldRecordType, projectRecordType
// — commandsidecar.go, commandaction.go) must agree with the generated
// policy table, or a contract change could silently point the gate at one
// record type while the resolver's Guards reads another. Nothing else pins
// that agreement: the decoders all discard `pol` (its RecordType is not
// threaded through, unlike archiveCommand/patchCommand's), so this is a
// fitness function over agentPolicies rather than a point assertion.
func TestOperandCommandRecordTypesAgreeWithThePolicyTable(t *testing.T) {
	want := map[string]string{
		"confirmOrganizationFact":         "organization",
		"updateOrganizationFact":          "organization",
		"confirmOrganizationProfileField": "organization",
		"updateOrganizationProfileField":  "organization",
		"retireCustomField":               "custom_field",
		"updateCustomFieldOptions":        "custom_field",
		"setProjectStakeholder":           "project",
		"removeProjectStakeholder":        "project",
	}
	found := make(map[string]bool, len(want))
	for _, pol := range agentPolicies {
		wantType, tracked := want[pol.Op]
		if !tracked {
			continue
		}
		found[pol.Op] = true
		if string(pol.RecordType) != wantType {
			t.Errorf("%s declares RecordType %q in the generated policy table, want %q — its resolver's "+
				"hardcoded record type would silently disagree with a contract change",
				pol.Op, pol.RecordType, wantType)
		}
	}
	for op := range want {
		if !found[op] {
			t.Errorf("%s no longer appears in the generated policy table", op)
		}
	}
}

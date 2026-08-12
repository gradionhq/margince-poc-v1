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
	"strings"
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
// an empty FactKey/Field downstream. removeProjectStakeholder's person_id is
// the one operand composed from pathOperand + ids.Parse (agentcommandoperand.go)
// rather than pathOperand alone, so it is included here too: a missing one
// must still answer "missing" through that composition, not fall through to
// ids.Parse("") and answer the malformed-shape code instead.
func TestAMissingSecondPathOperandAnswers422(t *testing.T) {
	id := ids.NewV7().String()
	cases := []struct {
		name      string
		method    string
		path      string
		decode    func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error)
		body      []byte
		wantField string
	}{
		{"confirmOrganizationFact", http.MethodPost, "/v1/organizations", confirmFactCommand, nil, "factKey"},
		{"updateOrganizationFact", http.MethodPatch, "/v1/organizations", updateFactCommand, nil, "factKey"},
		{"confirmOrganizationProfileField", http.MethodPost, "/v1/organizations", confirmProfileFieldCommand, nil, "field"},
		{"updateOrganizationProfileField", http.MethodPatch, "/v1/organizations", updateProfileFieldCommand, nil, "field"},
		{"removeProjectStakeholder", http.MethodDelete, "/v1/projects", removeStakeholderCommand, nil, "person_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// extraParam left empty: the route matched (a valid {id}), but the
			// second segment the router would bind was never set — the shape a
			// routing bug, not a malformed request, would produce.
			req := operandRequest(c.method, c.path, id, "", "", c.body)
			_, err := c.decode(agentPolicy{Op: c.name}, restCommandDeps{records: seamRecord{}}, req, c.body)
			var detailed *httperr.DetailedError
			if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
				t.Fatalf("a missing %s answered %v, want a 422 naming it", c.wantField, err)
			}
			if len(detailed.Fields) != 1 || detailed.Fields[0].Field != c.wantField || detailed.Fields[0].Code != "missing" {
				t.Errorf("the 422 named %+v, want field %q code \"missing\"", detailed.Fields, c.wantField)
			}
		})
	}
}

// A malformed (non-empty) person_id on removeProjectStakeholder is also a
// 422, code "invalid" rather than "missing" — the other half of the
// pathOperand + ids.Parse composition the test above proves the missing
// case for. Neither is the 404 the routed {id} gets: person_id names WHICH
// edge, not whether the project exists, so its shape being wrong is the
// caller's mistake, never an existence leak.
func TestARemoveStakeholderMalformedPersonIDAnswers422(t *testing.T) {
	req := operandRequest(http.MethodDelete, "/v1/projects", ids.NewV7().String(), "person_id", "not-a-uuid", nil)
	_, err := removeStakeholderCommand(agentPolicy{Op: "removeProjectStakeholder"}, restCommandDeps{records: seamRecord{}}, req, nil)
	var detailed *httperr.DetailedError
	if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed person_id answered %v, want a 422", err)
	}
	if len(detailed.Fields) != 1 || detailed.Fields[0].Field != "person_id" || detailed.Fields[0].Code != "invalid" {
		t.Errorf("the 422 named %+v, want field \"person_id\" code \"invalid\"", detailed.Fields)
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

// What resolving these eight through their own commands buys, and it is a
// refusal rather than a label: Guards runs before anything stages.
// An organization or project the caller cannot see stages NOTHING — the same
// proof shape TestAnArchiveOfAnUnseeableRecordStagesNothing gives archive —
// for one op from each seam-served family (organization, project). The two
// custom_field ops have no such proof: the seam has never served that type,
// so there is no read for Guards to skip. That they never attempt one is
// TestCustomFieldCommandsStageAndAdmitOutsideTheRecordSeam's own claim
// (modules/agents/commandaction_test.go), proven there against
// unreadableProvider{} — a provider that fails every read, so a resolver
// that consulted it anyway would fail that test rather than pass here:
// TestEachOperandCommandStagesTheRoutedRecord's use of `seamRecord{}` (every
// read succeeds) cannot tell "never read" apart from "read and got lucky".
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

// syntheticOperandRequest builds a well-formed request for route (an
// agentPolicies key, "METHOD /path/{param}/…"), binding {id} to id and every
// OTHER path parameter to a fresh, distinct uuid — enough for any of this
// family's decoders to succeed without this test needing to know which
// parameter names a given route carries (factKey and field accept any
// non-empty string; a uuid satisfies that as well as anything, and is what
// person_id's own ids.Parse requires).
func syntheticOperandRequest(route string, id ids.UUID) *http.Request {
	method, template, _ := strings.Cut(route, " ")
	segments := strings.Split(strings.TrimPrefix(template, "/"), "/")
	rctx := chi.NewRouteContext()
	built := make([]string, 0, len(segments))
	for _, seg := range segments {
		name, isParam := strings.CutPrefix(seg, "{")
		if !isParam {
			built = append(built, seg)
			continue
		}
		name = strings.TrimSuffix(name, "}")
		val := ids.NewV7().String()
		if name == "id" {
			val = id.String()
		}
		rctx.URLParams.Add(name, val)
		built = append(built, val)
	}
	req := httptest.NewRequest(method, "/"+strings.Join(built, "/"), nil)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// What a registration does not say: that the decoder bound to an operation is
// the RIGHT one. TestEveryAgentReachableMutatingRouteDecodesIntoACommand
// (agentcommandcoverage_test.go) proves every one of these routes HAS an entry;
// a mis-wired entry satisfies it completely, because a swapped decoder still
// stages something — often something that looks right, since most of this
// family stages the routed record.
//
// So this walk invokes the bound decoder against a synthetic request for the
// route the policy table names, and checks what it staged against what that
// same table declared: the record type, and the routed id rather than one of
// the route's OTHER path parameters.
//
// Derived from agentPolicies rather than a hand-listed set of eight op names:
// TestEachOperandCommandStagesTheRoutedRecord above hand-picks its eight and
// would not notice a NINTH such operation the contract grows.
//
// The whole-record patch shape (route ends exactly at /{id}) is excluded
// because a synthetic request cannot exercise it: those decoders read a BODY,
// and a body is per-operation where a path parameter is not. Their equivalent
// proof is TestAPatchStagesItsRecordAndID and its siblings above.
// operandBodies carries a minimal contract body for the one route in this
// family whose operand is not in the path at all: setProjectStakeholder names
// its person in the BODY, and its decoder refuses a request that names none
// (agentcommandoperand.go). Every other route here is decodable from the path
// alone, which is what lets the walk be synthetic.
//
// gatekit:fixture the minimal contract body the one body-carrying route in this family declares — expected input, not a waived cost
var operandBodies = map[string]string{
	"setProjectStakeholder": `{"person_id":"019ff000-0000-7000-8000-000000000031","role":"champion"}`,
}

func TestEveryConfirmFirstOperandRouteDecodesIntoTheRightCommand(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tool != "update_record" || pol.Tier != tierConfirmationRequired {
			continue
		}
		if strings.HasSuffix(route, "/{id}") {
			continue
		}
		checked++

		decode, described := restCommands[pol.Op]
		if !described {
			// The completeness gate reports this route by name; here it would
			// only be a nil decoder to dereference.
			continue
		}

		id := ids.NewV7()
		req := syntheticOperandRequest(route, id)
		body := []byte(operandBodies[pol.Op])
		call, err := decode(pol, restCommandDeps{records: seamRecord{}}, req, body)
		if err != nil {
			t.Errorf("%s (%s): decoding a well-formed request answered %v", route, pol.Op, err)
			continue
		}
		info, err := call.Subject(context.Background())
		if err != nil {
			t.Errorf("%s (%s): naming the subject answered %v", route, pol.Op, err)
			continue
		}
		if info.TargetType != string(pol.RecordType) {
			t.Errorf("%s (%s) stages target type %q, want %q — the policy table's own declared record type",
				route, pol.Op, info.TargetType, pol.RecordType)
		}
		if info.TargetID != id {
			t.Errorf("%s (%s) stages target id %s, want %s — restCommands binds this operationId to the "+
				"WRONG decoder, or the decoder read the wrong path parameter as {id}", route, pol.Op, info.TargetID, id)
		}
	}
	// No literal count: how many such routes exist is the contract's business,
	// and the completeness gate is what notices one arriving. What this walk
	// owes is that it ran at all — a filter that selected nothing would report
	// every decoder correctly wired without invoking one.
	if checked == 0 {
		t.Fatal("no confirm-first update_record route carries more than the routed {id} — this walk invoked " +
			"no decoder, and would pass against every mis-wiring it exists to catch")
	}
}

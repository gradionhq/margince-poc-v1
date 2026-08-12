// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The create and patch families' half of the governance seam
// (modules/agents/command.go), the same shape agentcommand_test.go proves for
// archive: createCommand and patchCommand, registered in restCommands
// alongside archiveCommand, resolving through agents.NewCreateCall /
// agents.NewPatchCall.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// createRequest is a POST against one of the create routes, carrying body as
// the request payload — the shape createCommand decodes. Unlike an archive or
// a patch, a create route carries no {id} for the router to have bound.
func createRequest(path string, body []byte) *http.Request {
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
}

// patchRequest is a PATCH against one of the patch routes, carrying the {id}
// the chi router would have bound plus the request body.
func patchRequest(path string, id ids.UUID, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, path+"/"+id.String(), bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// Every create operation the contract lets an agent reach must decode into a
// command. Derived from the generated policy table rather than a remembered
// list of thirteen: create_record's own tool tag also covers createOffer
// (POST /v1/deals/{id}/offers), which nests under a parent resource and is
// out of this family's scope — so the filter is the route shape a top-level
// collection create actually has, a bare collection path with no `{`, which
// is what tells the two apart without hand-naming either.
//
// Every one of the thirteen belongs here, including the six whose record
// type (custom_field, list, offer_template, product, saved_view, tag)
// create_record's own Handle cannot write: createResolver.Guards
// (command.go) asks nothing about whether the verb "serves" a type, so
// registering them costs nothing, and the door-dependent question that
// mattered is answered once, at createRecord.StageInfo, on the door where it
// is true.
func TestEveryAgentReachableCreateOperationDecodesIntoACommand(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tool != "create_record" || strings.Contains(route, "{") {
			continue
		}
		checked++
		if _, described := restCommands[pol.Op]; !described {
			t.Errorf("%s (%s) creates a record but decodes into no command, so its staged target is still "+
				"guessed from the route while the tool door reads it from the call", route, pol.Op)
		}
	}
	if checked != 13 {
		t.Errorf("the policy table carries %d agent-reachable top-level create operations, want 13 — if the "+
			"contract gained or lost one, this seam's coverage moved with it", checked)
	}
}

// Every whole-record write operation the contract lets an agent reach must
// decode into a command. update_record's own tool tag covers far more than a
// whole-record write — child-resource and membership mutations like
// updateOfferLineItem or applyTag carry a second path segment or a second
// path parameter after {id}, and agentsplit.go's actionShapedUpdateOps
// already draws that line for the auto-execute side. The filter here draws
// the same line independently, by ROUTE SHAPE alone: one path parameter,
// named id, at the very end, and nothing after it.
//
// The shape, not the method. Twelve of these route as PATCH and one — the
// offer template's — as PUT, a full replace rather than a field patch, and
// the difference changes nothing this seam answers: both name the record in
// {id} and carry that record's fields in the body. A method-keyed filter is
// how the PUT came to be invisible to every gate on this surface at once,
// including canonicalCollectionRoute (agentcommandnested_test.go), which
// excused it as covered HERE.
//
// Unlike create, this family never had a "verb does not serve" refusal to
// worry about (see patchResolver.Guards' own comment in command.go), so
// every one of these thirteen belongs here.
func TestEveryAgentReachableWholeRecordWriteOperationDecodesIntoACommand(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tool != "update_record" {
			continue
		}
		if !strings.HasSuffix(route, "/{id}") || strings.Count(route, "{") != 1 {
			continue
		}
		checked++
		if _, described := restCommands[pol.Op]; !described {
			t.Errorf("%s (%s) writes a whole record but decodes into no command, so its staged target is "+
				"still guessed from the route while the tool door reads it from the call", route, pol.Op)
		}
	}
	if checked != 13 {
		t.Errorf("the policy table carries %d agent-reachable whole-record write operations, want 13 — if the "+
			"contract gained or lost one, this seam's coverage moved with it", checked)
	}
}

// A served create type stages the record TYPE with no id: the row does not
// exist yet, so there is nothing for the approvals surface to probe or pin.
func TestACreateStagesItsRecordTypeWithNoTargetID(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "createProject", Access: accessTool, Tool: "create_record", RecordType: recordTypeProject}
	body := []byte(`{"name":"New Project","organization_id":"018f2a10-0000-7000-8000-000000000001"}`)

	stageRefusal(httptest.NewRecorder(), createRequest("/v1/projects", body), staging, restCommandDeps{}, pol, body)

	if staging.last.TargetType != "project" {
		t.Fatalf("staged target_type = %q, want \"project\"", staging.last.TargetType)
	}
	if !staging.last.TargetID.IsZero() {
		t.Errorf("staged target_id = %s, want zero — a create names no row an approval could pin",
			staging.last.TargetID)
	}
}

// A create OUTSIDE create_record's own served vocabulary still stages,
// through createResolver like every other registered create — the same shape
// TestAnArchiveOutsideTheToolSchemaStagesTheRowItNames proves for archive.
// custom_field is one of the six create routes whose record type
// create_record's own Handle cannot write, and createResolver.Guards has no
// opinion on that (command.go); proving it stages successfully through the
// resolver is what makes that indifference correct rather than assumed.
func TestACreateOutsideTheToolSchemaStagesItsRecordTypeWithNoTargetID(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "createCustomField", Access: accessTool, Tool: "create_record", RecordType: recordTypeCustomField}
	body := []byte(`{"name":"Champion Score","field_type":"text"}`)

	stageRefusal(httptest.NewRecorder(), createRequest("/v1/custom-fields", body), staging, restCommandDeps{}, pol, body)

	if staging.last.TargetType != "custom_field" {
		t.Fatalf("staged target_type = %q, want \"custom_field\"", staging.last.TargetType)
	}
	if !staging.last.TargetID.IsZero() {
		t.Errorf("staged target_id = %s, want zero", staging.last.TargetID)
	}
}

// A served patch type stages the record TYPE and the id the route named — the
// row a human's decision reads and the redemption pins.
func TestAPatchStagesItsRecordAndID(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "updateProject", Access: accessTool, Tool: "update_record", RecordType: recordTypeProject}
	projectID := ids.NewV7()
	body := []byte(`{"name":"Renamed"}`)

	stageRefusal(httptest.NewRecorder(), patchRequest("/v1/projects", projectID, body), staging,
		restCommandDeps{records: seamRecord{}}, pol, body)

	if staging.last.TargetType != "project" || staging.last.TargetID != projectID {
		t.Fatalf("staged target = (%s,%s), want (project,%s)", staging.last.TargetType, staging.last.TargetID, projectID)
	}
}

// A malformed id is a miss, not a parse failure, for a patch exactly as it is
// for an archive: "that is not a uuid" and "there is no such row" must read
// alike.
func TestAMalformedPatchIDAnswersNotFound(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "updateProject", Access: accessTool, Tool: "update_record", RecordType: recordTypeProject}
	body := []byte(`{"name":"Renamed"}`)

	req := httptest.NewRequest(http.MethodPatch, "/v1/projects/not-a-uuid", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	stageRefusal(rec, req, staging, restCommandDeps{records: seamRecord{}}, pol, body)

	if rec.Code != http.StatusNotFound {
		t.Errorf("a malformed patch id answered %d, want 404", rec.Code)
	}
	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against an id that names no row", staging.last.Tool)
	}
}

// A patch record type the record seam does not serve still stages, with its
// own type and id — the servedByTheRecordSeam short-circuit
// (modules/agents/command.go) standing down rather than faulting on a read
// the seam cannot answer. Staged against a provider that fails EVERY read, so
// a resolver that consulted the seam anyway fails here rather than passing on
// a lenient stub — the same proof shape as
// TestAnArchiveOutsideTheToolSchemaStagesTheRowItNames.
func TestAPatchOutsideTheToolSchemaStagesTheRowItNames(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{
		Op: "updateWebhookSubscription", Access: accessTool, Tool: "update_record",
		RecordType: recordTypeWebhookSubscription,
	}
	subID := ids.NewV7()
	body := []byte(`{"state":"paused"}`)

	stageRefusal(httptest.NewRecorder(), patchRequest("/v1/webhook-subscriptions", subID, body), staging,
		restCommandDeps{records: hiddenRecord{}}, pol, body)

	if staging.last.TargetType != "webhook_subscription" || staging.last.TargetID != subID {
		t.Fatalf("staged target = (%s,%s), want (webhook_subscription,%s) — a webhook subscription is patched "+
			"over REST whether or not the record seam has ever heard of one",
			staging.last.TargetType, staging.last.TargetID, subID)
	}
}

// An unknown field over REST answers 422 validation_error, not an opaque 500.
// rejectUnknownFields (modules/agents/recordfields.go) returns a
// *agents.BadArgsError, an MCP-tool-door type httperr.Classify has no
// built-in notion of — this is the first REST path to reach one, and without
// BadArgsError.MessageFault (badargs.go) this would answer as an unhandled
// server fault instead of the caller mistake it is.
func TestAPatchWithAnUnknownFieldAnswers422NotAn500(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "updateProject", Access: accessTool, Tool: "update_record", RecordType: recordTypeProject}
	projectID := ids.NewV7()
	body := []byte(`{"nickname":"typo"}`)

	rec := httptest.NewRecorder()
	stageRefusal(rec, patchRequest("/v1/projects", projectID, body), staging,
		restCommandDeps{records: seamRecord{}}, pol, body)

	var problem struct {
		Code string `json:"code"`
	}
	if decErr := json.NewDecoder(rec.Body).Decode(&problem); decErr != nil {
		t.Fatalf("decoding the problem body: %v", decErr)
	}
	if rec.Code != http.StatusUnprocessableEntity || problem.Code != "validation_error" {
		t.Errorf("an unknown field answered %d %q, want %d validation_error", rec.Code, problem.Code, http.StatusUnprocessableEntity)
	}
	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a payload that was never a valid patch", staging.last.Tool)
	}
}

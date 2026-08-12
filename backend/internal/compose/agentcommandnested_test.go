// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the seven bespoke auto-execute commands
// (agentcommandnested.go): the routed {id}'s existence-hiding 404, the
// offer line items' own {lineItemId} 422, the staged target each decoder
// resolves to, and the derived coverage gate that pins all seven off the
// route walk — task 6's sibling of agentcommandoperand_test.go's proof
// shape for task 5's eight.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// A malformed routed {id} answers 404 for every one of the seven, the same
// existence-hiding answer archiveCommand/patchCommand and task 5's eight
// already give.
func TestANestedCommandMalformedRouteIDAnswersNotFound(t *testing.T) {
	cases := []struct {
		name   string
		decode func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error)
		req    *http.Request
	}{
		{"addListMember", addListMemberCommand, operandRequest(http.MethodPost, "/v1/lists", "not-a-uuid", "", "", nil)},
		{"applyTag", applyTagCommand, operandRequest(http.MethodPost, "/v1/tags", "not-a-uuid", "", "", nil)},
		{
			"addOfferLineItem", addOfferLineItemCommand,
			operandRequest(http.MethodPost, "/v1/offers", "not-a-uuid", "", "", []byte(`{}`)),
		},
		{
			"updateOfferLineItem", updateOfferLineItemCommand,
			operandRequest(http.MethodPatch, "/v1/offers", "not-a-uuid", "lineItemId", ids.NewV7().String(), []byte(`{}`)),
		},
		{
			"removeOfferLineItem", removeOfferLineItemCommand,
			operandRequest(http.MethodDelete, "/v1/offers", "not-a-uuid", "lineItemId", ids.NewV7().String(), nil),
		},
		{
			"createOffer", createOfferCommand,
			operandRequest(http.MethodPost, "/v1/deals", "not-a-uuid", "", "", []byte(`{}`)),
		},
		{
			"upsertPartner", upsertPartnerCommand,
			operandRequest(http.MethodPut, "/v1/organizations", "not-a-uuid", "", "", []byte(`{}`)),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.decode(agentPolicy{Op: c.name}, restCommandDeps{records: seamRecord{}}, c.req, nil); !errors.Is(err, apperrors.ErrNotFound) {
				t.Errorf("decoding a malformed id answered %v, want the not-found sentinel", err)
			}
		})
	}
}

// A missing {lineItemId} answers 422 naming it, not a panic on an empty
// UUID downstream — the same shape removeProjectStakeholder's person_id
// gets in task 5's own table.
func TestAMissingLineItemIDAnswers422(t *testing.T) {
	offerID := ids.NewV7().String()
	cases := []struct {
		name   string
		decode func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error)
	}{
		{"updateOfferLineItem", updateOfferLineItemCommand},
		{"removeOfferLineItem", removeOfferLineItemCommand},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// extraParam left empty: the route matched (a valid offer {id}), but
			// the {lineItemId} segment the router would bind was never set.
			req := operandRequest(http.MethodPatch, "/v1/offers", offerID, "", "", []byte(`{}`))
			_, err := c.decode(agentPolicy{Op: c.name}, restCommandDeps{records: seamRecord{}}, req, []byte(`{}`))
			var detailed *httperr.DetailedError
			if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
				t.Fatalf("a missing lineItemId answered %v, want a 422 naming it", err)
			}
			if len(detailed.Fields) != 1 || detailed.Fields[0].Field != "lineItemId" || detailed.Fields[0].Code != "missing" {
				t.Errorf("the 422 named %+v, want field \"lineItemId\" code \"missing\"", detailed.Fields)
			}
		})
	}
}

// A malformed (non-empty) lineItemId is also a 422, code "invalid" rather
// than "missing" — the other half of the pathOperand + ids.Parse
// composition, the same shape TestARemoveStakeholderMalformedPersonIDAnswers422
// proves for person_id.
func TestAMalformedLineItemIDAnswers422(t *testing.T) {
	req := operandRequest(http.MethodDelete, "/v1/offers", ids.NewV7().String(), "lineItemId", "not-a-uuid", nil)
	_, err := removeOfferLineItemCommand(agentPolicy{Op: "removeOfferLineItem"}, restCommandDeps{records: seamRecord{}}, req, nil)
	var detailed *httperr.DetailedError
	if !errors.As(err, &detailed) || detailed.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a malformed lineItemId answered %v, want a 422", err)
	}
	if len(detailed.Fields) != 1 || detailed.Fields[0].Field != "lineItemId" || detailed.Fields[0].Code != "invalid" {
		t.Errorf("the 422 named %+v, want field \"lineItemId\" code \"invalid\"", detailed.Fields)
	}
}

// Each of the seven stages against the routed record it names — proven
// through stageRefusal end to end, the same shape
// TestEachOperandCommandStagesTheRoutedRecord proves for task 5's eight.
// createOffer is the one exception: it stages the record TYPE with no id
// (gradionhq/margince-poc-v1#1046), asserted separately below.
func TestEachNestedCommandStagesTheRoutedRecord(t *testing.T) {
	listID, tagID, offerID, orgID := ids.NewV7(), ids.NewV7(), ids.NewV7(), ids.NewV7()
	lineItemID := ids.NewV7()
	cases := []struct {
		name           string
		pol            agentPolicy
		req            *http.Request
		body           []byte
		wantTargetType string
		wantTargetID   ids.UUID
	}{
		{
			"addListMember",
			agentPolicy{Op: "addListMember", Access: accessTool, Tool: "update_record", RecordType: recordTypeList},
			operandRequest(http.MethodPost, "/v1/lists", listID.String(), "", "", []byte(`{}`)), []byte(`{}`),
			"list", listID,
		},
		{
			"applyTag",
			agentPolicy{Op: "applyTag", Access: accessTool, Tool: "update_record", RecordType: recordTypeTag},
			operandRequest(http.MethodPost, "/v1/tags", tagID.String(), "", "", []byte(`{}`)), []byte(`{}`),
			"tag", tagID,
		},
		{
			"addOfferLineItem",
			agentPolicy{Op: "addOfferLineItem", Access: accessTool, Tool: "update_record", RecordType: recordTypeOffer},
			operandRequest(http.MethodPost, "/v1/offers", offerID.String(), "", "", []byte(`{}`)), []byte(`{}`),
			"offer", offerID,
		},
		{
			"updateOfferLineItem",
			agentPolicy{Op: "updateOfferLineItem", Access: accessTool, Tool: "update_record", RecordType: recordTypeOffer},
			operandRequest(http.MethodPatch, "/v1/offers", offerID.String(), "lineItemId", lineItemID.String(), []byte(`{}`)),
			[]byte(`{}`), "offer", offerID,
		},
		{
			"removeOfferLineItem",
			agentPolicy{Op: "removeOfferLineItem", Access: accessTool, Tool: "update_record", RecordType: recordTypeOffer},
			operandRequest(http.MethodDelete, "/v1/offers", offerID.String(), "lineItemId", lineItemID.String(), nil), nil,
			"offer", offerID,
		},
		{
			"upsertPartner",
			agentPolicy{Op: "upsertPartner", Access: accessTool, Tool: "update_record", RecordType: recordTypePartner},
			operandRequest(http.MethodPut, "/v1/organizations", orgID.String(), "", "", []byte(`{}`)), []byte(`{}`),
			"organization", orgID,
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

// gradionhq/margince-poc-v1#1046: createOffer stages the record TYPE with
// NO id, end to end through stageRefusal — the routed {id} on
// POST /v1/deals/{id}/offers is the DEAL, not an offer, so the only honest
// staged target is the one every other create stages.
func TestCreateOfferStagesNoIDThroughStageRefusal(t *testing.T) {
	dealID := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "createOffer", Access: accessTool, Tool: "create_record", RecordType: recordTypeOffer}
	body := []byte(`{"currency":"EUR"}`)
	req := operandRequest(http.MethodPost, "/v1/deals", dealID.String(), "", "", body)

	stageRefusal(httptest.NewRecorder(), req, staging, restCommandDeps{records: seamRecord{}}, pol, body)

	if staging.last.TargetType != "offer" {
		t.Fatalf("staged target_type = %q, want \"offer\"", staging.last.TargetType)
	}
	if !staging.last.TargetID.IsZero() {
		t.Errorf("staged target_id = %s, want zero — the routed id names the deal, not an offer", staging.last.TargetID)
	}
	if !strings.Contains(staging.last.Summary, dealID.String()) {
		t.Errorf("summary %q does not name the parent deal", staging.last.Summary)
	}
}

// The behaviour change registering these seven in restCommands buys over
// the route-walk fallback: Guards now runs, for the two families the
// record seam actually serves. createOffer refuses a DEAL the caller
// cannot see; upsertPartner refuses an ORGANIZATION the caller cannot see.
// The other five (list, tag, offer) have no such proof — the seam has
// never served those types, so there is no read for Guards to skip, the
// same bound task 5's custom_field commands stand on.
func TestANestedCommandOfAnUnseeableParentStagesNothing(t *testing.T) {
	cases := []struct {
		name string
		pol  agentPolicy
		req  *http.Request
		body []byte
	}{
		{
			"createOffer",
			agentPolicy{Op: "createOffer", Access: accessTool, Tool: "create_record", RecordType: recordTypeOffer},
			operandRequest(http.MethodPost, "/v1/deals", ids.NewV7().String(), "", "", []byte(`{}`)), []byte(`{}`),
		},
		{
			"upsertPartner",
			agentPolicy{Op: "upsertPartner", Access: accessTool, Tool: "update_record", RecordType: recordTypePartner},
			operandRequest(http.MethodPut, "/v1/organizations", ids.NewV7().String(), "", "", []byte(`{}`)), []byte(`{}`),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			staging := &capturingApprovals{}
			rec := httptest.NewRecorder()

			stageRefusal(rec, c.req, staging, restCommandDeps{records: hiddenRecord{}}, c.pol, c.body)

			if rec.Code != http.StatusNotFound {
				t.Errorf("staging against an unseeable parent answered %d, want 404 — the refusal must not "+
					"tell a caller that a row they may not see exists", rec.Code)
			}
			if staging.last.Tool != "" {
				t.Errorf("an approval was staged for %q against a parent nobody can decide about", staging.last.Tool)
			}
		})
	}
}

// canonicalCollectionRoute reports the two route shapes
// TestEveryAgentReachableCreateOperationDecodesIntoACommand and
// TestEveryAgentReachablePatchOperationDecodesIntoACommand already cover in
// full: a bare collection path with no path parameter at all (a top-level
// create), or a path ending in exactly one {id} (a whole-record patch,
// PATCH or PUT alike — updateOfferTemplate's PUT is canonical by this
// shape even though that other test's own filter is PATCH-only). Anything
// else nests under a parent or reaches a child/membership action, which is
// this file's seven.
func canonicalCollectionRoute(route string) bool {
	if !strings.Contains(route, "{") {
		return true
	}
	return strings.HasSuffix(route, "/{id}") && strings.Count(route, "{") == 1
}

// TestEveryAutoExecuteNestedOrActionRouteDecodesIntoACommand is task 6's
// sibling of TestEveryConfirmFirstOperandRouteDecodesIntoTheRightCommand
// (agentcommandoperand_test.go): the same derived-coverage shape, for the
// AUTO-EXECUTE side of create_record/update_record — every such operation
// whose route is not the canonical collection shape must decode into a
// command, or its staged target is still guessed from the route the moment
// a tier floor (#982) promotes it to confirm-first.
//
// Derived from agentPolicies rather than the seven names in the brief's own
// table, so a route the contract adds to this shape fails here rather than
// silently falling back to the guess.
func TestEveryAutoExecuteNestedOrActionRouteDecodesIntoACommand(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier != tierAutoExecute {
			continue
		}
		if pol.Tool != "create_record" && pol.Tool != "update_record" {
			continue
		}
		if canonicalCollectionRoute(route) {
			continue
		}
		checked++
		if _, described := restCommands[pol.Op]; !described {
			t.Errorf("%s (%s) is an auto-execute create/update operation whose route is not the canonical "+
				"collection shape, but decodes into no command — its staged target is still guessed from "+
				"the route the moment a tier floor promotes it", route, pol.Op)
		}
	}
	if checked != 7 {
		t.Errorf("the policy table carries %d auto-execute nested/action create-or-update operations, want "+
			"7 — if the contract gained or lost one, this seam's coverage moved with it", checked)
	}
}

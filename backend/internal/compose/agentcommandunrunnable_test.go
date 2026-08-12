// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// No confirm-first operation stages a call its own executor would refuse.
//
// A staged approval spends a human's attention and then their one-shot
// authority: the redemption is consumed BEFORE the handler runs, so a call the
// handler was always going to reject on its arguments costs the approval on the
// way to the refusal, and the agent has to ask again. #982 closed this for the
// generic verbs one at a time; this is the same obligation, derived over every
// confirm-first operation the contract declares, so a family that grows one
// answers here rather than being remembered.
//
// Each fixture is a call that is wrong on ARGUMENT grounds alone — nothing
// about who is asking, nothing about workspace state. The refusal must arrive
// as a 4xx with nothing staged.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// unrunnableCall is one call an operation's executor would refuse, and the
// reason it would — stated so a reader can tell a fixture that exercises the
// refusal from one that merely happens to fail.
type unrunnableCall struct {
	refusedFor string
	build      func() (*http.Request, []byte)
}

// malformedRoutedID is the refusal every routed operation shares: the id in the
// path is not one. It is the weakest of the fixtures here and the most widely
// applicable — an operation whose only argument is the record it names has no
// other way to be wrong — and it is a real executor refusal, since the handler
// answers the same not-found for it.
func malformedRoutedID(method, collection string) unrunnableCall {
	return unrunnableCall{
		refusedFor: "the routed id is not a uuid, so it names no record the handler could act on",
		build: func() (*http.Request, []byte) {
			return routedFixture(method, "/v1"+collection+"/not-a-uuid", "not-a-uuid", "")
		},
	}
}

// routedFixture builds the request with the router's {id} bound, and nothing
// else bound: every fixture here that needs a SECOND path parameter is a
// fixture about that parameter being absent.
func routedFixture(method, path, routedID, body string) (*http.Request, []byte) {
	payload := []byte(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", routedID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx)), payload
}

// bodyFixture builds a request for a route that carries no {id}: the body is
// the whole of what can be wrong.
func bodyFixture(path, body string) unrunnableCall {
	return unrunnableCall{
		refusedFor: "the body names a member the record type does not accept",
		build: func() (*http.Request, []byte) {
			payload := []byte(body)
			return httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload)), payload
		},
	}
}

// unrunnableCalls is one such call per confirm-first operation.
//
// Where a family has a SEMANTIC argument refusal — a phase outside the ladder,
// a booking with no duration, a send addressed to nobody, a patch naming a
// field the record type has no member for — that is the fixture, because it is
// the refusal #982 was about: an argument the handler validates and the staging
// used not to. Where an operation's only argument is the record it names, the
// id shape is the whole of what can be wrong and the fixture says so.
var unrunnableCalls = map[string]unrunnableCall{
	"archiveActivity":      malformedRoutedID(http.MethodDelete, "/activities"),
	"archiveDeal":          malformedRoutedID(http.MethodDelete, "/deals"),
	"archiveList":          malformedRoutedID(http.MethodDelete, "/lists"),
	"archiveOffer":         malformedRoutedID(http.MethodDelete, "/offers"),
	"archiveOfferTemplate": malformedRoutedID(http.MethodDelete, "/offer-templates"),
	"archiveOrganization":  malformedRoutedID(http.MethodDelete, "/organizations"),
	"archivePerson":        malformedRoutedID(http.MethodDelete, "/people"),
	"archiveProduct":       malformedRoutedID(http.MethodDelete, "/products"),
	"archiveProject":       malformedRoutedID(http.MethodDelete, "/projects"),
	"archiveRelationship":  malformedRoutedID(http.MethodDelete, "/relationships"),
	"archiveSavedView":     malformedRoutedID(http.MethodDelete, "/views"),
	"archiveTag":           malformedRoutedID(http.MethodDelete, "/tags"),

	"disqualifyLead":            malformedRoutedID(http.MethodDelete, "/leads"),
	"retireCustomField":         malformedRoutedID(http.MethodPost, "/custom-fields"),
	"updateCustomFieldOptions":  malformedRoutedID(http.MethodPatch, "/custom-fields"),
	"updateWebhookSubscription": malformedRoutedID(http.MethodPatch, "/webhook-subscriptions"),
	"scrapeCompany":             malformedRoutedID(http.MethodPost, "/organizations"),
	"deepReadCompany":           malformedRoutedID(http.MethodPost, "/organizations"),
	"mergePerson":               malformedRoutedID(http.MethodPost, "/people"),
	"mergeOrganization":         malformedRoutedID(http.MethodPost, "/organizations"),

	"updateProject": {
		refusedFor: "the patch names a member a project has no field for",
		build: func() (*http.Request, []byte) {
			return routedFixture(http.MethodPatch, "/v1/projects/"+ids.NewV7().String(),
				ids.NewV7().String(), `{"nickname":"typo"}`)
		},
	},
	"createProject": bodyFixture("/v1/projects", `{"nickname":"typo"}`),

	"advanceProjectPhase": {
		refusedFor: "the phase named is outside the contract's ladder",
		build: func() (*http.Request, []byte) {
			id := ids.NewV7().String()
			return routedFixture(http.MethodPost, "/v1/projects/"+id+"/advance", id, `{"to_phase":"vibing"}`)
		},
	},
	"promoteLead": {
		refusedFor: "the trigger named is outside the contract's enum, so no engagement justifies the promotion",
		build: func() (*http.Request, []byte) {
			id := ids.NewV7().String()
			return routedFixture(http.MethodPost, "/v1/leads/"+id+"/promote", id, `{"trigger":"a hunch"}`)
		},
	},
	"bookMeeting": {
		refusedFor: "the meeting ends before it starts, which the store refuses after the approval is spent",
		build: func() (*http.Request, []byte) {
			body := []byte(`{"start":"2026-08-10T10:00:00Z","end":"2026-08-10T09:00:00Z",` +
				`"links":[{"entity_type":"deal","entity_id":"019ff000-0000-7000-8000-000000000021"}]}`)
			return httptest.NewRequest(http.MethodPost, "/v1/bookings", bytes.NewReader(body)), body
		},
	},
	"sendEmail": {
		refusedFor: "the send reaches nobody",
		build: func() (*http.Request, []byte) {
			id := ids.NewV7().String()
			return routedFixture(http.MethodPost, "/v1/activities/"+id+"/send-email", id,
				`{"to":[],"subject":"Q3","body":"hi","consent_purpose":"sales"}`)
		},
	},
	"sendAccountEmail": {
		refusedFor: "the send reaches nobody",
		build: func() (*http.Request, []byte) {
			body := []byte(`{"to":[],"subject":"Q3","body":"hi","consent_purpose":"sales",` +
				`"links":[{"entity_type":"organization","entity_id":"019ff000-0000-7000-8000-000000000022"}]}`)
			return httptest.NewRequest(http.MethodPost, "/v1/emails", bytes.NewReader(body)), body
		},
	},
	"sendMessage": {
		refusedFor: "the anchor is not a channel conversation, so no reply can be transmitted through it",
		build: func() (*http.Request, []byte) {
			id := ids.NewV7().String()
			return routedFixture(http.MethodPost, "/v1/activities/"+id+"/send-message", id,
				`{"body":"hello","consent_purpose":"support"}`)
		},
	},

	// The operand family: the second path segment the router would have bound
	// is absent, which is the shape a routing defect produces and the one thing
	// these operations cannot run without.
	"confirmOrganizationFact":         missingOperand(http.MethodPost, "/v1/organizations/%s/facts//confirm"),
	"updateOrganizationFact":          missingOperand(http.MethodPatch, "/v1/organizations/%s/facts/"),
	"confirmOrganizationProfileField": missingOperand(http.MethodPost, "/v1/organizations/%s/profile-fields//confirm"),
	"updateOrganizationProfileField":  missingOperand(http.MethodPatch, "/v1/organizations/%s/profile-fields/"),
	"removeProjectStakeholder":        missingOperand(http.MethodDelete, "/v1/projects/%s/stakeholders/"),

	"setProjectStakeholder": {
		refusedFor: "the person_id in the body is not a uuid, so the edge names no person",
		build: func() (*http.Request, []byte) {
			id := ids.NewV7().String()
			return routedFixture(http.MethodPut, "/v1/projects/"+id+"/stakeholders", id,
				`{"person_id":"not-a-uuid","role":"champion"}`)
		},
	},
}

// missingOperand builds a request whose routed {id} is well formed and whose
// SECOND path parameter was never bound.
func missingOperand(method, path string) unrunnableCall {
	return unrunnableCall{
		refusedFor: "the operand the route carries is absent, and the operation has nothing to act on without it",
		build: func() (*http.Request, []byte) {
			id := ids.NewV7().String()
			return routedFixture(method, strings.Replace(path, "%s", id, 1), id, "")
		},
	}
}

// noUnrunnableCall names the confirm-first operations with no cheap call an
// executor would refuse on arguments alone, and why — stated here rather than
// skipped silently, so the shape of what is NOT covered is readable.
//
// Both are creates of a record type whose body the governance seam holds to no
// shape: agents.createRecordShapes carries the types create_record itself
// writes, and a create outside it is performed by its own module's handler,
// which is where its body is validated. The seam has no shape to refuse against
// (gradionhq/margince-poc-v1#1021 is where those types get one), so any body
// this test could send would stage — which is a gap to record, not a fixture to
// fake.
var noUnrunnableCall = gatekit.Waive(map[string]string{
	"createCustomField": "the governance seam holds a custom_field body to no declared shape, so every " +
		"body this gate could send would stage and the refusal it looks for happens in the module's own handler",
	"createWebhookSubscription": "the governance seam holds a webhook_subscription body to no declared " +
		"shape, so every body this gate could send would stage and the refusal it looks for happens in the " +
		"module's own handler",
})

func TestNoConfirmFirstOperationStagesACallItsExecutorWouldRefuse(t *testing.T) {
	defer noUnrunnableCall.AssertAllMatched(t)
	subject := map[string]bool{}
	for op, route := range agentReachableMutations() {
		if agentPolicies[route].Tier != tierConfirmationRequired {
			continue
		}
		subject[op] = true
	}
	if len(subject) == 0 {
		t.Fatal("the policy table declares no confirm-first agent-reachable mutation — this gate checked nothing")
	}
	for op := range subject {
		fixture, written := unrunnableCalls[op]
		if !written {
			if !noUnrunnableCall.Waived(t, op) {
				t.Errorf("%s is confirm-first and has neither a call its executor would refuse nor a stated "+
					"reason there is none — a human's one-shot approval can be spent reaching a refusal the "+
					"staging could have made first", op)
			}
			continue
		}
		t.Run(op, func(t *testing.T) {
			assertRefusedBeforeStaging(t, op, fixture)
		})
	}
	for op := range unrunnableCalls {
		if !subject[op] {
			t.Errorf("unrunnableCalls[%q] describes an operation that is not confirm-first, so nothing it "+
				"asserts is about a staged approval", op)
		}
	}
	for _, op := range noUnrunnableCall.Subjects() {
		if _, both := unrunnableCalls[op]; both {
			t.Errorf("%s is both excused and covered by a fixture; the excuse describes a gap that is closed", op)
		}
	}
}

func assertRefusedBeforeStaging(t *testing.T, op string, fixture unrunnableCall) {
	t.Helper()
	req, body := fixture.build()
	staging := &capturingApprovals{}
	rec := httptest.NewRecorder()
	pol := agentPolicies[agentReachableMutations()[op]]

	stageRefusal(rec, req, staging, restCommandDeps{records: seamRecord{}, channels: channelKinds{}}, pol, body)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for a call refused because %s — the human's yes is spent reaching a "+
			"refusal this door could have made first", fixture.refusedFor)
	}
	if rec.Code < http.StatusBadRequest || rec.Code >= http.StatusInternalServerError {
		t.Errorf("a call refused because %s answered %d; the caller is owed a 4xx naming what to fix, not a "+
			"server fault", fixture.refusedFor, rec.Code)
	}
}

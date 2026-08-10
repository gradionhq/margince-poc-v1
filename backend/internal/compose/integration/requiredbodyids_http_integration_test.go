// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Making a refusal clearer must not turn it into a probe for whether a record
// exists.
//
// Every body in this suite now names an omitted required id instead of answering
// a bare not-found. That fix has one way to go wrong, and it is the dangerous
// one: if the SUPPLIED-but-invisible case starts answering 422 as well, the
// surface has become an existence oracle — a caller could enumerate ids and read
// the status code to learn which rows are there.
//
// So each body is driven twice over real HTTP, and the pair is the assertion:
//
//	omitted                     → 422 naming the wire field
//	supplied, names nothing      → 404, saying nothing
//
// The unit probes in each module prove the guard is called; only this lane can
// prove the second half, because existence-hiding is rendered by a row-scoped
// query against real rows.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// problemBody is the RFC 7807 shape both cases answer with. Fields is decoded
// because "422" alone is not the claim — the caller has to be told WHICH field.
type problemBody struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	} `json:"details"`
}

// namesField reports whether a 422 body's per-field breakdown names field.
func namesField(problem problemBody, field string) bool {
	for _, refusal := range problem.Details.Errors {
		if refusal.Field == field {
			return true
		}
	}
	return false
}

// requiredIDFixtures are the records the twelve rows below need in order to
// reach their guard at all: a container in the path, or a subject the body can
// legitimately name. A missing PATH id would 404 for its own reasons and prove
// nothing about the body.
type requiredIDFixtures struct {
	person, organization, activity string
	list, tag, project             string
	deal, subjectUser              string
}

func seedRequiredIDFixtures(t *testing.T, e *apptest.AppEnv) requiredIDFixtures {
	t.Helper()
	var out requiredIDFixtures
	out.person = createAndID(t, e, "/v1/people", apptest.AnyMap{"full_name": "Merge Source"})
	out.organization = createAndID(t, e, "/v1/organizations", apptest.AnyMap{"display_name": "Merge Org"})
	out.activity = createAndID(t, e, "/v1/activities", apptest.AnyMap{
		"kind": "note", "body": "relink probe",
		"links": []apptest.AnyMap{{"entity_type": "person", "entity_id": out.person}},
	})
	out.list = createAndID(t, e, "/v1/lists", apptest.AnyMap{
		"name": "Required IDs", "entity_type": "person", "list_type": "static",
	})
	out.tag = createAndID(t, e, "/v1/tags", apptest.AnyMap{"name": "required-ids"})
	out.project = createAndID(t, e, "/v1/projects", apptest.AnyMap{
		"name": "Stakeholder probe", "organization_id": out.organization, "source": "manual",
	})
	out.deal = seedDealForRequiredIDs(t, e)

	// A REAL user for the grant's subject, so the subject_id row isolates
	// subject_id and the record_id row isolates record_id.
	var users struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/users", nil, nil, &users); status != http.StatusOK || len(users.Data) == 0 {
		t.Fatalf("list users → %d (%d users)", status, len(users.Data))
	}
	out.subjectUser = users.Data[0].ID
	return out
}

// createAndID posts one fixture and returns its id, failing loudly if the create
// itself was refused — a fixture that silently did not exist would turn every row
// below into a 404 about the path.
func createAndID(t *testing.T, e *apptest.AppEnv, path string, body apptest.AnyMap) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", path, body, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST %s → %d, want 201", path, status)
	}
	if created.ID == "" {
		t.Fatalf("POST %s returned no id", path)
	}
	return created.ID
}

// requiredIDCase is one body, driven twice. omitted and supplied differ ONLY in
// the id under test, so the status difference cannot come from anything else.
type requiredIDCase struct {
	method, path      string
	omitted, supplied apptest.AnyMap
	field             string
}

// requiredIDCases is every body this suite drives, keyed by the field under
// test. Data, kept out of the test so the assertions stay readable — and one
// entry per required id rather than per body, because CreateRecordGrantRequest
// carries two and a guard that named only the first would leave the second
// answering not-found for a subject nobody sent.
func requiredIDCases(f requiredIDFixtures, absent string) map[string]requiredIDCase {
	return map[string]requiredIDCase{
		// The MCP twin is guarded at Registry.Invoke, so this route is the half
		// that has to keep up: one rule, one answer, whichever surface asks.
		"AdvanceDealRequest.to_stage_id": {
			method: "POST", path: "/v1/deals/" + f.deal + "/advance",
			omitted: apptest.AnyMap{}, supplied: apptest.AnyMap{"to_stage_id": absent}, field: "to_stage_id",
		},
		"CreateStageRequest.pipeline_id": {
			method: "POST", path: "/v1/stages",
			omitted:  apptest.AnyMap{"name": "Orphan stage", "position": 9},
			supplied: apptest.AnyMap{"name": "Orphan stage", "position": 9, "pipeline_id": absent},
			field:    "pipeline_id",
		},
		"MergePersonJSONBody.target_id": {
			method: "POST", path: "/v1/people/" + f.person + "/merge",
			omitted: apptest.AnyMap{}, supplied: apptest.AnyMap{"target_id": absent}, field: "target_id",
		},
		"MergeOrganizationJSONBody.target_id": {
			method: "POST", path: "/v1/organizations/" + f.organization + "/merge",
			omitted: apptest.AnyMap{}, supplied: apptest.AnyMap{"target_id": absent}, field: "target_id",
		},
		"RelinkActivityJSONBody.entity_id": {
			method: "POST", path: "/v1/activities/" + f.activity + "/relink",
			omitted:  apptest.AnyMap{"entity_type": "person"},
			supplied: apptest.AnyMap{"entity_type": "person", "entity_id": absent},
			field:    "entity_id",
		},
		"RecordConsentRequest.purpose_id": {
			method: "POST", path: "/v1/people/" + f.person + "/consent",
			omitted:  apptest.AnyMap{"new_state": "granted"},
			supplied: apptest.AnyMap{"new_state": "granted", "purpose_id": absent},
			field:    "purpose_id",
		},
		"IssueDoubleOptInJSONBody.purpose_id": {
			method: "POST", path: "/v1/people/" + f.person + "/consent/double-opt-in",
			omitted: apptest.AnyMap{}, supplied: apptest.AnyMap{"purpose_id": absent}, field: "purpose_id",
		},
		"AddListMemberRequest.entity_id": {
			method: "POST", path: "/v1/lists/" + f.list + "/members",
			omitted:  apptest.AnyMap{"entity_type": "person"},
			supplied: apptest.AnyMap{"entity_type": "person", "entity_id": absent},
			field:    "entity_id",
		},
		"ApplyTagRequest.entity_id": {
			method: "POST", path: "/v1/tags/" + f.tag + "/apply",
			omitted:  apptest.AnyMap{"entity_type": "person"},
			supplied: apptest.AnyMap{"entity_type": "person", "entity_id": absent},
			field:    "entity_id",
		},
		"SetProjectStakeholderRequest.person_id": {
			method: "PUT", path: "/v1/projects/" + f.project + "/stakeholders",
			omitted:  apptest.AnyMap{"role": "sponsor"},
			supplied: apptest.AnyMap{"role": "sponsor", "person_id": absent},
			field:    "person_id",
		},
		// Two required ids, so two rows: a guard that named only the first would
		// leave the second answering not-found for a subject nobody sent.
		"CreateRecordGrantRequest.record_id": {
			method: "POST", path: "/v1/record-grants",
			omitted: apptest.AnyMap{
				"access": "read", "record_type": "person", "subject_type": "user", "subject_id": f.subjectUser,
			},
			supplied: apptest.AnyMap{
				"access": "read", "record_type": "person", "subject_type": "user",
				"subject_id": f.subjectUser, "record_id": absent,
			},
			field: "record_id",
		},
		"CreateRecordGrantRequest.subject_id": {
			method: "POST", path: "/v1/record-grants",
			omitted: apptest.AnyMap{
				"access": "read", "record_type": "person", "subject_type": "user", "record_id": f.person,
			},
			supplied: apptest.AnyMap{
				"access": "read", "record_type": "person", "subject_type": "user",
				"record_id": f.person, "subject_id": absent,
			},
			field: "subject_id",
		},
	}
}

func TestAnOmittedRequiredIDIsNamedAndASuppliedOneStaysHidden(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "required-ids"
	apptest.BootstrapWorkspaceSession(t, e, "Required IDs", "ids@fable.test", "Admin")

	for name, tc := range requiredIDCases(seedRequiredIDFixtures(t, e), ids.NewV7().String()) {
		t.Run(name+"/omitted names the field", func(t *testing.T) {
			var problem problemBody
			status := e.Call(t, tc.method, tc.path, tc.omitted, nil, &problem)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("→ %d, want 422 (a bare 404 is the defect this closes): %+v", status, problem)
			}
			if !namesField(problem, tc.field) {
				t.Errorf("the 422 names %+v, want the wire field %q — a status alone leaves the caller "+
					"guessing which key they forgot", problem.Details.Errors, tc.field)
			}
		})
		t.Run(name+"/supplied but invisible stays a 404", func(t *testing.T) {
			var problem problemBody
			status := e.Call(t, tc.method, tc.path, tc.supplied, nil, &problem)
			if status != http.StatusNotFound {
				t.Fatalf("→ %d, want 404: a well-formed id that names nothing must not be distinguishable "+
					"from one the caller may not see, or the status code enumerates rows: %+v", status, problem)
			}
			// And it must not name the field either: a 404 that said "purpose_id"
			// would confirm the caller's id was structurally accepted and only
			// the row was missing.
			if namesField(problem, tc.field) {
				t.Errorf("the 404 names %q, which tells the caller their id was accepted and the row is "+
					"simply absent", tc.field)
			}
		})
	}
}

// seedDealForRequiredIDs opens one deal in the workspace's seeded default
// pipeline and returns its id — the advance route needs a real deal in the path,
// or its 404 would be about the deal rather than about the stage.
func seedDealForRequiredIDs(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var pipelines struct {
		Data []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID       string `json:"id"`
				Semantic string `json:"semantic"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK || len(pipelines.Data) == 0 {
		t.Fatalf("list pipelines → %d (%d pipelines)", status, len(pipelines.Data))
	}
	pipeline := pipelines.Data[0]
	open := ""
	for _, stage := range pipeline.Stages {
		if stage.Semantic == "open" {
			open = stage.ID
			break
		}
	}
	if open == "" {
		t.Fatalf("the seeded pipeline has no open stage: %+v", pipeline.Stages)
	}
	var deal struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/deals", apptest.AnyMap{
		"name": "Advance probe", "pipeline_id": pipeline.ID, "stage_id": open,
		"currency": "EUR", "amount_minor": 1000, "source": "ui",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("create deal → %d", status)
	}
	return deal.ID
}

// The same rule, reached through the AGENT gate instead of the handler.
//
// A passport's REST call to a dynamic-tier tool resolves its tier BEFORE the
// handler runs (compose/agentgate.go), so `advance_deal` has a second entry point
// for the very field U3 unified — and the session-driven pair above cannot see
// it. One rule must read the same on both, or the asymmetry has simply moved from
// one transport to one credential type.
func TestAPassportReadsTheSameRefusalAsASessionForAnOmittedStage(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "required-ids-agent"
	apptest.BootstrapWorkspaceSession(t, e, "Required IDs Agent", "agent-ids@fable.test", "Admin")
	deal := seedDealForRequiredIDs(t, e)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "stage agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	var asSession, asPassport problemBody
	sessionStatus := e.Call(t, "POST", "/v1/deals/"+deal+"/advance", apptest.AnyMap{}, nil, &asSession)
	passportStatus := e.Call(t, "POST", "/v1/deals/"+deal+"/advance", apptest.AnyMap{}, bearer, &asPassport)

	if sessionStatus != http.StatusUnprocessableEntity || passportStatus != http.StatusUnprocessableEntity {
		t.Fatalf("session → %d, passport → %d; want 422 from both", sessionStatus, passportStatus)
	}
	if asSession.Detail != asPassport.Detail {
		t.Errorf("one rule, two sentences:\n  session:  %q\n  passport: %q", asSession.Detail, asPassport.Detail)
	}
	for _, answer := range []problemBody{asSession, asPassport} {
		if !namesField(answer, "to_stage_id") {
			t.Errorf("the refusal names %+v, want the wire field to_stage_id", answer.Details.Errors)
		}
	}

	// And an unreadable body is a DIFFERENT fault: answering "to_stage_id is
	// required" for JSON that never parsed points the caller at a field that may
	// well be there.
	// Three faults, three answers — and the middle one is the trap. A body that is
	// perfectly readable JSON carrying a to_stage_id the UUID decoder refuses fails
	// the SAME json.Unmarshal as a body that is not JSON at all, so one branch for
	// both tells a caller to hunt a syntax error that is not there while the value
	// they can actually see and fix goes unnamed.
	for name, tc := range map[string]struct {
		body      string
		wantField string
		// distinctFrom is the omitted-key sentence: every one of these must differ
		// from it, or the fault they name is one the caller cannot tell apart.
		distinctFrom string
	}{
		"not JSON at all":        {"{not json", "body", asPassport.Detail},
		"empty body":             {"", "body", asPassport.Detail},
		"valid JSON, not a UUID": {`{"to_stage_id":"banana"}`, "to_stage_id", asPassport.Detail},
		"valid JSON, a number":   {`{"to_stage_id":12345}`, "to_stage_id", asPassport.Detail},
		"valid JSON, an array":   {`[]`, "to_stage_id", asPassport.Detail},
	} {
		t.Run(name, func(t *testing.T) {
			answer := postRawBody(t, e, "/v1/deals/"+deal+"/advance", tc.body, bearer)
			if answer.status != http.StatusUnprocessableEntity {
				t.Fatalf("→ %d, want 422: %+v", answer.status, answer.problem)
			}
			if !namesField(answer.problem, tc.wantField) {
				t.Errorf("names %+v, want the field %q", answer.problem.Details.Errors, tc.wantField)
			}
			if answer.problem.Detail == tc.distinctFrom {
				t.Errorf("answers the omitted-key sentence %q for a different fault — the caller cannot "+
					"tell which mistake they made", answer.problem.Detail)
			}
			// The specific lie this closes: claiming the body was unreadable when it
			// parsed fine.
			if tc.wantField == "to_stage_id" && strings.Contains(answer.problem.Detail, "not readable JSON") {
				t.Errorf("body %q is valid JSON and the refusal says %q — the caller is sent hunting a "+
					"syntax error that does not exist", tc.body, answer.problem.Detail)
			}
		})
	}
}

// rawAnswer is a response to a body the harness cannot marshal for us, because
// the point is that it is not valid JSON.
type rawAnswer struct {
	status  int
	problem problemBody
}

// postRawBody sends bytes verbatim. env.call marshals its body, so it can never
// produce the malformed-JSON case the gate has to tell apart from an omitted key.
func postRawBody(t *testing.T, e *apptest.AppEnv, path, body string, headers map[string]string) rawAnswer {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.TS.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.Client.Do(req) //nolint:bodyclose // closed by apptest.CloseBody below; bodyclose only recognises a Close in the same package
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer apptest.CloseBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	out := rawAnswer{status: resp.StatusCode}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.problem); err != nil {
			t.Fatalf("POST %s: decoding %q: %v", path, raw, err)
		}
	}
	return out
}

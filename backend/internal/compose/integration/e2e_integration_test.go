// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// End-to-end lane: the real handler stack (session auth, RLS transaction
// helper, stores, RFC 7807 mapper) over the real migrated Postgres —
// bootstrap → login-by-cookie → CRUD → optimistic concurrency → archive.
// TLS test server because the session cookie is Secure per ADR-0043. The
// agent-governance slice of this lane lives in
// e2e_agent_integration_test.go.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// seededStages is the seeded default pipeline's stage vocabulary a
// scenario advances deals through.
type seededStages struct {
	pipelineID string
	open       string
	won        string
	lost       string
}

// discoverSeededPipeline asserts the bootstrap seeded exactly one default
// pipeline with its six stages and resolves the semantic stage ids.
func discoverSeededPipeline(t *testing.T, e *apptest.AppEnv) seededStages {
	t.Helper()
	var pipelines struct {
		Data []struct {
			Id        string `json:"id"`
			IsDefault bool   `json:"is_default"`
			Stages    []struct {
				Id       string `json:"id"`
				Semantic string `json:"semantic"`
			} `json:"stages"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
		t.Fatalf("pipelines status = %d", status)
	}
	if len(pipelines.Data) != 1 || !pipelines.Data[0].IsDefault || len(pipelines.Data[0].Stages) != 6 {
		t.Fatalf("seeded pipeline shape wrong: %+v", pipelines.Data)
	}
	stages := seededStages{pipelineID: pipelines.Data[0].Id}
	for _, s := range pipelines.Data[0].Stages {
		switch s.Semantic {
		case "won":
			stages.won = s.Id
		case "lost":
			stages.lost = s.Id
		case "open":
			if stages.open == "" {
				stages.open = s.Id
			}
		}
	}
	return stages
}

// exercisePersonWriteInvariants runs the person write shape: create with
// server-stamped provenance, duplicate-email 409 with the existing id,
// If-Match version skew, then the versioned update. Returns the person id.
func exercisePersonWriteInvariants(t *testing.T, e *apptest.AppEnv, adminUserID string) string {
	t.Helper()
	var person apptest.AnyMap
	status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Grace Hopper",
		"source":    "ui",
		"emails":    []apptest.AnyMap{{"email": "grace@navy.mil", "is_primary": true}},
	}, nil, &person)
	if status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	personID := person["id"].(string)
	if person["captured_by"] != "human:"+adminUserID {
		t.Errorf("captured_by = %v; the server must stamp the acting principal", person["captured_by"])
	}

	var dup apptest.AnyMap
	status = e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Grace Clone",
		"source":    "ui",
		"emails":    []apptest.AnyMap{{"email": "grace@navy.mil"}},
	}, nil, &dup)
	if status != http.StatusConflict {
		t.Fatalf("duplicate email = %d, want 409", status)
	}
	if dup["details"].(apptest.AnyMap)["existing_id"] != personID {
		t.Errorf("409 existing_id = %v, want %s", dup["details"], personID)
	}

	var conflict apptest.AnyMap
	status = e.Call(t, "PATCH", "/v1/people/"+personID, apptest.AnyMap{"title": "Rear Admiral"},
		map[string]string{"If-Match": "42"}, &conflict)
	if status != http.StatusConflict || conflict["code"] != "version_skew" {
		t.Fatalf("stale If-Match = %d %v, want 409 version_skew", status, conflict)
	}

	var person2 apptest.AnyMap
	status = e.Call(t, "PATCH", "/v1/people/"+personID, apptest.AnyMap{"title": "Rear Admiral"},
		map[string]string{"If-Match": "1"}, &person2)
	if status != http.StatusOK || person2["version"].(float64) != 2 {
		t.Fatalf("If-Match update = %d version %v, want 200 v2", status, person2["version"])
	}
	return personID
}

// exerciseDealToWon creates the organization + deal, asserts losing
// without a reason is refused, and closes the deal as won. Returns the
// deal id.
func exerciseDealToWon(t *testing.T, e *apptest.AppEnv, stages seededStages) string {
	t.Helper()
	var org apptest.AnyMap
	status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{
		"display_name": "Acme GmbH",
		"source":       "ui",
		"domains":      []apptest.AnyMap{{"domain": "acme.example", "is_primary": true}},
	}, nil, &org)
	if status != http.StatusCreated {
		t.Fatalf("create org = %d %v", status, org)
	}

	var deal apptest.AnyMap
	status = e.Call(t, "POST", "/v1/deals", apptest.AnyMap{
		"name":            "Acme rollout",
		"amount_minor":    250_000_00,
		"currency":        "EUR",
		"pipeline_id":     stages.pipelineID,
		"stage_id":        stages.open,
		"organization_id": org["id"],
		"source":          "ui",
	}, nil, &deal)
	if status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
	dealID := deal["id"].(string)

	// Losing without a reason is refused (deal_lost_reason).
	var lostErr apptest.AnyMap
	status = e.Call(t, "POST", "/v1/deals/"+dealID+"/advance", apptest.AnyMap{"to_stage_id": stages.lost}, nil, &lostErr)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("lost without reason = %d %v, want 422", status, lostErr)
	}

	status = e.Call(t, "POST", "/v1/deals/"+dealID+"/advance", apptest.AnyMap{"to_stage_id": stages.won}, nil, &deal)
	if status != http.StatusOK || deal["status"] != "won" || deal["closed_at"] == nil {
		t.Fatalf("advance to won = %d %v", status, deal)
	}
	return dealID
}

// exerciseActivityIdempotentCapture logs an email activity against the
// deal and replays the identical capture, asserting the replay is a
// silent 200 onto the same activity.
func exerciseActivityIdempotentCapture(t *testing.T, e *apptest.AppEnv, dealID string) {
	t.Helper()
	// --- activity: log against the deal, idempotent capture replay ---
	var activity apptest.AnyMap
	logReq := apptest.AnyMap{
		"kind":          "email",
		"subject":       "Signed!",
		"source":        "email:msg-1",
		"source_system": "gmail",
		"source_id":     "msg-1",
		"links":         []apptest.AnyMap{{"entity_type": "deal", "entity_id": dealID}},
	}
	if status := e.Call(t, "POST", "/v1/activities", logReq, nil, &activity); status != http.StatusCreated {
		t.Fatalf("log activity = %d %v", status, activity)
	}
	var replay apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/activities", logReq, nil, &replay); status != http.StatusOK {
		t.Fatalf("capture replay = %d, want 200 (idempotent)", status)
	}
	if replay["id"] != activity["id"] {
		t.Errorf("replay returned a different activity: %v vs %v", replay["id"], activity["id"])
	}
}

func TestEndToEnd_coreSalesFlow(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	// The cookie authenticates /me.
	var me apptest.AnyMap
	if status := e.Call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me status = %d", status)
	}
	if got := me["user"].(apptest.AnyMap)["email"]; got != "ada@example.com" {
		t.Fatalf("/me email = %v", got)
	}

	stages := discoverSeededPipeline(t, e)
	personID := exercisePersonWriteInvariants(t, e, me["user"].(apptest.AnyMap)["id"].(string))
	dealID := exerciseDealToWon(t, e, stages)

	exerciseActivityIdempotentCapture(t, e, dealID)

	// --- lead: segregated, dedupes on email ---
	var lead apptest.AnyMap
	status := e.Call(t, "POST", "/v1/leads", apptest.AnyMap{
		"full_name":    "Cold Prospect",
		"email":        "cold@example.org",
		"company_name": "Unknown AG",
		"source":       "import:batch-1",
	}, nil, &lead)
	if status != http.StatusCreated {
		t.Fatalf("create lead = %d %v", status, lead)
	}
	status = e.Call(t, "POST", "/v1/leads", apptest.AnyMap{
		"email":  "cold@example.org",
		"source": "import:batch-2",
	}, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("duplicate lead = %d, want 409", status)
	}

	// --- archive cascades and stays fetchable by id ---
	var person apptest.AnyMap
	if status := e.Call(t, "DELETE", "/v1/people/"+personID, nil, nil, &person); status != http.StatusOK {
		t.Fatalf("archive person = %d", status)
	}
	if person["archived_at"] == nil {
		t.Error("archived person carries no archived_at")
	}
	var people struct {
		Data []apptest.AnyMap `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/people", nil, nil, &people); status != http.StatusOK {
		t.Fatalf("list people = %d", status)
	}
	for _, p := range people.Data {
		if p["id"] == personID {
			t.Error("archived person still appears in the default list")
		}
	}

	// --- the governance audit view reflects the session's own trail ---
	var audit struct {
		Data []apptest.AnyMap `json:"data"`
		Page apptest.AnyMap   `json:"page"`
	}
	if status := e.Call(t, "GET", "/v1/audit-log?entity_type=person&action=archive", nil, nil, &audit); status != http.StatusOK {
		t.Fatalf("audit log = %d", status)
	}
	found := false
	for _, entry := range audit.Data {
		if entry["entity_id"] == personID && entry["actor_type"] == "human" {
			found = true
		}
	}
	if !found {
		t.Errorf("the person archive is missing from the filtered audit view: %v", audit.Data)
	}
}

func TestEndToEnd_authAndSurfaceBoundaries(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	// An unimplemented contract operation answers an explicit 501.
	var problem apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/coldstart", apptest.AnyMap{"url": "https://example.com"}, nil, &problem); status != http.StatusNotImplemented {
		t.Fatalf("unimplemented op = %d %v, want 501", status, problem)
	}

	// Logout revokes; the session no longer authenticates.
	if status := e.Call(t, "POST", "/v1/auth/logout", nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout = %d", status)
	}
	if status := e.Call(t, "GET", "/v1/me", nil, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("/me after logout = %d, want 401", status)
	}

	// Login re-authenticates with fresh credentials.
	var me apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/auth/login", apptest.AnyMap{
		"email":    "ada@example.com",
		"password": "correct-horse-battery",
	}, nil, &me); status != http.StatusOK {
		t.Fatalf("login = %d", status)
	}
	if status := e.Call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me after login = %d", status)
	}

	// Wrong password is a 401 that does not say which half was wrong.
	var authErr apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/auth/login", apptest.AnyMap{
		"email":    "ada@example.com",
		"password": "wrong",
	}, nil, &authErr); status != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", status)
	}
}

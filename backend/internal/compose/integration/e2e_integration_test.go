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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/migrations"
)

type env struct {
	ts     *httptest.Server
	client *http.Client
	slug   string
	owner  *pgx.Conn
}

// setup boots the default harness server — no schema pool, so the
// customfields runtime-DDL operations answer their generated 501
// (the unwired-by-default posture). Suites that need the
// schema pool wired (customfields_http_integration_test.go) call
// setupWithOptions directly with compose.WithSchemaPool(SchemaPool(t)).
func setup(t *testing.T) *env {
	t.Helper()
	return setupWithOptions(t)
}

// setupWithOptions is setup's body, parameterized over extra compose
// options so a suite that needs a boot-optional seam (e.g. the
// customfields schema pool) can wire it without duplicating the
// migrate-and-boot ceremony every other suite in this package shares.
func setupWithOptions(t *testing.T, opts ...compose.Option) *env {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})

	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatalf("resetting schema: %v", err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatalf("loading custom migrations: %v", err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	allOpts := append([]compose.Option{compose.WithPublicBaseURL("https://mail.example.test")}, opts...)
	ts := httptest.NewTLSServer(compose.New(pool, slog.New(slog.NewTextHandler(os.Stderr, nil)), allOpts...))
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := ts.Client()
	client.Jar = jar

	return &env{ts: ts, client: client, slug: "fable-e2e", owner: owner}
}

// bootstrapWorkspace provisions the tenant + admin and leaves the session
// cookie in the client jar — the first step of every e2e scenario.
func (e *env) bootstrapWorkspace(t *testing.T) {
	t.Helper()
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Fable E2E",
		"admin_email":        "ada@example.com",
		"admin_display_name": "Ada Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("bootstrap status = %d", status)
	}
	e.slug = "fable-e2e" // slugify("Fable E2E")
}

// setWorkspaceSeat flips a workspace's users to a seat type through the
// owner connection, inside a workspace-bound transaction so RLS (FORCE)
// admits the UPDATE. Used to drive the read-seat ceiling from a test.
func (e *env) setWorkspaceSeat(t *testing.T, slug, seat string) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	var wsID string
	if err := tx.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID); err != nil {
		t.Fatalf("set guc: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app_user SET seat_type = $2 WHERE workspace_id = $1`, wsID, seat); err != nil {
		t.Fatalf("seat update: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// call issues one API request with the dev workspace header and decodes
// the JSON response into out (when non-nil), returning the status code.
//
//craft:ignore naked-any the test transport seam: body/out are whichever request/response shapes the scenario exercises
func (e *env) call(t *testing.T, method, path string, body any, headers map[string]string, out any) int {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reqBody)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Slug", e.slug)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer closeBody(t, resp)

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decoding %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

type anyMap = map[string]any

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
func discoverSeededPipeline(t *testing.T, e *env) seededStages {
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
	if status := e.call(t, "GET", "/v1/pipelines", nil, nil, &pipelines); status != http.StatusOK {
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
func exercisePersonWriteInvariants(t *testing.T, e *env, adminUserID string) string {
	t.Helper()
	var person anyMap
	status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Grace Hopper",
		"source":    "ui",
		"emails":    []anyMap{{"email": "grace@navy.mil", "is_primary": true}},
	}, nil, &person)
	if status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	personID := person["id"].(string)
	if person["captured_by"] != "human:"+adminUserID {
		t.Errorf("captured_by = %v; the server must stamp the acting principal", person["captured_by"])
	}

	var dup anyMap
	status = e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Grace Clone",
		"source":    "ui",
		"emails":    []anyMap{{"email": "grace@navy.mil"}},
	}, nil, &dup)
	if status != http.StatusConflict {
		t.Fatalf("duplicate email = %d, want 409", status)
	}
	if dup["details"].(anyMap)["existing_id"] != personID {
		t.Errorf("409 existing_id = %v, want %s", dup["details"], personID)
	}

	var conflict anyMap
	status = e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"title": "Rear Admiral"},
		map[string]string{"If-Match": "42"}, &conflict)
	if status != http.StatusConflict || conflict["code"] != "version_skew" {
		t.Fatalf("stale If-Match = %d %v, want 409 version_skew", status, conflict)
	}

	var person2 anyMap
	status = e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"title": "Rear Admiral"},
		map[string]string{"If-Match": "1"}, &person2)
	if status != http.StatusOK || person2["version"].(float64) != 2 {
		t.Fatalf("If-Match update = %d version %v, want 200 v2", status, person2["version"])
	}
	return personID
}

// exerciseDealToWon creates the organization + deal, asserts losing
// without a reason is refused, and closes the deal as won. Returns the
// deal id.
func exerciseDealToWon(t *testing.T, e *env, stages seededStages) string {
	t.Helper()
	var org anyMap
	status := e.call(t, "POST", "/v1/organizations", anyMap{
		"display_name": "Acme GmbH",
		"source":       "ui",
		"domains":      []anyMap{{"domain": "acme.example", "is_primary": true}},
	}, nil, &org)
	if status != http.StatusCreated {
		t.Fatalf("create org = %d %v", status, org)
	}

	var deal anyMap
	status = e.call(t, "POST", "/v1/deals", anyMap{
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
	var lostErr anyMap
	status = e.call(t, "POST", "/v1/deals/"+dealID+"/advance", anyMap{"to_stage_id": stages.lost}, nil, &lostErr)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("lost without reason = %d %v, want 422", status, lostErr)
	}

	status = e.call(t, "POST", "/v1/deals/"+dealID+"/advance", anyMap{"to_stage_id": stages.won}, nil, &deal)
	if status != http.StatusOK || deal["status"] != "won" || deal["closed_at"] == nil {
		t.Fatalf("advance to won = %d %v", status, deal)
	}
	return dealID
}

// exerciseActivityIdempotentCapture logs an email activity against the
// deal and replays the identical capture, asserting the replay is a
// silent 200 onto the same activity.
func exerciseActivityIdempotentCapture(t *testing.T, e *env, dealID string) {
	t.Helper()
	// --- activity: log against the deal, idempotent capture replay ---
	var activity anyMap
	logReq := anyMap{
		"kind":          "email",
		"subject":       "Signed!",
		"source":        "email:msg-1",
		"source_system": "gmail",
		"source_id":     "msg-1",
		"links":         []anyMap{{"entity_type": "deal", "entity_id": dealID}},
	}
	if status := e.call(t, "POST", "/v1/activities", logReq, nil, &activity); status != http.StatusCreated {
		t.Fatalf("log activity = %d %v", status, activity)
	}
	var replay anyMap
	if status := e.call(t, "POST", "/v1/activities", logReq, nil, &replay); status != http.StatusOK {
		t.Fatalf("capture replay = %d, want 200 (idempotent)", status)
	}
	if replay["id"] != activity["id"] {
		t.Errorf("replay returned a different activity: %v vs %v", replay["id"], activity["id"])
	}
}

func TestEndToEnd_coreSalesFlow(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// The cookie authenticates /me.
	var me anyMap
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me status = %d", status)
	}
	if got := me["user"].(anyMap)["email"]; got != "ada@example.com" {
		t.Fatalf("/me email = %v", got)
	}

	stages := discoverSeededPipeline(t, e)
	personID := exercisePersonWriteInvariants(t, e, me["user"].(anyMap)["id"].(string))
	dealID := exerciseDealToWon(t, e, stages)

	exerciseActivityIdempotentCapture(t, e, dealID)

	// --- lead: segregated, dedupes on email ---
	var lead anyMap
	status := e.call(t, "POST", "/v1/leads", anyMap{
		"full_name":    "Cold Prospect",
		"email":        "cold@example.org",
		"company_name": "Unknown AG",
		"source":       "import:batch-1",
	}, nil, &lead)
	if status != http.StatusCreated {
		t.Fatalf("create lead = %d %v", status, lead)
	}
	status = e.call(t, "POST", "/v1/leads", anyMap{
		"email":  "cold@example.org",
		"source": "import:batch-2",
	}, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("duplicate lead = %d, want 409", status)
	}

	// --- archive cascades and stays fetchable by id ---
	var person anyMap
	if status := e.call(t, "DELETE", "/v1/people/"+personID, nil, nil, &person); status != http.StatusOK {
		t.Fatalf("archive person = %d", status)
	}
	if person["archived_at"] == nil {
		t.Error("archived person carries no archived_at")
	}
	var people struct {
		Data []anyMap `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/people", nil, nil, &people); status != http.StatusOK {
		t.Fatalf("list people = %d", status)
	}
	for _, p := range people.Data {
		if p["id"] == personID {
			t.Error("archived person still appears in the default list")
		}
	}

	// --- the governance audit view reflects the session's own trail ---
	var audit struct {
		Data []anyMap `json:"data"`
		Page anyMap   `json:"page"`
	}
	if status := e.call(t, "GET", "/v1/audit-log?entity_type=person&action=archive", nil, nil, &audit); status != http.StatusOK {
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
	e := setup(t)
	e.bootstrapWorkspace(t)

	// An unimplemented contract operation answers an explicit 501.
	var problem anyMap
	if status := e.call(t, "POST", "/v1/coldstart", anyMap{"url": "https://example.com"}, nil, &problem); status != http.StatusNotImplemented {
		t.Fatalf("unimplemented op = %d %v, want 501", status, problem)
	}

	// Logout revokes; the session no longer authenticates.
	if status := e.call(t, "POST", "/v1/auth/logout", nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout = %d", status)
	}
	if status := e.call(t, "GET", "/v1/me", nil, nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("/me after logout = %d, want 401", status)
	}

	// Login re-authenticates with fresh credentials.
	var me anyMap
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email":    "ada@example.com",
		"password": "correct-horse-battery",
	}, nil, &me); status != http.StatusOK {
		t.Fatalf("login = %d", status)
	}
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me after login = %d", status)
	}

	// Wrong password is a 401 that does not say which half was wrong.
	var authErr anyMap
	if status := e.call(t, "POST", "/v1/auth/login", anyMap{
		"email":    "ada@example.com",
		"password": "wrong",
	}, nil, &authErr); status != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", status)
	}
}

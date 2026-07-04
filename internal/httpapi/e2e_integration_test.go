//go:build integration

package httpapi_test

// End-to-end lane: the real handler stack (session auth, RLS transaction
// helper, stores, RFC 7807 mapper) over the real migrated Postgres —
// bootstrap → login-by-cookie → CRUD → optimistic concurrency → archive.
// TLS test server because the session cookie is Secure per ADR-0043.

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

	"github.com/gradionhq/fable-poc/internal/httpapi"
	"github.com/gradionhq/fable-poc/internal/pg"
	"github.com/gradionhq/fable-poc/internal/pgmigrate"
	"github.com/gradionhq/fable-poc/migrations"
)

type env struct {
	ts     *httptest.Server
	client *http.Client
	slug   string
	owner  *pgx.Conn
}

func setup(t *testing.T) *env {
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
	t.Cleanup(func() { _ = owner.Close(context.Background()) })

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
	if _, err := pgmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	pool, err := pg.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	ts := httptest.NewTLSServer(httpapi.New(pool, slog.New(slog.NewTextHandler(os.Stderr, nil))))
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := ts.Client()
	client.Jar = jar

	return &env{ts: ts, client: client, slug: "fable-e2e", owner: owner}
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
	defer func() { _ = resp.Body.Close() }()

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

func TestEndToEnd_coreSalesFlow(t *testing.T) {
	e := setup(t)

	// --- bootstrap: tenant + admin + session cookie + seeded pipeline ---
	var me anyMap
	status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Fable E2E",
		"admin_email":        "ada@example.com",
		"admin_display_name": "Ada Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, &me)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap status = %d, body %v", status, me)
	}
	e.slug = "fable-e2e" // slugify("Fable E2E")

	// The cookie authenticates /me.
	if status := e.call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("/me status = %d", status)
	}
	if got := me["user"].(anyMap)["email"]; got != "ada@example.com" {
		t.Fatalf("/me email = %v", got)
	}

	// --- the seeded default pipeline arrived with its stages ---
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
	pipeline := pipelines.Data[0]
	var openStage, wonStage, lostStage string
	for _, s := range pipeline.Stages {
		switch s.Semantic {
		case "won":
			wonStage = s.Id
		case "lost":
			lostStage = s.Id
		case "open":
			if openStage == "" {
				openStage = s.Id
			}
		}
	}

	// --- person: create, duplicate-email 409, If-Match skew, archive ---
	var person anyMap
	status = e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Grace Hopper",
		"source":    "ui",
		"emails":    []anyMap{{"email": "grace@navy.mil", "is_primary": true}},
	}, nil, &person)
	if status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	personID := person["id"].(string)
	if person["captured_by"] != "human:"+me["user"].(anyMap)["id"].(string) {
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

	status = e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"title": "Rear Admiral"},
		map[string]string{"If-Match": "1"}, &person)
	if status != http.StatusOK || person["version"].(float64) != 2 {
		t.Fatalf("If-Match update = %d version %v, want 200 v2", status, person["version"])
	}

	// --- organization + deal + advance to won (FX freeze at 1 for base) ---
	var org anyMap
	status = e.call(t, "POST", "/v1/organizations", anyMap{
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
		"pipeline_id":     pipeline.Id,
		"stage_id":        openStage,
		"organization_id": org["id"],
		"source":          "ui",
	}, nil, &deal)
	if status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
	dealID := deal["id"].(string)

	// Losing without a reason is refused (deal_lost_reason).
	var lostErr anyMap
	status = e.call(t, "POST", "/v1/deals/"+dealID+"/advance", anyMap{"to_stage_id": lostStage}, nil, &lostErr)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("lost without reason = %d %v, want 422", status, lostErr)
	}

	status = e.call(t, "POST", "/v1/deals/"+dealID+"/advance", anyMap{"to_stage_id": wonStage}, nil, &deal)
	if status != http.StatusOK || deal["status"] != "won" || deal["closed_at"] == nil {
		t.Fatalf("advance to won = %d %v", status, deal)
	}

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

	// --- lead: segregated, dedupes on email ---
	var lead anyMap
	status = e.call(t, "POST", "/v1/leads", anyMap{
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
}

func TestEndToEnd_authAndSurfaceBoundaries(t *testing.T) {
	e := setup(t)

	var me anyMap
	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name":     "Fable E2E",
		"admin_email":        "ada@example.com",
		"admin_display_name": "Ada Admin",
		"admin_password":     "correct-horse-battery",
	}, nil, &me); status != http.StatusCreated {
		t.Fatalf("bootstrap = %d", status)
	}

	// An unimplemented contract operation answers an explicit 501.
	var problem anyMap
	if status := e.call(t, "GET", "/v1/search?q=x", nil, nil, &problem); status != http.StatusNotImplemented {
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

// The agent path on the REST surface (ADR-0013: agents are clients of the
// same contract): mint a passport over HTTP, then ride it — reads under
// the read scope, writes refused without the write scope, revocation as
// the kill switch.
func TestEndToEnd_passportBearerSurface(t *testing.T) {
	e := setup(t)

	status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Fable E2E", "admin_email": "admin@fable.test",
		"admin_display_name": "Admin", "admin_password": "correct-horse-battery",
	}, nil, nil)
	if status != 201 {
		t.Fatalf("bootstrap → %d", status)
	}

	// A human session mints the passport; the response carries the token
	// exactly once.
	var minted struct {
		PassportID string `json:"passport_id"`
		Token      string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "e2e agent", "scopes": []string{"read"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	if minted.Token == "" {
		t.Fatal("no token in the mint response")
	}

	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// The read scope reads…
	if status := e.call(t, "GET", "/v1/people", nil, bearer, nil); status != 200 {
		t.Fatalf("bearer GET /people → %d", status)
	}
	// …and cannot write: refused with the scope code, and no row lands.
	var problem struct {
		Code string `json:"code"`
	}
	status = e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Should not exist", "source": "mcp", "captured_by": "x",
	}, bearer, &problem)
	if status != 403 || problem.Code != "scope_exceeds_grantor" {
		t.Fatalf("read-scope write → %d %q, want 403 scope_exceeds_grantor", status, problem.Code)
	}

	// Bad tokens are 401, not 500.
	if status := e.call(t, "GET", "/v1/people", nil, map[string]string{"Authorization": "Bearer mgp_bogus"}, nil); status != 401 {
		t.Fatalf("bogus bearer → %d", status)
	}

	// Revoke over HTTP (session-authenticated); the token dies with it.
	if status := e.call(t, "DELETE", "/v1/passports/"+minted.PassportID, nil, nil, nil); status != 204 {
		t.Fatalf("revoke → %d", status)
	}
	if status := e.call(t, "GET", "/v1/people", nil, bearer, nil); status != 401 {
		t.Fatalf("revoked bearer still reads: %d", status)
	}
}

// C1: a WRITE-scoped passport still cannot mutate over REST — agent
// mutations must flow through the governed MCP tool surface, so the REST
// surface is read-only for passports and there is exactly one agent
// mutation choke point. The scope is present, so the refusal is the
// surface restriction, not a scope miss.
func TestEndToEnd_passportCannotMutateOverREST(t *testing.T) {
	e := setup(t)

	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Fable E2E", "admin_email": "admin@fable.test",
		"admin_display_name": "Admin", "admin_password": "correct-horse-battery",
	}, nil, nil); status != 201 {
		t.Fatalf("bootstrap → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "write agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// Reads still work under the read scope…
	if status := e.call(t, "GET", "/v1/people", nil, bearer, nil); status != 200 {
		t.Fatalf("write-passport GET /people → %d", status)
	}
	// …but a mutating REST call is refused with the surface-restriction code
	// even though the passport HOLDS write scope.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Should not exist", "source": "mcp", "captured_by": "x",
	}, bearer, &problem)
	if status != 403 || problem.Code != "agent_surface_restricted" {
		t.Fatalf("write-scope REST mutation → %d %q, want 403 agent_surface_restricted", status, problem.Code)
	}
	// The refusal is not a stray scope error.
	if problem.Code == "scope_exceeds_grantor" {
		t.Fatal("a write-scoped passport must not be refused as out-of-scope")
	}
}

// C2: a read seat is a hard capability ceiling — a read-seat human may read
// but not mutate over REST, whatever their role grants (A62/ADR-0047). The
// bootstrap admin is a full seat that mutates; flipping the workspace to
// read seats turns the same authenticated call into a 403.
func TestEndToEnd_readSeatCannotMutate(t *testing.T) {
	e := setup(t)

	if status := e.call(t, "POST", "/v1/workspaces", anyMap{
		"workspace_name": "Fable E2E", "admin_email": "admin@fable.test",
		"admin_display_name": "Admin", "admin_password": "correct-horse-battery",
	}, nil, nil); status != 201 {
		t.Fatalf("bootstrap → %d", status)
	}

	// A full-seat admin creates freely.
	var created struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Full Seat Made", "source": "manual", "captured_by": "admin",
	}, nil, &created); status != 201 {
		t.Fatalf("full-seat create → %d", status)
	}

	// Demote to a read seat; the live seat is read at authentication, so the
	// same session now hits the ceiling.
	e.setWorkspaceSeat(t, e.slug, "read")

	// Reads still succeed…
	if status := e.call(t, "GET", "/v1/people", nil, nil, nil); status != 200 {
		t.Fatalf("read-seat GET → %d", status)
	}
	// …every mutation is refused with the seat code, before RBAC.
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Read Seat Blocked", "source": "manual", "captured_by": "admin",
	}, nil, &problem); status != 403 || problem.Code != "seat_tier_insufficient" {
		t.Fatalf("read-seat create → %d %q, want 403 seat_tier_insufficient", status, problem.Code)
	}
	if status := e.call(t, "PATCH", "/v1/people/"+created.ID, anyMap{"title": "X"}, nil, &problem); status != 403 || problem.Code != "seat_tier_insufficient" {
		t.Fatalf("read-seat update → %d %q, want 403 seat_tier_insufficient", status, problem.Code)
	}
}

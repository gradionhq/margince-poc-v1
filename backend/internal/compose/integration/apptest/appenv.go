// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package apptest

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
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// AppEnv is the heaviest of the three exported fixtures: a real compose handler
// stack behind a TLS test server, with a cookie-jar client already logged in.
// integration.Env and integration.SearchEnv give a suite a migrated database;
// this gives it the running application, which is what a suite asserting
// transport, session auth or an RFC 7807 body actually needs.
//
// It is AppEnv rather than Env because integration.Env is the database fixture.
// TLS rather than plain HTTP because the session cookie is Secure per ADR-0043,
// and a plain server would drop it and fail every authenticated call for a reason
// unrelated to the test.
type AppEnv struct {
	TS     *httptest.Server
	Client *http.Client
	Slug   string
	Owner  *pgx.Conn
	Pool   *pgxpool.Pool
}

// SetupApp boots the default harness server — no schema pool, so the
// customfields runtime-DDL operations answer their generated 501 (the
// unwired-by-default posture). Suites that need the schema pool wired
// (customfields_http_integration_test.go) call SetupAppWithOptions directly with
// compose.WithSchemaPool(integration.SchemaPool(t)).
func SetupApp(t *testing.T) *AppEnv {
	t.Helper()
	return SetupAppWithOptions(t)
}

// SetupAppWithOptions is SetupApp's body, parameterized over extra compose
// options so a suite that needs a boot-optional seam (e.g. the
// customfields schema pool) can wire it without duplicating the
// migrate-and-boot ceremony every other suite in this package shares.
func SetupAppWithOptions(t *testing.T, opts ...compose.Option) *AppEnv {
	t.Helper()
	return SetupAppWithOriginOptions(t, func(string) []compose.Option { return opts })
}

// SetupAppWithOriginOptions is SetupAppWithOptions for a suite whose wiring must
// name the harness's OWN origin — the RFC 8414/9728 discovery documents carry
// absolute URLs, and a suite asserting what a real client dereferences cannot
// hardcode a port the OS assigns. The listener is opened before the handler is
// composed, so the origin is known without booting twice.
func SetupAppWithOriginOptions(t *testing.T, opts func(origin string) []compose.Option) *AppEnv {
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

	if err := testdb.EnsureSchema(ctx, owner); err != nil {
		t.Fatalf("migrating schema: %v", err)
	}
	if err := testdb.Reset(ctx, owner); err != nil {
		t.Fatalf("resetting database: %v", err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// The delivery machinery every send transport is composed with in the api
	// role. Without it a send refuses rather than log an activity claiming a
	// message went out, so a harness missing it would test the refusal in
	// every suite that sends — including the consent and preference-center
	// suites, whose subject is what happens AFTER a send is accepted.
	applyRiverSchema(t)
	sendInserter, err := jobs.NewInserter(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	// Unstarted, so the listener's port is known before the handler is
	// composed: StartTLS below serves the same handler NewTLSServer would.
	ts := httptest.NewUnstartedServer(nil)
	origin := "https://" + ts.Listener.Addr().String()
	allOpts := append([]compose.Option{
		compose.WithPublicBaseURL("https://mail.example.test"),
		compose.WithDelivery(compose.NewDeliveryStager(pool, sendInserter)),
	}, opts(origin)...)
	ts.Config.Handler = compose.New(pool, slog.New(slog.NewTextHandler(os.Stderr, nil)), allOpts...)
	ts.StartTLS()
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := ts.Client()
	client.Jar = jar

	return &AppEnv{TS: ts, Client: client, Slug: "fable-e2e", Owner: owner, Pool: pool}
}

// BootstrapWorkspace provisions the organization + admin (the A107 boot
// path) and leaves the admin session cookie in the client jar — the
// first step of every e2e scenario.
func (e *AppEnv) BootstrapWorkspace(t *testing.T) {
	t.Helper()
	BootstrapWorkspaceSession(t, e, "Fable E2E", "ada@example.com", "Ada Admin")
	e.Slug = "fable-e2e" // slugify("Fable E2E")
}

// SetWorkspaceSeat flips a workspace's users to a seat type through the
// owner connection, inside a workspace-bound transaction so RLS (FORCE)
// admits the UPDATE. Used to drive the read-seat ceiling from a test.
func (e *AppEnv) SetWorkspaceSeat(t *testing.T, slug, seat string) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.Owner.Begin(ctx)
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

// Call issues one API request with the dev workspace header and decodes
// the JSON response into out (when non-nil), returning the status code.
//
//craft:ignore naked-any the test transport seam: body/out are whichever request/response shapes the scenario exercises
func (e *AppEnv) Call(t *testing.T, method, path string, body any, headers map[string]string, out any) int {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.TS.URL+path, reqBody)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer CloseBody(t, resp)

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

// AnyMap is the shape a scenario builds an ad-hoc JSON request body in, and
// reads a response into when it asserts on one or two fields rather than a
// typed struct. An alias, not a new type, so it interchanges freely with the
// map literals the suites already write.
type AnyMap = map[string]any

// BootstrapWorkspaceSession provisions the installation's organization through
// the A107 boot path — configuration-driven, exactly what cmd/api runs at
// startup — and signs its admin in over HTTP. The arrange step every e2e
// scenario shares. The login also primes the server's singleton-organization
// resolution before a test seeds any cross-tenant rows directly.
//
// It lives here rather than with a suite because BootstrapWorkspace above calls
// it, and a non-test file cannot reach a helper declared in a _test.go one.
func BootstrapWorkspaceSession(t *testing.T, e *AppEnv, organizationName, adminEmail, adminName string) {
	t.Helper()
	pwFile := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(pwFile, []byte("correct-horse-battery"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := deployconfig.Config{
		Version:      1,
		Organization: deployconfig.Organization{Name: organizationName},
		BootstrapAdmin: &deployconfig.BootstrapAdmin{
			Email: adminEmail, DisplayName: adminName, PasswordFile: pwFile,
		},
	}
	if err := compose.EnsureInstallation(context.Background(), e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if status := e.Call(t, "POST", "/v1/auth/login", AnyMap{
		"email": adminEmail, "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("login → %d", status)
	}
}

// CloseBody closes a response body and fails the test if it cannot, so a leaked
// body is a red test rather than a slow drip nobody attributes.
// CloseBody closes a response body and fails the test on a dirty close —
// a broken close can hide a truncated read.
func CloseBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("closing response body: %v", err)
	}
}

// applyRiverSchema gives the booted app River's schema on the harness-migrated
// database, as cmd/migrate does after core and custom.
//
// A near-copy of integration.ApplyRiverSchema, and deliberately so. This package
// exists to break an import cycle: package compose's own white-box tests import
// package integration, so nothing there may import compose — and this fixture
// boots a compose handler stack. Importing integration back from here would
// close the cycle the other way round, because integration's suites import this
// package. Twelve duplicated lines are what that boundary costs, and the
// original cannot move here either: package compose's tests use it too, and
// would then reach compose through it.
func applyRiverSchema(t *testing.T) {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	if ownerDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	ownerPool, err := database.NewPool(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("opening owner pool: %v", err)
	}
	defer ownerPool.Close()
	if err := testdb.EnsureRiverSchema(ctx, ownerPool, jobs.Migrate); err != nil {
		t.Fatal(err)
	}
}

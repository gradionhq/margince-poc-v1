// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The Surface-A1 test plumbing shared by the mcp integration scenarios:
// the pipe-driven stdio client, the provisioned workspace fixture
// (migrated schema, seeded pipeline, read-write + read-only passports),
// and the small parsing helpers the governed-call assertions reuse.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
)

// mcpClient drives one stdio server session like an agent client would.
type mcpClient struct {
	t   *testing.T
	enc *json.Encoder
	sc  *bufio.Scanner
	seq int
}

func startMCP(t *testing.T, token, slug string, svc *identity.Service, pool *pgxpool.Pool) *mcpClient {
	t.Helper()
	registry := compose.NewRegistry(pool)

	bind := func(ctx context.Context) (context.Context, error) {
		wsID, err := svc.ResolveWorkspace(ctx, slug)
		if err != nil {
			return nil, err
		}
		ctx = principal.WithWorkspaceID(ctx, wsID.UUID)
		agent, err := svc.AuthenticateAgent(ctx, token)
		if err != nil {
			return nil, err
		}
		return principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7()), nil
	}

	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	srv := agents.NewStdioServer(registry, bind, "margince-crm", "test")
	go func() {
		// Serve returns when the client half of the pipes closes at test
		// cleanup — its exit is the shutdown signal, not a failure, and
		// asserting on it from this goroutine would race test completion.
		//craft:ignore swallowed-errors the Serve exit error at pipe teardown is the intended shutdown path, and this goroutine outlives the test's assertion window
		_ = srv.Serve(context.Background(), serverIn, serverOut)
		//craft:ignore swallowed-errors best-effort close of the response pipe after the server already exited; no reader is left to care
		_ = serverOut.Close()
	}()
	t.Cleanup(func() {
		if err := clientOut.Close(); err != nil {
			t.Errorf("closing the client pipe: %v", err)
		}
	})

	sc := bufio.NewScanner(clientIn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &mcpClient{t: t, enc: json.NewEncoder(clientOut), sc: sc}
}

//craft:ignore naked-any the JSON-RPC seam: params are whatever shape the method under test sends
func (c *mcpClient) rpc(method string, params any) json.RawMessage {
	c.t.Helper()
	c.seq++
	if err := c.enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": c.seq, "method": method, "params": params,
	}); err != nil {
		c.t.Fatal(err)
	}
	if !c.sc.Scan() {
		c.t.Fatalf("no response to %s: %v", method, c.sc.Err())
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(c.sc.Bytes(), &resp); err != nil {
		c.t.Fatalf("malformed response: %v", err)
	}
	if resp.Error != nil {
		c.t.Fatalf("%s → rpc error: %s", method, resp.Error.Message)
	}
	return resp.Result
}

// callTool returns the tool result text and whether it was an error.
//
//craft:ignore naked-any the JSON-RPC seam: each tool declares its own argument shape
func (c *mcpClient) callTool(name string, args any) (string, bool) {
	c.t.Helper()
	raw := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	var res struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		c.t.Fatal(err)
	}
	if len(res.Content) == 0 {
		c.t.Fatalf("%s returned no content", name)
	}
	return res.Content[0].Text, res.IsError
}

// mcpEnv is the provisioned Surface-A1 fixture: a migrated schema, one
// workspace with the seeded pipeline, a read-write and a read-only
// passport, and the approvals service plus the deciding human's context.
type mcpEnv struct {
	owner        *pgx.Conn
	pool         *pgxpool.Pool
	svc          *identity.Service
	admin        identity.Identity
	wsCtx        context.Context
	rw           identity.IssuedPassport
	ro           identity.IssuedPassport
	approvalsSvc *approvals.Service
	humanCtx     context.Context
}

func setupMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()

	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatal(err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatal(err)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	svc := identity.NewService(pool)
	admin, _, err := svc.Bootstrap(ctx, identity.BootstrapInput{
		WorkspaceName: "Agent Test", Slug: "agent-test",
		AdminEmail: "admin@agent.test", AdminName: "Admin",
		AdminPassword: "correct-horse-battery",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wsCtx := principal.WithWorkspaceID(ctx, admin.WorkspaceID.UUID)
	// The seed emits pipeline.created, and every emission needs the
	// correlation the HTTP layer normally mints.
	seedCtx := principal.WithCorrelationID(
		principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"}), ids.NewV7())
	if err := deals.NewHandlers(pool).SeedWorkspaceDefaults(seedCtx); err != nil {
		t.Fatal(err)
	}

	rw, err := svc.IssuePassport(wsCtx, admin, identity.IssuePassportInput{Scopes: []string{"read", "write"}})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := svc.IssuePassport(wsCtx, admin, identity.IssuePassportInput{Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}

	humanCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + admin.UserID.String(),
		UserID: admin.UserID.UUID, Permissions: admin.Permissions,
	}), ids.NewV7())

	return &mcpEnv{
		owner: owner, pool: pool, svc: svc, admin: admin, wsCtx: wsCtx,
		rw: rw, ro: ro, approvalsSvc: approvals.NewService(pool), humanCtx: humanCtx,
	}
}

// seededStages resolves the default pipeline's first two open stages and
// its won stage straight from the schema.
func seededStages(t *testing.T, owner *pgx.Conn, wsID ids.UUID) (pipeline, openA, openB, won string) {
	t.Helper()
	rows, err := owner.Query(context.Background(),
		`SELECT s.id::text, s.pipeline_id::text, s.semantic FROM stage s
		 WHERE s.workspace_id = $1 ORDER BY s.position`, wsID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, pid, semantic string
		if err := rows.Scan(&id, &pid, &semantic); err != nil {
			t.Fatal(err)
		}
		pipeline = pid
		switch {
		case semantic == "open" && openA == "":
			openA = id
		case semantic == "open" && openB == "":
			openB = id
		case semantic == "won":
			won = id
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if openA == "" || openB == "" || won == "" {
		t.Fatalf("seeded pipeline lacks two open stages + won (a=%s b=%s won=%s)", openA, openB, won)
	}
	return pipeline, openA, openB, won
}

var approvalIDPattern = regexp.MustCompile(`staged as approval ([0-9a-f-]{36})`)

func extractApprovalID(t *testing.T, text string) ids.UUID {
	t.Helper()
	m := approvalIDPattern.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no approval id in %q", text)
	}
	id, err := ids.Parse(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func withApproval(args map[string]any, id ids.UUID) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out["approval_id"] = id.String()
	return out
}

func strPtr(s string) *string { return &s }

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The Surface-A1 end-to-end: a real workspace, a real passport, the real
// stdio server over pipes — initialize, tools/list, then the governed
// calls: 🟢 create/search/read/log/advance(open→open), the 🟡 floor on
// advance→won (zero side effects), scope refusal on a read-only passport,
// and the agent:* provenance the store must stamp.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
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
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
		ctx = principal.WithWorkspaceID(ctx, wsID)
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
		_ = srv.Serve(context.Background(), serverIn, serverOut)
		_ = serverOut.Close()
	}()
	t.Cleanup(func() { _ = clientOut.Close() })

	sc := bufio.NewScanner(clientIn)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	return &mcpClient{t: t, enc: json.NewEncoder(clientOut), sc: sc}
}

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

func TestMCPSurfaceEndToEnd(t *testing.T) {
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
	defer func() { _ = owner.Close(context.Background()) }()
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
	defer pool.Close()

	svc := identity.NewService(pool)
	dealsHandlers := deals.NewHandlers(pool)

	admin, _, err := svc.Bootstrap(ctx, identity.BootstrapInput{
		WorkspaceName: "Agent Test", Slug: "agent-test",
		AdminEmail: "admin@agent.test", AdminName: "Admin",
		AdminPassword: "correct-horse-battery",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wsCtx := principal.WithWorkspaceID(ctx, admin.WorkspaceID)
	// The seed emits pipeline.created, and every emission needs the
	// correlation the HTTP layer normally mints.
	seedCtx := principal.WithCorrelationID(
		principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"}), ids.NewV7())
	if err := dealsHandlers.SeedWorkspaceDefaults(seedCtx); err != nil {
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

	approvalsSvc := approvals.NewService(pool)
	c := startMCP(t, rw.Token, "agent-test", svc, pool)

	// The protocol handshake + the declared surface.
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(c.rpc("initialize", map[string]any{}), &init); err != nil || init.ProtocolVersion == "" {
		t.Fatalf("initialize: %v (%+v)", err, init)
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(c.rpc("tools/list", map[string]any{}), &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"search_records", "read_record", "create_record", "update_record", "log_activity", "advance_deal"} {
		if !names[want] {
			t.Fatalf("tools/list is missing %s (surface: %v)", want, list.Tools)
		}
	}

	// 🟢 create: the record lands with agent:* provenance, server-stamped.
	text, isErr := c.callTool("create_record", map[string]any{
		"record_type": "person",
		"fields":      map[string]any{"full_name": "Agent Made", "title": "CTO"},
	})
	if isErr {
		t.Fatalf("create_record: %s", text)
	}
	var created struct {
		ID     ids.UUID `json:"id"`
		Fields struct {
			CapturedBy string `json:"captured_by"`
			Source     string `json:"source"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatal(err)
	}
	if want := "agent:" + rw.ID.String(); created.Fields.CapturedBy != want {
		t.Errorf("captured_by = %q, want the server-stamped %q", created.Fields.CapturedBy, want)
	}
	if created.Fields.Source != "mcp" {
		t.Errorf("source = %q, want mcp", created.Fields.Source)
	}

	// 🟢 search + read round-trip.
	text, isErr = c.callTool("search_records", map[string]any{"q": "Agent Made", "record_type": "person"})
	if isErr || !strings.Contains(text, created.ID.String()) {
		t.Fatalf("search_records did not find the created person: err=%v %s", isErr, text)
	}
	if text, isErr = c.callTool("read_record", map[string]any{"record_type": "person", "id": created.ID}); isErr {
		t.Fatalf("read_record: %s", text)
	}

	// 🟢 log_activity linked to the person.
	if text, isErr = c.callTool("log_activity", map[string]any{
		"kind": "note", "subject": "Agent's note",
		"links": []map[string]any{{"entity_type": "person", "entity_id": created.ID}},
	}); isErr {
		t.Fatalf("log_activity: %s", text)
	}

	// advance_deal: open→open is 🟢, →won is 🟡 with ZERO side effects.
	pipelineID, openA, openB, wonStage := seededStages(t, owner, admin.WorkspaceID)
	text, isErr = c.callTool("create_record", map[string]any{
		"record_type": "deal",
		"fields": map[string]any{
			"name": "Agent deal", "pipeline_id": pipelineID, "stage_id": openA,
		},
	})
	if isErr {
		t.Fatalf("create deal: %s", text)
	}
	var deal struct {
		ID ids.UUID `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &deal); err != nil {
		t.Fatal(err)
	}

	if text, isErr = c.callTool("advance_deal", map[string]any{"deal_id": deal.ID, "to_stage_id": openB}); isErr {
		t.Fatalf("open→open advance should be 🟢: %s", text)
	}
	winArgs := map[string]any{"deal_id": deal.ID, "to_stage_id": wonStage}
	text, isErr = c.callTool("advance_deal", winArgs)
	if !isErr || !strings.Contains(text, "staged as approval") {
		t.Fatalf("advance→won must stage an approval, got err=%v %s", isErr, text)
	}
	var stageAfter, statusAfter string
	if err := owner.QueryRow(ctx, `SELECT stage_id::text, status FROM deal WHERE id = $1`, deal.ID).
		Scan(&stageAfter, &statusAfter); err != nil {
		t.Fatal(err)
	}
	if stageAfter != openB || statusAfter != "open" {
		t.Fatalf("the refused 🟡 call left side effects: stage=%s status=%s", stageAfter, statusAfter)
	}

	// --- the full 🟡 loop: stage → human decision → redemption ---
	approvalID := extractApprovalID(t, text)

	// The staged item sits in the human inbox.
	humanCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + admin.UserID.String(),
		UserID: admin.UserID, Permissions: admin.Permissions,
	}), ids.NewV7())
	pending, err := approvalsSvc.List(humanCtx, strPtr("pending"), 50)
	if err != nil || len(pending) != 1 {
		t.Fatalf("inbox: %v (%d items)", err, len(pending))
	}

	// C3: a human who could not DECIDE the staged action cannot even SEE it.
	// A low-privilege viewer (no grants) gets an empty inbox and a
	// not-found on the id — the inbox is never a workspace-wide side channel
	// leaking proposed_change/diffs.
	strangerCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:stranger", UserID: ids.NewV7(),
	}), ids.NewV7())
	if leaked, err := approvalsSvc.List(strangerCtx, strPtr("pending"), 50); err != nil || len(leaked) != 0 {
		t.Fatalf("C3: low-priv inbox leaked %d items (err=%v), want 0", len(leaked), err)
	}
	if _, err := approvalsSvc.Get(strangerCtx, approvalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("C3: low-priv Get on a foreign approval → %v, want ErrNotFound", err)
	}

	// Redemption before approval is refused; an agent cannot decide.
	if text, isErr = c.callTool("advance_deal", withApproval(winArgs, approvalID)); !isErr || !strings.Contains(text, "pending") {
		t.Fatalf("undedecided approval redeemed: err=%v %s", isErr, text)
	}
	agentCtx := principal.WithCorrelationID(principal.WithActor(wsCtx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + rw.ID.String(), PassportID: rw.ID,
	}), ids.NewV7())
	if _, err := approvalsSvc.Decide(agentCtx, approvalID, true, nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("an agent decided its own staging: %v", err)
	}

	// The human approves; the agent repeats the IDENTICAL call + approval_id.
	if _, err := approvalsSvc.Decide(humanCtx, approvalID, true, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// A retry with DIFFERENT args must not ride the approval.
	tampered := withApproval(map[string]any{"deal_id": deal.ID, "to_stage_id": wonStage, "lost_reason": "sneaky"}, approvalID)
	if text, isErr = c.callTool("advance_deal", tampered); !isErr || !strings.Contains(text, "differs") {
		t.Fatalf("tampered redemption passed: err=%v %s", isErr, text)
	}
	if text, isErr = c.callTool("advance_deal", withApproval(winArgs, approvalID)); isErr {
		t.Fatalf("approved redemption failed: %s", text)
	}
	if err := owner.QueryRow(ctx, `SELECT status FROM deal WHERE id = $1`, deal.ID).Scan(&statusAfter); err != nil {
		t.Fatal(err)
	}
	if statusAfter != "won" {
		t.Fatalf("deal status = %s after redeemed close, want won", statusAfter)
	}
	// Single-use: the same approval cannot authorize a second effect.
	if text, isErr = c.callTool("advance_deal", withApproval(winArgs, approvalID)); !isErr || !strings.Contains(text, "redeemed") {
		t.Fatalf("approval redeemed twice: err=%v %s", isErr, text)
	}

	// Version skew: an approval given for a row that then CHANGED must not
	// execute — stage archive, mutate the target, approve, redeem → skew.
	archiveArgs := map[string]any{"record_type": "person", "id": created.ID}
	text, _ = c.callTool("archive_record", archiveArgs)
	skewID := extractApprovalID(t, text)
	if text, isErr = c.callTool("update_record", map[string]any{
		"record_type": "person", "id": created.ID, "fields": map[string]any{"title": "CEO"},
	}); isErr {
		t.Fatalf("bump: %s", text)
	}
	if _, err := approvalsSvc.Decide(humanCtx, skewID, true, nil); err != nil {
		t.Fatal(err)
	}
	if text, isErr = c.callTool("archive_record", withApproval(archiveArgs, skewID)); !isErr || !strings.Contains(text, "changed since") {
		t.Fatalf("stale approval executed against a changed row: err=%v %s", isErr, text)
	}

	// Reject path: a rejected staging never becomes authority.
	text, _ = c.callTool("archive_record", archiveArgs)
	rejectID := extractApprovalID(t, text)
	if _, err := approvalsSvc.Decide(humanCtx, rejectID, false, strPtr("keep them")); err != nil {
		t.Fatal(err)
	}
	if text, isErr = c.callTool("archive_record", withApproval(archiveArgs, rejectID)); !isErr || !strings.Contains(text, "rejected") {
		t.Fatalf("rejected approval redeemed: err=%v %s", isErr, text)
	}

	// merge_records rides the same 🟡 loop: two people staged → human
	// approves → the agent redeems the identical call and the survivor
	// absorbs the source.
	mkPerson := func(name string) ids.UUID {
		text, isErr := c.callTool("create_record", map[string]any{
			"record_type": "person", "fields": map[string]any{"full_name": name},
		})
		if isErr {
			t.Fatalf("create %s: %s", name, text)
		}
		var made struct {
			ID ids.UUID `json:"id"`
		}
		if err := json.Unmarshal([]byte(text), &made); err != nil {
			t.Fatal(err)
		}
		return made.ID
	}
	mergeSrc, mergeTgt := mkPerson("Dupe A"), mkPerson("Dupe B")
	mergeArgs := map[string]any{"record_type": "person", "source_id": mergeSrc, "target_id": mergeTgt}
	text, isErr = c.callTool("merge_records", mergeArgs)
	if !isErr || !strings.Contains(text, "staged as approval") {
		t.Fatalf("merge_records must stage a 🟡 approval, got err=%v %s", isErr, text)
	}
	mergeID := extractApprovalID(t, text)
	if _, err := approvalsSvc.Decide(humanCtx, mergeID, true, nil); err != nil {
		t.Fatalf("approve merge: %v", err)
	}
	if text, isErr = c.callTool("merge_records", withApproval(mergeArgs, mergeID)); isErr {
		t.Fatalf("approved merge did not redeem: %s", text)
	}
	if !strings.Contains(text, mergeTgt.String()) {
		t.Errorf("merge result names survivor? %s", text)
	}
	// The source is now merged away — reading it as an agent 404s.
	if text, isErr = c.callTool("read_record", map[string]any{"record_type": "person", "id": mergeSrc}); !isErr {
		t.Errorf("merged-away source still reads live: %s", text)
	}

	// A read-only passport cannot reach a write tool — refused at the
	// gate, before the handler.
	roClient := startMCP(t, ro.Token, "agent-test", svc, pool)
	text, isErr = roClient.callTool("create_record", map[string]any{
		"record_type": "person", "fields": map[string]any{"full_name": "Should not exist"},
	})
	if !isErr || !strings.Contains(text, "scope") {
		t.Fatalf("read-only passport wrote: err=%v %s", isErr, text)
	}
	if text, isErr = roClient.callTool("search_records", map[string]any{"q": "Agent Made"}); isErr {
		t.Fatalf("read-only passport should read: %s", text)
	}

	// Revocation binds on the NEXT call of a live session — no reconnect.
	if err := svc.RevokePassport(wsCtx, admin, ro.ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr = roClient.callTool("search_records", map[string]any{"q": "x"}); !isErr || !strings.Contains(text, "authentication") {
		t.Fatalf("revoked passport kept working: err=%v %s", isErr, text)
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

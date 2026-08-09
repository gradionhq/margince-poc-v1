// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The io.modelcontextprotocol/tasks extension, end to end on the real origin
// against a real database.
//
// The unit suite proves the state machine over fakes. Only this one proves the
// things that live in SQL and nowhere else: that a handle is durable across
// requests, that the effect a released poll performs commits exactly once, that
// a second poll is answered from the recorded result rather than by running
// anything again, and that the handle is worthless to a passport it was not
// minted for.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// The per-request metadata a TASK-CAPABLE modern client sends, spelled as the
// specification writes it — this suite is a client, so it must not read the
// server's own constants for what to put on the wire.
const tasksMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/tasks":{}}}}`

// TestAConfirmFirstCallBecomesATaskAndCompletesOnce is the headline claim: the
// dead end becomes a handle, the human's decision moves it, and the effect
// lands exactly once no matter how often the client polls.
func TestAConfirmFirstCallBecomesATaskAndCompletesOnce(t *testing.T) {
	e := setupConnector(t)
	bearer := passportBearer(t, e.AppEnv, "task client", "read", "write")
	leadID := createDisqualifiableLead(t, e.AppEnv)

	// The 🟡 call. A client that declared the extension is handed a handle
	// instead of the refusal it would have got before.
	created := rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{%s,
			"name":"disqualify_lead","arguments":{"lead_id":%q}}}`, tasksMeta, leadID),
		modernHeaders(bearer, "tools/call", "disqualify_lead")).Body)

	if created["resultType"] != "task" {
		t.Fatalf("resultType = %v, want task — a declaring client's staged call must answer a handle", created["resultType"])
	}
	taskID, _ := created["taskId"].(string)
	if taskID == "" {
		t.Fatalf("the handle carried no taskId: %v", created)
	}
	if created["status"] != "working" {
		t.Errorf("status = %v, want working", created["status"])
	}

	// The lead is untouched: a handle is a promise to poll, never an effect.
	if disqualified(t, e.AppEnv, leadID) {
		t.Fatal("the lead was disqualified before anyone approved it")
	}

	// Polling before the decision reports progress and changes nothing. This is
	// also the durability claim the specification makes: the handle resolves on
	// a request that shares no state with the one that minted it.
	pending := pollTask(t, e, bearer, taskID)
	if pending["status"] != "working" {
		t.Fatalf("status before the decision = %v, want working", pending["status"])
	}
	if disqualified(t, e.AppEnv, leadID) {
		t.Fatal("polling performed the effect nobody had approved")
	}

	// A person approves it in Margince, exactly as they would today.
	approveStagedCall(t, e.AppEnv)

	// The next poll performs the effect under its OWN passport and completes.
	completed := pollTask(t, e, bearer, taskID)
	if completed["status"] != "completed" {
		t.Fatalf("status after approval = %v (%v), want completed", completed["status"], completed["statusMessage"])
	}
	if completed["result"] == nil {
		t.Fatal("a completed task carried no result, so a second poll would have nothing to answer with")
	}
	if !disqualified(t, e.AppEnv, leadID) {
		t.Fatal("the task completed but the effect never landed")
	}
	if completed["pollIntervalMs"] != nil {
		t.Error("a terminal task invited another poll")
	}

	// And the second poll is answered from what the first recorded. Nothing
	// runs again — which matters because it could not: redemption is
	// single-use, so a re-run would fail and a client would see a completed
	// task turn into a refusal.
	again := pollTask(t, e, bearer, taskID)
	if again["status"] != "completed" {
		t.Fatalf("the second poll reported %v, want the recorded completed answer", again["status"])
	}
	if !sameJSON(t, completed["result"], again["result"]) {
		t.Errorf("the second poll answered a different result:\n%v\n%v", completed["result"], again["result"])
	}
	if staged := stagedApprovalCount(t, e.AppEnv); staged != 1 {
		t.Errorf("%d approvals were staged across the whole flow, want exactly 1", staged)
	}
}

// A handle is bound to the passport it was minted for, and the binding is the
// only thing that makes possessing an id worthless. A different passport gets
// the answer an unknown id gets — telling the two apart would say which ids
// exist.
func TestATaskIsInvisibleToAnyOtherPassport(t *testing.T) {
	e := setupConnector(t)
	mine := passportBearer(t, e.AppEnv, "the minting client", "read", "write")
	theirs := passportBearer(t, e.AppEnv, "another client", "read", "write")
	leadID := createDisqualifiableLead(t, e.AppEnv)

	created := rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{%s,
			"name":"disqualify_lead","arguments":{"lead_id":%q}}}`, tasksMeta, leadID),
		modernHeaders(mine, "tools/call", "disqualify_lead")).Body)
	taskID, _ := created["taskId"].(string)
	if taskID == "" {
		t.Fatalf("the handle carried no taskId: %v", created)
	}

	foreign := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{%s,"taskId":%q}}`, tasksMeta, taskID),
		modernHeaders(theirs, "tasks/get", taskID))
	unknown := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tasks/get","params":{%s,"taskId":"019fe000-0000-7000-8000-00000000dead"}}`, tasksMeta),
		modernHeaders(theirs, "tasks/get", "019fe000-0000-7000-8000-00000000dead"))

	foreignCode, foreignMessage := rpcErrorOf(t, foreign.Body)
	unknownCode, unknownMessage := rpcErrorOf(t, unknown.Body)
	if foreignCode != -32602 {
		t.Fatalf("another passport's poll → %d %q, want -32602", foreignCode, foreignMessage)
	}
	if foreignMessage != unknownMessage {
		t.Errorf("a foreign task and an unknown one answered differently:\n%q\n%q\n"+
			"telling them apart is an existence oracle", foreignMessage, unknownMessage)
	}
	if unknownCode != foreignCode {
		t.Errorf("codes differ: %d vs %d", foreignCode, unknownCode)
	}
}

// Cancelling retracts the proposal. Leaving it in the inbox would leave a
// person a decision that can no longer take effect and that nobody can
// withdraw.
func TestCancellingATaskTakesItsProposalOffTheInbox(t *testing.T) {
	e := setupConnector(t)
	bearer := passportBearer(t, e.AppEnv, "task client", "read", "write")
	leadID := createDisqualifiableLead(t, e.AppEnv)

	created := rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{%s,
			"name":"disqualify_lead","arguments":{"lead_id":%q}}}`, tasksMeta, leadID),
		modernHeaders(bearer, "tools/call", "disqualify_lead")).Body)
	taskID, _ := created["taskId"].(string)

	cancelled := rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/cancel","params":{%s,"taskId":%q}}`, tasksMeta, taskID),
		modernHeaders(bearer, "tasks/cancel", taskID)).Body)
	if cancelled["resultType"] != "complete" {
		t.Errorf("tasks/cancel resultType = %v, want complete", cancelled["resultType"])
	}

	after := pollTask(t, e, bearer, taskID)
	if after["status"] != "cancelled" {
		t.Fatalf("status after cancel = %v, want cancelled", after["status"])
	}
	// The proposal is gone from the inbox — withdrawal is forced expiry, so the
	// row reads expired and can no longer be approved.
	if pendingApprovalCount(t, e.AppEnv) != 0 {
		t.Error("cancelling the task left a live proposal a person could still approve")
	}
	if disqualified(t, e.AppEnv, leadID) {
		t.Fatal("a cancelled task's effect landed anyway")
	}
}

// A client that did not declare the extension sees exactly what it always saw.
// This is the specification's MUST NOT, and it is what keeps every existing
// client — and the certification band — on byte-identical behaviour.
func TestANonDeclaringClientStillGetsThePlainRefusal(t *testing.T) {
	e := setupConnector(t)
	bearer := passportBearer(t, e.AppEnv, "old client", "read", "write")
	leadID := createDisqualifiableLead(t, e.AppEnv)

	refused := rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{%s,
			"name":"disqualify_lead","arguments":{"lead_id":%q}}}`, modernMeta, leadID),
		modernHeaders(bearer, "tools/call", "disqualify_lead")).Body)

	if refused["resultType"] != "complete" {
		t.Fatalf("resultType = %v, want complete — a non-declaring client must never be handed a task", refused["resultType"])
	}
	if refused["isError"] != true {
		t.Fatalf("a staged 🟡 call answered %v, want the in-band refusal", refused)
	}
	var tasks int
	if err := e.AppEnv.Owner.QueryRow(t.Context(), `SELECT count(*) FROM agent_task`).Scan(&tasks); err != nil {
		t.Fatalf("counting tasks: %v", err)
	}
	if tasks != 0 {
		t.Errorf("%d tasks were created for a client that cannot poll them", tasks)
	}
}

// The extension is advertised where a client can act on it, and the three
// methods refuse a request that did not declare it.
func TestTheExtensionIsAdvertisedAndItsMethodsRequireIt(t *testing.T) {
	e := setupConnector(t)
	bearer := passportBearer(t, e.AppEnv, "task client", "read")

	discovered := rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{%s}}`, tasksMeta),
		modernHeaders(bearer, "server/discover", "")).Body)
	capabilities, _ := discovered["capabilities"].(map[string]any)
	extensions, ok := capabilities["extensions"].(map[string]any)
	if !ok {
		t.Fatalf("server/discover advertised no extensions: %v", capabilities)
	}
	if _, named := extensions["io.modelcontextprotocol/tasks"]; !named {
		t.Fatalf("extensions = %v, want io.modelcontextprotocol/tasks", extensions)
	}

	// And a request that did not declare it is asking for a method that, for
	// that caller, does not exist.
	undeclared := mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{%s,"taskId":"019fe000-0000-7000-8000-00000000dead"}}`, modernMeta),
		modernHeaders(bearer, "tasks/get", "019fe000-0000-7000-8000-00000000dead"))
	code, message := rpcErrorOf(t, undeclared.Body)
	if code != -32021 {
		t.Fatalf("tasks/get without the capability → %d %q, want -32021 "+
			"(the core specification's MissingRequiredClientCapability)", code, message)
	}
}

// ---- helpers ----

// createDisqualifiableLead makes a lead the 🟡 tool has something to act on.
func createDisqualifiableLead(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var lead apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/leads", apptest.AnyMap{
		"full_name": "Task Lead", "email": "task@lead.test", "source": "manual",
	}, nil, &lead); status != http.StatusCreated {
		t.Fatalf("create lead → %d", status)
	}
	id, _ := lead["id"].(string)
	if id == "" {
		t.Fatal("the created lead carried no id")
	}
	return id
}

// pollTask runs one tasks/get as this passport and returns the task.
func pollTask(t *testing.T, e *connectorEnv, bearer map[string]string, taskID string) map[string]any {
	t.Helper()
	return rpcResult(t, mcpRaw(e.AppEnv, t, http.MethodPost, "/mcp",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tasks/get","params":{%s,"taskId":%q}}`, tasksMeta, taskID),
		modernHeaders(bearer, "tasks/get", taskID)).Body)
}

// approveStagedCall approves the one pending proposal, as the human admin.
func approveStagedCall(t *testing.T, e *apptest.AppEnv) {
	t.Helper()
	var approvalID string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT id::text FROM approval WHERE status = 'pending'`).Scan(&approvalID); err != nil {
		t.Fatalf("finding the staged proposal: %v", err)
	}
	if status := e.Call(t, "POST", "/v1/approvals/"+approvalID+"/approve", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("approve → %d", status)
	}
}

// disqualified reports whether the 🟡 effect actually landed.
func disqualified(t *testing.T, e *apptest.AppEnv, leadID string) bool {
	t.Helper()
	var status string
	if err := e.Owner.QueryRow(t.Context(), `SELECT status FROM lead WHERE id = $1`, leadID).Scan(&status); err != nil {
		t.Fatalf("reading the lead: %v", err)
	}
	return status == "disqualified"
}

func stagedApprovalCount(t *testing.T, e *apptest.AppEnv) int {
	t.Helper()
	var staged int
	if err := e.Owner.QueryRow(t.Context(), `SELECT count(*) FROM approval`).Scan(&staged); err != nil {
		t.Fatalf("counting approvals: %v", err)
	}
	return staged
}

func pendingApprovalCount(t *testing.T, e *apptest.AppEnv) int {
	t.Helper()
	var live int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM approval WHERE status = 'pending' AND expires_at > now()`).Scan(&live); err != nil {
		t.Fatalf("counting live proposals: %v", err)
	}
	return live
}

// rpcErrorOf decodes the ERROR half of a JSON-RPC response, which rpcResult
// deliberately refuses to do — several assertions here are about the refusal.
func rpcErrorOf(t *testing.T, body string) (int, string) {
	t.Helper()
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("JSON-RPC response does not decode: %v (%s)", err, body)
	}
	if envelope.Error == nil {
		t.Fatalf("expected a JSON-RPC error, got: %s", body)
	}
	return envelope.Error.Code, envelope.Error.Message
}

// sameJSON compares two decoded results by their re-encoded bytes, so a
// difference anywhere inside is caught rather than just at the top level.
func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	left, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("re-encoding a result: %v", err)
	}
	right, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("re-encoding a result: %v", err)
	}
	return string(left) == string(right)
}

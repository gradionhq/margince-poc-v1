// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the io.modelcontextprotocol/tasks extension obliges this server to do,
// and the two properties that make handing an agent a durable handle safe: the
// handle is worthless without the passport it was minted for, and nothing at
// all happens between polls.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/agentquota"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// The extension's identifier and the codes around it, spelled as the
// specification writes them rather than read off the constants the code uses. A
// test that reads the same constant proves only self-consistency — and a typo
// in the extension key would read every client as unable to hold a handle,
// which looks exactly like a client that declared nothing.
func TestTheTasksExtensionTokensAreSpelledAsTheSpecificationWritesThem(t *testing.T) {
	if extensionTasks != "io.modelcontextprotocol/tasks" {
		t.Errorf("extension = %q, want io.modelcontextprotocol/tasks", extensionTasks)
	}
	for _, tc := range []struct{ got, want string }{
		{methodTasksGet, "tasks/get"},
		{methodTasksUpdate, "tasks/update"},
		{methodTasksCancel, "tasks/cancel"},
		{resultTypeTask, "task"},
		{string(TaskWorking), "working"},
		{string(TaskCompleted), "completed"},
		{string(TaskFailed), "failed"},
		{string(TaskCancelled), "cancelled"},
	} {
		if tc.got != tc.want {
			t.Errorf("token = %q, want the protocol's own spelling %q", tc.got, tc.want)
		}
	}
	// -32021 is the CORE specification's code for a missing client capability.
	// The Tasks extension's own text says -32003, which the core spec places in
	// the legacy sub-range new implementations must not allocate in — so this
	// assertion is the decision, not a transcription.
	if codeMissingClientCapability != -32021 {
		t.Errorf("MissingRequiredClientCapability = %d, want -32021 (the core specification's table); "+
			"-32003 is the extension's stale carry-over from the 2025-11-25 draft", codeMissingClientCapability)
	}
}

// Only an EXACTLY spelled declaration counts. encoding/json matches struct
// members case-insensitively, so decoding capabilities into a struct would read
// `"Extensions"` as this declaration and hand a handle to a client that never
// claimed to understand one — stranding the approved effect behind it.
//
// Everything unreadable fails CLOSED, which is the cheap direction: missing a
// declaration that IS there costs the caller the plain refusal every client
// already handles.
func TestOnlyAnExactlySpelledExtensionDeclarationCounts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities string
		want         bool
	}{
		{"the specification's own spelling", `{"extensions":{"io.modelcontextprotocol/tasks":{}}}`, true},
		{"a mis-cased member", `{"Extensions":{"io.modelcontextprotocol/tasks":{}}}`, false},
		{"a shouted member", `{"EXTENSIONS":{"io.modelcontextprotocol/tasks":{}}}`, false},
		{"a mis-cased extension name", `{"extensions":{"IO.MODELCONTEXTPROTOCOL/TASKS":{}}}`, false},
		{"no extensions at all", `{}`, false},
		{"a null extensions member", `{"extensions":null}`, false},
		{"extensions as a string", `{"extensions":"tasks"}`, false},
		{"the extension declared as null", `{"extensions":{"io.modelcontextprotocol/tasks":null}}`, false},
		{"the extension declared as true", `{"extensions":{"io.modelcontextprotocol/tasks":true}}`, false},
		{"capabilities that do not decode", `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaresTasks(json.RawMessage(tc.capabilities)); got != tc.want {
				t.Errorf("declaresTasks(%s) = %v, want %v", tc.capabilities, got, tc.want)
			}
		})
	}
}

// A staged confirm-first call answers a HANDLE to a client that declared the
// extension, and the same refusal as always to one that did not. The second
// half is the specification's MUST NOT, and it is what keeps every existing
// client — and the certification band, which is one — seeing byte-identical
// behaviour.
func TestOnlyADeclaringClientIsHandedATaskForAStagedCall(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fr       framing
		wantTask bool
	}{
		{"a declaring modern client", framing{modern: true, version: modernProtocolVersion, tasks: true}, true},
		{"a modern client that declared nothing", framing{modern: true, version: modernProtocolVersion}, false},
		{"a handshake-era client", legacyFraming, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := stagingDispatcher(t)
			out := s.call(agentCtx(), json.RawMessage(`{"name":"send_it","arguments":{}}`), tc.fr)
			handle, minted := out.(createTaskResult)
			if minted != tc.wantTask {
				t.Fatalf("answered %T, want task=%v", out, tc.wantTask)
			}
			if !tc.wantTask {
				refusal, ok := out.(map[string]any)
				if !ok || refusal["isError"] != true {
					t.Fatalf("a non-declaring client must get the ordinary refusal, got %v", out)
				}
				if len(store.created) != 0 {
					t.Errorf("a task was created for a client that cannot poll it")
				}
				return
			}
			if handle[fieldStatus] != string(TaskWorking) {
				t.Errorf("status = %v, want working", handle[fieldStatus])
			}
			if handle[fieldPollInterval] != taskPollIntervalMs {
				t.Errorf("pollIntervalMs = %v, want %d", handle[fieldPollInterval], taskPollIntervalMs)
			}
			if handle.resultType() != resultTypeTask {
				t.Errorf("resultType = %q, want %q", handle.resultType(), resultTypeTask)
			}
		})
	}
}

// A refusal that reached no human has nothing to poll. The step-up is the case
// that matters: it stages a question too, but releasing it widens a counter
// rather than performing the call, so a task completing on release would report
// an effect that never happened.
func TestOnlyAStagedConfirmFirstRefusalBecomesATask(t *testing.T) {
	staged := ids.From[ids.ApprovalKind](ids.NewV7())
	for _, tc := range []struct {
		name    string
		refusal error
	}{
		{"a plain permission refusal", apperrors.ErrPermissionDenied},
		{"a quota step-up a human was asked about", &StepUpStagedError{ApprovalID: staged}},
		{"a confirm-first refusal that never staged", apperrors.ErrRequiresApproval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store := stagingDispatcher(t)
			if _, minted := s.mintTask(agentCtx(), taskCapableFraming(), "send_it", tc.refusal); minted {
				t.Fatalf("%v became a task; only a staged confirm-first call may", tc.refusal)
			}
			if len(store.created) != 0 {
				t.Errorf("a task row was created for %v", tc.refusal)
			}
		})
	}
}

// The three methods belong to the extension, so a request that did not declare
// it is asking for a method that — for that caller — does not exist.
func TestTheTaskMethodsRefuseAClientThatDeclaredNoExtension(t *testing.T) {
	s, _ := stagingDispatcher(t)
	for _, method := range []string{methodTasksGet, methodTasksUpdate, methodTasksCancel} {
		resp := s.dispatch(agentCtx(), rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: method,
			Params: json.RawMessage(`{"taskId":"` + ids.NewV7().String() + `"}`),
		}, framing{modern: true, version: modernProtocolVersion})
		if resp.Error == nil || resp.Error.Code != codeMissingClientCapability {
			t.Fatalf("%s answered %+v, want -32021", method, resp.Error)
		}
		if resp.Error.Data == nil || resp.Error.Data.RequiredCapabilities == nil {
			t.Fatalf("%s refusal named no requiredCapabilities, so a client cannot learn the fix", method)
		}
		if _, named := resp.Error.Data.RequiredCapabilities.Extensions[extensionTasks]; !named {
			t.Errorf("%s refusal did not name %q", method, extensionTasks)
		}
	}
}

// In the handshake era the methods genuinely do not exist: the capability that
// admits them is a per-request member that era cannot carry.
func TestTheTaskMethodsDoNotExistInTheHandshakeEra(t *testing.T) {
	s, _ := stagingDispatcher(t)
	for _, method := range []string{methodTasksGet, methodTasksUpdate, methodTasksCancel} {
		resp := s.dispatch(agentCtx(), rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: method,
			Params: json.RawMessage(`{"taskId":"` + ids.NewV7().String() + `"}`),
		}, legacyFraming)
		if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
			t.Errorf("%s in the legacy framing answered %+v, want -32601", method, resp.Error)
		}
	}
}

// A task nobody here minted, and a task id that is not one at all, are the same
// fact from a caller's side: no task is being named. Answering them differently
// would let a caller tell a real id it does not own from a typo.
func TestAnUnknownTaskAndAMalformedOneGetTheSameAnswer(t *testing.T) {
	s, _ := stagingDispatcher(t)
	for _, params := range []string{
		`{"taskId":"` + ids.NewV7().String() + `"}`,
		`{"taskId":"not-a-uuid"}`,
		`{"taskId":""}`,
	} {
		resp := s.dispatch(agentCtx(), rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksGet,
			Params: json.RawMessage(params),
		}, taskCapableFraming())
		if resp.Error == nil || resp.Error.Code != codeInvalidParams {
			t.Errorf("%s answered %+v, want -32602", params, resp.Error)
		}
	}
}

// Cancelling withdraws the proposal. Leaving it in the inbox would leave a
// decision that can no longer take effect and that nobody can retract.
func TestCancellingATaskWithdrawsTheProposalBehindIt(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)

	resp := s.dispatch(agentCtx(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksCancel,
		Params: json.RawMessage(`{"taskId":"` + task.ID.String() + `"}`),
	}, taskCapableFraming())
	if resp.Error != nil {
		t.Fatalf("tasks/cancel answered %+v", resp.Error)
	}
	if store.approvals.withdrawn != task.ApprovalID {
		t.Errorf("withdrew %s, want the task's own approval %s", store.approvals.withdrawn, task.ApprovalID)
	}
	if got := store.tasks[task.ID].Status; got != TaskCancelled {
		t.Errorf("status = %v, want cancelled", got)
	}
}

// Cancellation is cooperative, and a terminal state is immutable. A cancel that
// rewrote a recorded answer would break the one promise a terminal state makes.
func TestCancellingASettledTaskLeavesItsAnswerAlone(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	store.approvals.decision = ApprovalApproved
	pollTask(t, s, task.ID)

	s.dispatch(agentCtx(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksCancel,
		Params: json.RawMessage(`{"taskId":"` + task.ID.String() + `"}`),
	}, taskCapableFraming())

	if got := store.tasks[task.ID].Status; got != TaskCompleted {
		t.Errorf("status = %v, want the completed answer left alone", got)
	}
	if !store.approvals.withdrawn.IsZero() {
		t.Error("cancelling a settled task withdrew an approval that had already been decided")
	}
}

// tasks/update is served because the specification requires the method to
// exist, and it acknowledges emptily because this server raises no
// inputRequests — a confirm-first decision is a person visiting Margince, not a
// round trip back through the agent's client.
func TestUpdatingATaskIsAnEmptyAcknowledgement(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)

	resp := s.dispatch(agentCtx(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksUpdate,
		Params: json.RawMessage(`{"taskId":"` + task.ID.String() + `","inputResponses":{"nobody-asked":{}}}`),
	}, taskCapableFraming())
	if resp.Error != nil {
		t.Fatalf("tasks/update answered %+v", resp.Error)
	}
	body, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshalling the ack: %v", err)
	}
	if string(body) != `{}` {
		t.Errorf("ack = %s, want an empty result", body)
	}
	if got := store.tasks[task.ID].Status; got != TaskWorking {
		t.Errorf("an update moved the task to %v; it decides nothing", got)
	}
}

// The extension is advertised where it can be acted on and nowhere else: an
// extension is negotiated per request, in a member the handshake era has no
// place for, so offering it there would offer a negotiation that era cannot
// enter.
func TestTheExtensionIsAdvertisedToTheEraThatCanDeclareIt(t *testing.T) {
	s, _ := stagingDispatcher(t)
	modern, ok := s.capabilities(true)["extensions"].(map[string]any)
	if !ok {
		t.Fatal("server/discover advertised no extensions, so no client can know to declare one")
	}
	if _, named := modern[extensionTasks]; !named {
		t.Errorf("extensions = %v, want %q", modern, extensionTasks)
	}
	if _, offered := s.capabilities(false)["extensions"]; offered {
		t.Error("initialize advertised an extension the handshake era cannot declare")
	}

	// And not at all without a store behind it: a client that saw it advertised
	// would be entitled to expect a handle.
	bare := NewDispatcher(NewRegistry(nil, auth.NewGate(fullSeatAuthority{})), bindAuthenticated, "margince-crm", "test")
	if _, offered := bare.capabilities(true)["extensions"]; offered {
		t.Error("a server with no task store advertised the extension anyway")
	}
}

// The discriminator belongs to the extension, and the specification forbids it
// anywhere else. Only one type can answer it, which is what makes that true by
// construction rather than by review.
func TestOnlyATaskHandleClaimsTheTaskResultType(t *testing.T) {
	if got := modernResultTypeOf(createTaskResult{}); got != resultTypeTask {
		t.Errorf("a task handle's resultType = %q, want %q", got, resultTypeTask)
	}
	for _, result := range []any{
		map[string]any{"tools": []any{}},
		map[string]json.RawMessage{},
		nil,
	} {
		if got := modernResultTypeOf(result); got != resultTypeComplete {
			t.Errorf("%T claimed resultType %q, want %q", result, got, resultTypeComplete)
		}
	}
}

// The three methods mirror their taskId into Mcp-Name, which the specification
// requires so an intermediary can route a poll without parsing the body.
func TestTheTaskMethodsMirrorTheirTaskID(t *testing.T) {
	id := ids.NewV7().String()
	for _, method := range []string{methodTasksGet, methodTasksUpdate, methodTasksCancel} {
		read, named := modernNamedMethods[method]
		if !named {
			t.Fatalf("%s mirrors no name, so a gateway routing on Mcp-Name is unchecked", method)
		}
		if got := read(json.RawMessage(`{"taskId":"` + id + `"}`)); got != id {
			t.Errorf("%s read %q from its body, want %q", method, got, id)
		}
	}
}

// `ttlMs` means two different things in one JSON namespace — a task's freshness
// in taskWire, a cache hint in finishModern — and they share an object only if
// some method both answers a task AND carries a cache hint. Nothing states that
// they cannot, so this does: the disjointness is a property, not a coincidence
// of today's closed set.
func TestNoMethodCarriesBothATaskAndACacheHint(t *testing.T) {
	for _, method := range []string{methodToolsCall, methodTasksGet, methodTasksUpdate, methodTasksCancel} {
		if _, hinted := modernCacheHint(method); hinted {
			t.Errorf("%s carries a cache hint AND can answer a task; both write %q into one result, "+
				"so one would silently overwrite the other", method, fieldTTLMs)
		}
	}
}

// The wire members are asserted as LITERALS, for the reason this file's first
// test gives about the method names: reading the constant the renderer writes
// proves only that this server agrees with itself, and a typo in one of these
// ships a task no client can read.
func TestTheWireTaskMembersAreSpelledAsTheSpecificationWritesThem(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{fieldTaskID, "taskId"},
		{fieldStatus, "status"},
		{fieldStatusMessage, "statusMessage"},
		{fieldCreatedAt, "createdAt"},
		{fieldLastUpdatedAt, "lastUpdatedAt"},
		{fieldPollInterval, "pollIntervalMs"},
		{fieldTTLMs, "ttlMs"},
		{fieldResult, "result"},
		{fieldError, "error"},
	} {
		if tc.got != tc.want {
			t.Errorf("wire member = %q, want the protocol's own spelling %q", tc.got, tc.want)
		}
	}
	// And they are the members a rendered task actually carries.
	wire := taskWire(Task{Status: TaskWorking, StatusMessage: "waiting"}, time.Now())
	for _, member := range []string{"taskId", "status", "statusMessage", "createdAt", "lastUpdatedAt", "ttlMs"} {
		if _, present := wire[member]; !present {
			t.Errorf("a rendered working task carries no %q", member)
		}
	}
}

// The freshness a handle reports tracks the decision it waits on, and never
// runs backwards past it.
func TestATasksFreshnessTracksTheDecisionItWaitsOn(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	task := Task{Status: TaskWorking, ExpiresAt: now.Add(90 * time.Second), CreatedAt: now, UpdatedAt: now}
	if got := taskWire(task, now)[fieldTTLMs]; got != int64(90_000) {
		t.Errorf("ttlMs = %v, want 90000", got)
	}
	expired := taskWire(Task{Status: TaskWorking, ExpiresAt: now.Add(-time.Hour)}, now)
	if got := expired[fieldTTLMs]; got != int64(0) {
		t.Errorf("ttlMs = %v for a lapsed window, want 0 — a negative freshness is not actionable", got)
	}
}

// A server composed without the task store serves the methods to nobody, and
// says so rather than answering an empty not-found for an id no client holds.
func TestAServerWithNoTaskStoreDoesNotServeTheMethods(t *testing.T) {
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	s := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
	resp := s.dispatch(agentCtx(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksGet,
		Params: json.RawMessage(`{"taskId":"` + ids.NewV7().String() + `"}`),
	}, taskCapableFraming())
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("answered %+v, want -32601", resp.Error)
	}
}

// ---- harness ----
// stagingTool is a confirm-first tool the gate always sends to a human, and
// which records the call it is eventually asked to perform.
type stagingTool struct {
	calls    int
	lastArgs json.RawMessage
	// target is the record the tool stages against and later names as the
	// evidence for its answer — one record, so the two agree.
	target ids.UUID
	// silent makes it answer WITHOUT naming a record, the way send_email does:
	// an activity id and a status, no record read through the seam.
	silent bool
}

func (t *stagingTool) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "send_it", Title: "send_it", Version: testToolVersion,
		Description:   "send_it does the thing the test needs it to do.",
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierConfirmationRequired,
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func (t *stagingTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	t.calls++
	t.lastArgs = in
	if t.silent {
		return json.RawMessage(`{"ok":true}`), nil
	}
	// It NAMES the record it acted on, as every mutating tool on this surface
	// does. That evidence is what a later poll re-proves the caller may still
	// see — a fake that skipped it would exercise a document the replay gate
	// refuses, and would prove nothing about the path a real tool takes.
	noteRecord(ctx, datasource.Record{
		Ref:       datasource.EntityRef{Type: datasource.EntityDeal, ID: t.target},
		Freshness: datasource.FreshnessInfo{Authoritative: true},
	})
	return json.RawMessage(`{"ok":true}`), nil
}

// StageInfo makes the tool stageable, which is what turns its refusal into
// something a handle can point at.
func (t *stagingTool) StageInfo(context.Context, json.RawMessage) (StageInfo, error) {
	return StageInfo{TargetType: "deal", TargetID: t.target, Summary: "send it"}, nil
}

// fakeApprovals is the staging, polling and redemption seam in one object, so a
// test can move the human's decision and watch what the poll does about it.
type fakeApprovals struct {
	mu          sync.Mutex
	staged      ids.ApprovalID
	decision    ApprovalDecision
	change      json.RawMessage
	consumed    bool
	stateErr    error
	changeErr   error
	withdrawErr error
	withdrawn   ids.ApprovalID
	redeemed    []ids.ApprovalID
}

func (a *fakeApprovals) Stage(_ context.Context, in StageRequest) (ids.ApprovalID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.staged = ids.From[ids.ApprovalKind](ids.NewV7())
	if a.change == nil {
		a.change = in.ProposedChange
	}
	return a.staged, nil
}

func (a *fakeApprovals) StageQuotaRelease(context.Context, QuotaReleaseRequest) (ids.ApprovalID, bool, error) {
	return ids.ApprovalID{}, false, errors.New("no step-up in this fixture")
}

func (a *fakeApprovals) Redeem(_ context.Context, id ids.ApprovalID, _, _ string) (int64, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.redeemed = append(a.redeemed, id)
	return 0, false, nil
}

func (a *fakeApprovals) State(context.Context, ids.ApprovalID) (ApprovalState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stateErr != nil {
		return ApprovalState{}, a.stateErr
	}
	decision := a.decision
	if decision == "" {
		decision = ApprovalPending
	}
	return ApprovalState{Decided: decision, Consumed: a.consumed, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (a *fakeApprovals) ProposedChange(context.Context, ids.ApprovalID) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.changeErr != nil {
		return nil, a.changeErr
	}
	return a.change, nil
}

func (a *fakeApprovals) Withdraw(_ context.Context, id ids.ApprovalID) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.withdrawErr != nil {
		return false, a.withdrawErr
	}
	a.withdrawn = id
	// A withdrawal only ever takes a PENDING proposal off the inbox, exactly as
	// WithdrawInTx does — so a decided one reports that nothing was retracted.
	return a.decision == "" || a.decision == ApprovalPending, nil
}

// fakeTasks is an in-memory task store with the ONE behaviour the durable one
// must have: a claim is won by exactly one caller, and a terminal state is
// never rewritten.
type fakeTasks struct {
	mu        sync.Mutex
	tasks     map[ids.UUID]Task
	created   []NewTask
	claimed   map[ids.UUID]bool
	reclaimed bool
	createErr error
	settleErr error
	cancelErr error
	loadErr   error
	approvals *fakeApprovals
	tool      *stagingTool
	records   *recordReader
	quota     *fakeQuota
}

// fakeQuota is a meter a test can push past a ceiling. It records nothing:
// what these cases turn on is whether a reading REFUSES, not what it counts.
type fakeQuota struct {
	mu       sync.Mutex
	exceeded bool
}

func (q *fakeQuota) exceed() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.exceeded = true
}

func (q *fakeQuota) Read(_ context.Context, c agentquota.Counter) agentquota.Reading {
	q.mu.Lock()
	defer q.mu.Unlock()
	return agentquota.Reading{Counter: c, Exceeded: q.exceeded}
}

func (q *fakeQuota) Consume(context.Context, agentquota.Counter, int) error { return nil }

// recordReader is the live re-read a recorded answer is proven against. Taking
// a record out of `visible` is how a test narrows the caller's access after the
// answer was produced.
type recordReader struct {
	mu      sync.Mutex
	visible map[ids.UUID]bool
}

func (r *recordReader) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.visible[ref.ID] {
		return datasource.Record{}, apperrors.ErrNotFound
	}
	return datasource.Record{Ref: ref, Freshness: datasource.FreshnessInfo{Authoritative: true}}, nil
}

func (r *recordReader) hide(id ids.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.visible, id)
}

func (f *fakeTasks) Create(_ context.Context, in NewTask) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return Task{}, f.createErr
	}
	f.created = append(f.created, in)
	task := Task{
		ID: ids.NewV7(), ApprovalID: in.ApprovalID, Tool: in.Tool, Status: TaskWorking,
		StatusMessage: in.StatusMessage, ExpiresAt: in.ExpiresAt,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.tasks[task.ID] = task
	return task, nil
}

func (f *fakeTasks) Load(_ context.Context, id ids.UUID) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return Task{}, f.loadErr
	}
	task, ok := f.tasks[id]
	if !ok {
		return Task{}, apperrors.ErrNotFound
	}
	return task, nil
}

func (f *fakeTasks) Claim(_ context.Context, id ids.UUID, _ time.Duration) (TaskClaim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed[id] || f.tasks[id].Status != TaskWorking {
		return TaskClaim{}, nil
	}
	f.claimed[id] = true
	return TaskClaim{Won: true, Reclaimed: f.reclaimed}, nil
}

func (f *fakeTasks) Settle(_ context.Context, id ids.UUID, s Settlement) (Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.settleErr != nil {
		return Task{}, f.settleErr
	}
	task, ok := f.tasks[id]
	if !ok {
		return Task{}, apperrors.ErrNotFound
	}
	if task.Status.terminal() {
		return task, nil
	}
	task.Status, task.StatusMessage = s.Status, s.StatusMessage
	task.Result, task.Error = s.Result, s.Error
	task.ServedRecords = s.ServedRecords
	task.UpdatedAt = time.Now()
	f.tasks[id] = task
	return task, nil
}

// Cancel mirrors the store's one guard: a claimed task is executing, and
// cancelling it would discard that execution's own settlement.
func (f *fakeTasks) Cancel(_ context.Context, id ids.UUID, lease time.Duration, message string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelErr != nil {
		return false, f.cancelErr
	}
	task, ok := f.tasks[id]
	// The store reads a claim as live only while it is younger than the lease;
	// this fixture has no clock, so a claim it holds is always live. The stale
	// arm is the integration lane's to prove, against the real predicate.
	_ = lease
	if !ok || task.Status != TaskWorking || f.claimed[id] {
		return false, nil
	}
	task.Status, task.StatusMessage = TaskCancelled, message
	task.UpdatedAt = time.Now()
	f.tasks[id] = task
	return true, nil
}

// stagingDispatcher is a server whose one tool always needs a human, wired to
// the fakes above.
func stagingDispatcher(t *testing.T) (*Dispatcher, *fakeTasks) {
	t.Helper()
	approvals := &fakeApprovals{}
	tool := &stagingTool{target: ids.NewV7()}
	store := &fakeTasks{
		tasks: map[ids.UUID]Task{}, claimed: map[ids.UUID]bool{},
		approvals: approvals, tool: tool,
	}
	// The replay reader is what a later poll re-proves the recorded answer
	// through. Without one every recorded document is withheld — fail-closed,
	// and the reason a composition that forgets it loses replay rather than its
	// gate.
	reader := &recordReader{visible: map[ids.UUID]bool{tool.target: true}}
	quota := &fakeQuota{}
	registry := NewRegistry(approvals, auth.NewGate(fullSeatAuthority{}, auth.WithQuota(quota)),
		WithReplayReader(reader), WithQuotaCharger(quota))
	registry.Register(tool)
	s := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").
		WithLogger(discardLog()).WithTasks(store, approvals)
	store.records, store.quota = reader, quota
	return s, store
}

func taskCapableFraming() framing {
	return framing{modern: true, version: modernProtocolVersion, tasks: true}
}

// mintOne stages a call and returns the handle's task.
func mintOne(t *testing.T, s *Dispatcher, store *fakeTasks) Task {
	t.Helper()
	out := s.call(agentCtx(), json.RawMessage(`{"name":"send_it","arguments":{"body":"what the agent proposed"}}`),
		taskCapableFraming())
	handle, minted := out.(createTaskResult)
	if !minted {
		t.Fatalf("the staged call answered %T, not a task handle", out)
	}
	id, err := ids.Parse(handle[fieldTaskID].(string))
	if err != nil {
		t.Fatalf("the handle's taskId did not parse: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.tasks[id]
}

// pollTask runs one tasks/get and returns the rendered task.
func pollTask(t *testing.T, s *Dispatcher, id ids.UUID) map[string]any {
	t.Helper()
	resp := s.dispatch(agentCtx(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksGet,
		Params: json.RawMessage(`{"taskId":"` + id.String() + `"}`),
	}, taskCapableFraming())
	if resp.Error != nil {
		t.Fatalf("tasks/get answered %d %q", resp.Error.Code, resp.Error.Message)
	}
	wire, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("tasks/get answered %T, not a task", resp.Result)
	}
	return wire
}

// cancelTaskCall runs one tasks/cancel and returns the whole response, because
// these cases are about the ERROR half.
func cancelTaskCall(t *testing.T, s *Dispatcher, id ids.UUID) rpcResponse {
	t.Helper()
	return s.dispatch(agentCtx(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodTasksCancel,
		Params: json.RawMessage(`{"taskId":"` + id.String() + `"}`),
	}, taskCapableFraming())
}

// isToolError reports whether a stored tool result refused.
func isToolError(t *testing.T, result json.RawMessage) bool {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("the stored result did not decode: %v", err)
	}
	return decoded["isError"] == true
}

// The staged approval id travels from the refusal into the handle, which is the
// binding every later poll resolves.
func TestAHandlePointsAtTheApprovalItsCallStaged(t *testing.T) {
	s, store := stagingDispatcher(t)
	task := mintOne(t, s, store)
	if task.ApprovalID != store.approvals.staged {
		t.Errorf("the handle points at %s, want the staged approval %s", task.ApprovalID, store.approvals.staged)
	}
}

// A staged refusal does not always arrive bare. Invoke wraps, and the whole
// reason mintTask uses errors.As rather than a type switch is to see through
// that — a wrapped refusal that stopped minting would put every 🟡 call back on
// the dead end this track exists to close.
func TestAWrappedStagedRefusalStillMintsAHandle(t *testing.T) {
	s, store := stagingDispatcher(t)
	staged := ids.From[ids.ApprovalKind](ids.NewV7())
	wrapped := fmt.Errorf("the gate refused it: %w", &workflow.StagedApprovalError{ApprovalID: staged})

	handle, minted := s.mintTask(agentCtx(), taskCapableFraming(), "send_it", wrapped)

	if !minted {
		t.Fatal("a wrapped staged refusal minted no handle")
	}
	if handle[fieldStatus] != string(TaskWorking) {
		t.Errorf("status = %v, want working", handle[fieldStatus])
	}
	if len(store.created) != 1 || store.created[0].ApprovalID != staged {
		t.Errorf("the handle points at %v, want the wrapped refusal's own approval %s", store.created, staged)
	}
}

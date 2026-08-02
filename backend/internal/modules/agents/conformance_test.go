// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the MCP specification obliges this server to put on the wire, as
// opposed to what the tools mean. Three obligations live here:
//
//   - a tool that advertises an outputSchema MUST answer with structured
//     content conforming to it, and SHOULD keep serializing the same JSON into
//     a text block for clients that ignore structured content;
//   - initialize may claim only capabilities this server can actually deliver;
//   - tools/list carries a display title and the annotation hints, so tier and
//     reach are readable structurally rather than only as English prose in the
//     description.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// echoTool answers with whatever bytes it was built with, so a test can put an
// exact result on the dispatcher's return path.
type echoTool struct {
	spec mcp.ToolSpec
	out  json.RawMessage
}

func (e echoTool) Spec() mcp.ToolSpec { return e.spec }
func (e echoTool) Handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return e.out, nil
}

func objectSpec(name string, scope principal.Scope) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: name, Title: name, RequiredScope: scope, Tier: mcp.TierAutoExecute,
		InputSchema:  json.RawMessage(`{"type":"object"}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

// dispatchWith builds a dispatcher over one tool, with its log captured so a
// test can assert on what the operator was told. The gate is real: a call has
// to be genuinely admitted to reach the rendering these tests are about.
func dispatchWith(t *testing.T, tool mcp.Tool, log *strings.Builder) *Dispatcher {
	t.Helper()
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	registry.Register(tool)
	return NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").
		WithLogger(slog.New(slog.NewTextHandler(log, nil)))
}

// scopedAgentCtx is one authenticated agent carrying exactly scopes — the
// caller every rendering test here dispatches as. (precedence_test.go's
// argument-less agentCtx is fixed at the write scope.)
func scopedAgentCtx(scopes ...principal.Scope) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:conformance", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(scopes...),
	})
}

func callResult(t *testing.T, s *Dispatcher, name string) map[string]any {
	t.Helper()
	out := s.call(scopedAgentCtx(principal.ScopeRead), json.RawMessage(`{"name":"`+name+`","arguments":{}}`))
	if out["isError"] == true {
		t.Fatalf("%s returned an in-band error: %v", name, out)
	}
	return out
}

// textBlock returns the serialized JSON the result's TextContent carries.
func textBlock(t *testing.T, res map[string]any) string {
	t.Helper()
	content, ok := res["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want exactly one block", res["content"])
	}
	text, ok := content[0][fieldText].(string)
	if !ok {
		t.Fatalf("content block carries no text: %#v", content[0])
	}
	return text
}

// The MUST: a declared outputSchema obliges structured results. The text block
// stays beside it, so a client that predates structured content is not served
// an empty answer.
func TestToolsCallReturnsStructuredContentBesideTheTextBlock(t *testing.T) {
	const out = `{"record_type":"deal","version":9007199254740993,"name":"Acme"}`
	var log strings.Builder
	s := dispatchWith(t, echoTool{spec: objectSpec("read_record", principal.ScopeRead), out: json.RawMessage(out)}, &log)

	res := callResult(t, s, "read_record")

	structured, ok := res["structuredContent"]
	if !ok {
		t.Fatalf("no structuredContent on a result whose tool declares an outputSchema: %#v", res)
	}
	// Byte-identical, not merely equivalent. The two members are one answer in
	// two renderings and a client may compare them, so a round trip through
	// map[string]any — which would widen this version past float64's exact
	// integer range and reorder the keys — is a real divergence, not a nicety.
	raw, ok := structured.(json.RawMessage)
	if !ok {
		t.Fatalf("structuredContent is %T, want the handler's own json.RawMessage", structured)
	}
	if string(raw) != out {
		t.Errorf("structuredContent = %s, want the handler's bytes unchanged %s", raw, out)
	}
	if got := textBlock(t, res); got != out {
		t.Errorf("text block = %s, want the same serialized JSON %s", got, out)
	}
}

// A tool that declares an object schema and then answers with something else
// is this server's defect. The caller still gets the answer it can read, the
// operator is told, and the member that would violate the advertised schema is
// left off rather than sent.
func TestNonObjectToolOutputIsReportedAndOmittedFromStructuredContent(t *testing.T) {
	for _, tc := range []struct{ name, out, wantLogged string }{
		{"answers_null", `null`, "returned JSON null"},
		{"answers_array", `[{"id":1}]`, "did not return a JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log strings.Builder
			s := dispatchWith(t, echoTool{spec: objectSpec(tc.name, principal.ScopeRead), out: json.RawMessage(tc.out)}, &log)

			res := callResult(t, s, tc.name)

			if _, present := res["structuredContent"]; present {
				t.Errorf("structuredContent present for output %s — it cannot conform to the advertised object schema", tc.out)
			}
			if got := textBlock(t, res); got != tc.out {
				t.Errorf("text block = %s, want the handler's answer %s — the caller still gets what it can read", got, tc.out)
			}
			if !strings.Contains(log.String(), tc.wantLogged) {
				t.Errorf("operator log = %q, want it to name the defect (%q)", log.String(), tc.wantLogged)
			}
		})
	}
}

// listChanged advertises a notification that travels on the GET SSE stream.
// GET /mcp answers 405 here, so claiming it would promise a message no client
// can ever receive.
func TestInitializeDoesNotClaimAListChangedItCannotSend(t *testing.T) {
	s := NewDispatcher(NewRegistry(nil, nil), bindAuthenticated, "margince-crm", "test")

	resp := s.handle(context.Background(), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodInitialize,
	})

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	capabilities, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %#v", result["capabilities"])
	}
	tools, ok := capabilities["tools"].(map[string]any)
	if !ok {
		t.Fatalf("tools capability = %#v", capabilities["tools"])
	}
	if tools["listChanged"] != false {
		t.Errorf("listChanged = %v, want false — no GET stream exists to send notifications/tools/list_changed on",
			tools["listChanged"])
	}
}

// Tier and scope are prose inside `description`, which no client can render
// structurally. The annotations carry the two facts the server can state from
// the spec the gate itself enforces.
func TestToolListCarriesTitleAndDerivedAnnotations(t *testing.T) {
	egress := objectSpec("send_email", principal.ScopeSend)
	egress.Title, egress.Tier, egress.Egress = "Send an email", mcp.TierConfirmationRequired, true
	read := objectSpec("read_record", principal.ScopeRead)
	read.Title = "Read a record"

	registry := NewRegistry(nil, nil)
	registry.Register(echoTool{spec: read, out: json.RawMessage(`{}`)})
	registry.Register(echoTool{spec: egress, out: json.RawMessage(`{}`)})
	s := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test")

	ctx := scopedAgentCtx(principal.ScopeRead, principal.ScopeSend)

	listed := map[string]map[string]any{}
	for _, tool := range s.toolList(ctx) {
		name, _ := tool[fieldName].(string)
		listed[name] = tool
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d tools, want 2: %#v", len(listed), listed)
	}

	for _, tc := range []struct {
		tool          string
		wantTitle     string
		wantReadOnly  bool
		wantOpenWorld bool
	}{
		{"read_record", "Read a record", true, false},
		{"send_email", "Send an email", false, true},
	} {
		tool := listed[tc.tool]
		if tool["title"] != tc.wantTitle {
			t.Errorf("%s title = %v, want %q", tc.tool, tool["title"], tc.wantTitle)
		}
		annotations, ok := tool["annotations"].(map[string]any)
		if !ok {
			t.Fatalf("%s annotations = %#v", tc.tool, tool["annotations"])
		}
		if annotations["title"] != tc.wantTitle {
			t.Errorf("%s annotations.title = %v, want %q", tc.tool, annotations["title"], tc.wantTitle)
		}
		if annotations["readOnlyHint"] != tc.wantReadOnly {
			t.Errorf("%s readOnlyHint = %v, want %v — it is derived from the required scope",
				tc.tool, annotations["readOnlyHint"], tc.wantReadOnly)
		}
		if annotations["openWorldHint"] != tc.wantOpenWorld {
			t.Errorf("%s openWorldHint = %v, want %v — it is derived from the egress flag",
				tc.tool, annotations["openWorldHint"], tc.wantOpenWorld)
		}
		// The two hints this server does not state. Their protocol defaults
		// (destructive, non-idempotent) are the conservative reading already,
		// and nothing here could hold a looser per-tool claim true.
		for _, unstated := range []string{"destructiveHint", "idempotentHint"} {
			if _, present := annotations[unstated]; present {
				t.Errorf("%s advertises %s; this server states neither hint", tc.tool, unstated)
			}
		}
	}
}

// ReadOnly is derived from the scope the admission gate enforces, so the hint
// and the authority cannot disagree.
func TestReadOnlyIsDerivedFromTheEnforcedScope(t *testing.T) {
	for scope, want := range map[principal.Scope]bool{
		principal.ScopeRead:   true,
		principal.ScopeDraft:  true,
		principal.ScopeWrite:  false,
		principal.ScopeSend:   false,
		principal.ScopeEnrich: false,
	} {
		if got := (mcp.ToolSpec{RequiredScope: scope}).ReadOnly(); got != want {
			t.Errorf("scope %q ReadOnly() = %v, want %v", scope, got, want)
		}
	}
}

// Registration is where a spec defect has to stop: past it, the defect is a
// served response. A title-less tool would render its identifier as its
// display name, and a non-object output schema is a promise tools/call cannot
// keep, because structuredContent is typed as an object.
func TestRegisterRefusesWireDefects(t *testing.T) {
	mustPanic(t, "a title-less tool has no display name but the one it was trying to improve on", func() {
		NewRegistry(nil, nil).Register(echoTool{spec: mcp.ToolSpec{Name: "untitled", Tier: mcp.TierAutoExecute}})
	})
	mustPanic(t, "an array output schema can never be answered with structuredContent", func() {
		spec := objectSpec("lists_things", principal.ScopeRead)
		spec.OutputSchema = json.RawMessage(`{"type":"array"}`)
		NewRegistry(nil, nil).Register(echoTool{spec: spec})
	})
	mustPanic(t, "an unparseable output schema is advertised verbatim to every client", func() {
		spec := objectSpec("broken_schema", principal.ScopeRead)
		spec.OutputSchema = json.RawMessage(`{"type":`)
		NewRegistry(nil, nil).Register(echoTool{spec: spec})
	})
}

// A tool declaring no output schema promises nothing, so tools/call owes it no
// structured content — and must not invent a claim the listing never made.
func TestAToolWithNoOutputSchemaGetsNoStructuredContent(t *testing.T) {
	spec := objectSpec("no_schema", principal.ScopeRead)
	spec.OutputSchema = nil
	var log strings.Builder
	s := dispatchWith(t, echoTool{spec: spec, out: json.RawMessage(`{"ok":true}`)}, &log)

	res := callResult(t, s, "no_schema")

	if _, present := res["structuredContent"]; present {
		t.Error("structuredContent present for a tool that advertises no outputSchema")
	}
	if log.Len() != 0 {
		t.Errorf("operator log = %q, want silence — declaring no schema is a choice, not a defect", log.String())
	}
}

// assertObjectSchema is what Register enforces; the error has to name the tool
// and the offending type, because a boot panic is read without a debugger.
func TestAssertObjectSchemaNamesTheToolAndTheType(t *testing.T) {
	spec := objectSpec("run_report", principal.ScopeRead)
	spec.OutputSchema = json.RawMessage(`{"type":"string"}`)

	err := assertObjectSchema(spec)

	if err == nil {
		t.Fatal("assertObjectSchema accepted a string output schema")
	}
	for _, want := range []string{"run_report", "string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if err := assertObjectSchema(objectSpec("read_record", principal.ScopeRead)); err != nil {
		t.Errorf("assertObjectSchema rejected an object schema: %v", err)
	}
	if err := assertObjectSchema(mcp.ToolSpec{Name: "no_schema"}); err != nil {
		t.Errorf("assertObjectSchema rejected an absent schema: %v", err)
	}
}

// The registered surface is the universe this walks: every tool the product
// ships has to carry a title, and Register is what makes that true for tools
// registered anywhere else too — including an extension's.
func TestEveryCoreToolCarriesADisplayTitle(t *testing.T) {
	registry := NewRegistry(nil, nil)
	RegisterCoreTools(registry, nil, nil, nil, nil)
	RegisterReportTool(registry, nil)
	RegisterCommsTools(registry, &recordingComms{}, nil)

	specs := registry.Specs()
	if len(specs) == 0 {
		t.Fatal("no tools registered — this walk would pass vacuously")
	}
	for _, spec := range specs {
		if strings.TrimSpace(spec.Title) == "" {
			t.Errorf("tool %q has no display title", spec.Name)
		}
		if spec.Title == spec.Name {
			t.Errorf("tool %q titles itself with its own identifier, which is what a client falls back to anyway", spec.Name)
		}
	}
}

// A refusal is not a result: it carries the agent's remedy as prose and no
// structured content, because there is no tool output to structure.
func TestAnInBandToolErrorCarriesNoStructuredContent(t *testing.T) {
	s := NewDispatcher(NewRegistry(nil, nil), bindAuthenticated, "margince-crm", "test").
		WithLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))

	res := s.call(scopedAgentCtx(principal.ScopeRead), json.RawMessage(`{"name":"no_such_tool","arguments":{}}`))

	if res["isError"] != true {
		t.Fatalf("unknown tool did not produce an in-band error: %v", res)
	}
	if _, present := res["structuredContent"]; present {
		t.Error("structuredContent present on a failed call")
	}
}

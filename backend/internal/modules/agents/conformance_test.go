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
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
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

// describedForRegistration is the stand-in description a fake tool carries so
// registration admits it at all. Register refuses a description-less tool, and
// every fake in this package is registered to exercise something else.
const describedForRegistration = "A stand-in tool, offered so the registry has something to admit."

// testToolVersion is the stand-in result-contract version a fake tool carries,
// for the same reason describedForRegistration exists: Register refuses a
// version-less tool, because every result it seals reports one as
// `schema_version`.
const testToolVersion = "1.0.0-test"

func objectSpec(name string, scope principal.Scope) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: name, Title: name, Version: testToolVersion, Description: name + " does the thing the test needs it to do.",
		RequiredScope: scope, Tier: mcp.TierAutoExecute,
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

// inertRetriever grounds the intent tools. Nothing here calls it — these walks
// read Specs() — but the registrars refuse a nil seam, which is the point: a
// surface that cannot ground its answer registers no tool, so a nil would
// silently shrink the very set these walks claim to cover.
type inertRetriever struct{}

func (inertRetriever) Search(context.Context, retrieval.Query) ([]retrieval.Hit, error) {
	return nil, nil
}

func (inertRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, nil
}

// fullRegistry builds EVERY tool the product ships, through all seven
// registrars.
//
// Building only some of them is how a walk lies: three of the seven families
// (network, slipping, intents) carry their own hand-written JSON schema
// literals, and a walk that skips them certifies eight tools it never looked
// at — which is exactly the failure TestTheWholeToolListEncodes exists to
// catch. Every seam here is non-nil for the same reason: each registrar
// registers nothing when its seam is absent, so a nil would shrink the set
// silently rather than fail.
func fullRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	RegisterCoreTools(r, nil, nil, nil, nil)
	RegisterPipelineTool(r, func(context.Context) ([]Pipeline, error) { return nil, nil })
	RegisterReportTool(r, nil, probeReportCatalog)
	RegisterIntentTools(r, inertRetriever{})
	RegisterSlippingTools(r,
		func(context.Context) ([]SlippingDeal, error) { return nil, nil },
		func(context.Context, SlippingDeal) (ids.UUID, string, error) { return ids.UUID{}, "", nil })
	RegisterNetworkTools(r,
		func(context.Context, ids.UUID) ([]KnownColleague, bool, error) { return nil, false, nil },
		func(context.Context, ids.UUID) (DealCoverageAnswer, error) { return DealCoverageAnswer{}, nil },
		func(context.Context, ids.UUID) ([]IntroRoute, bool, error) { return nil, false, nil },
		func(context.Context) (AtRiskReport, error) { return AtRiskReport{}, nil })
	RegisterCommsTools(r, &recordingComms{}, &multiLinkProvider{})
	RegisterLifecycleTools(r, nil, inertLifecycle{}, inertLifecycle{}, inertLifecycle{})
	RegisterEnrichTool(r, nil, inertLifecycle{})
	RegisterQueryTool(r, nil, func(context.Context, json.RawMessage) (QueryAnswer, error) {
		return QueryAnswer{Coverage: CoverageCompleteExact}, nil
	})
	return r
}

// inertLifecycle satisfies the three lifecycle seams and the enrich seam for the
// walks that only need a tool's SPEC and argument shape: they never reach a
// handler, so a seam that answers nothing is the honest stand-in.
type inertLifecycle struct{}

func (inertLifecycle) RelinkActivity(context.Context, ids.UUID, string, ids.UUID, bool) (json.RawMessage, error) {
	return nil, nil
}

func (inertLifecycle) DisqualifyLead(context.Context, ids.UUID) (json.RawMessage, error) {
	return nil, nil
}

func (inertLifecycle) AdvanceProjectPhase(context.Context, ids.UUID, string, *string, *int64) (json.RawMessage, error) {
	return nil, nil
}

func (inertLifecycle) EnrichCompany(context.Context, ids.UUID, string, string) (json.RawMessage, error) {
	return nil, nil
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
	// The handler's bytes ride inside the envelope every result now carries, and
	// they ride there UNCHANGED — that is what makes structuredContent and the
	// text block comparable, and it is why this reads the payload rather than
	// re-encoding a decoded copy of it.
	if got := string(payloadOf(t, raw)); got != out {
		t.Errorf("structuredContent payload = %s, want the handler's bytes unchanged %s", got, out)
	}
	if got := textBlock(t, res); got != string(raw) {
		t.Errorf("text block = %s, want the same serialized result %s", got, raw)
	}
}

// A tool that declares an object shape and then answers with something else is
// this server's defect. The caller still gets the answer it can read, the
// operator is told which member parted company with the schema, and the member
// that would violate the advertised schema is left off rather than sent.
func TestNonObjectToolOutputIsReportedAndOmittedFromStructuredContent(t *testing.T) {
	for _, tc := range []struct{ name, out, wantLogged string }{
		{"answers_null", `null`, "is null, which is not a value of the declared type"},
		{"answers_array", `[{"id":1}]`, "result.data: declared an object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log strings.Builder
			s := dispatchWith(t, echoTool{spec: objectSpec(tc.name, principal.ScopeRead), out: json.RawMessage(tc.out)}, &log)

			res := callResult(t, s, tc.name)

			if _, present := res["structuredContent"]; present {
				t.Errorf("structuredContent present for output %s — it cannot conform to the advertised object schema", tc.out)
			}
			// The caller still gets what it can read: the envelope is well formed
			// whatever the handler put inside it, so the defect is confined to
			// the payload rather than costing the answer its whole rendering.
			if got := string(payloadOf(t, json.RawMessage(textBlock(t, res)))); got != tc.out {
				t.Errorf("text block payload = %s, want the handler's answer %s — the caller still gets what it can read", got, tc.out)
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
// and the authority cannot disagree — and it claims read-only ONLY where the
// scope proves it, because a tool that writes and says it does not is a lie a
// client acts on.
func TestReadOnlyIsDerivedFromTheEnforcedScope(t *testing.T) {
	for scope, want := range map[principal.Scope]bool{
		principal.ScopeRead: true,
		// Draft is NOT read-only. The scope covers draft_email, which returns
		// a proposal, AND draft_follow_ups_for, which persists a draft
		// activity — so it cannot answer the question, and only the
		// conservative half is honest.
		principal.ScopeDraft:  false,
		principal.ScopeWrite:  false,
		principal.ScopeSend:   false,
		principal.ScopeEnrich: false,
	} {
		if got := (mcp.ToolSpec{RequiredScope: scope}).ReadOnly(); got != want {
			t.Errorf("scope %q ReadOnly() = %v, want %v", scope, got, want)
		}
	}
}

// A 🟡 tool is completed by re-presenting the same call with the approval a
// human released, so `approval_id` is not an optional nicety on those tools: it
// is the only way to finish one. A schema that omits it while forbidding
// additional properties tells a validating client the argument does not exist —
// and this surface is documentation for exactly such clients, and for models
// reading it as one. Derived from the registered set, so a new confirm-first
// tool is enrolled the day it is written.
func TestEveryConfirmFirstToolAdvertisesItsApprovalArgument(t *testing.T) {
	checked := 0
	for _, spec := range fullRegistry(t).Specs() {
		if spec.Tier != mcp.TierConfirmationRequired {
			continue
		}
		checked++
		// Read as a raw object: JSON Schema's keys are the protocol's
		// (`additionalProperties`), not this codebase's snake_case.
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(spec.InputSchema, &doc); err != nil {
			t.Errorf("%s: input schema is not readable: %v", spec.Name, err)
			continue
		}
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(doc["properties"], &properties); err != nil {
			t.Errorf("%s: input schema declares no readable properties: %v", spec.Name, err)
			continue
		}
		if _, declared := properties["approval_id"]; declared {
			continue
		}
		// An open schema still lets a client send the argument; a closed one
		// does not, so only the closed case is a broken redemption path.
		if closed := string(doc["additionalProperties"]) == "false"; closed {
			t.Errorf("%s is confirm-first and forbids additional properties, but advertises no "+
				"approval_id — a client validating against this surface cannot redeem an approval "+
				"a human already granted", spec.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no confirm-first tool resolved — this gate asserted nothing")
	}
}

// Registration is where a spec defect has to stop: past it, the defect is a
// served response. A title-less tool would render its identifier as its
// display name, a description-less one leaves a client with nothing to choose
// it on but that identifier, and a non-object output schema is a promise
// tools/call cannot keep, because structuredContent is typed as an object.
func TestRegisterRefusesWireDefects(t *testing.T) {
	mustPanic(t, "a title-less tool has no display name but the one it was trying to improve on", func() {
		NewRegistry(nil, nil).Register(echoTool{spec: mcp.ToolSpec{Name: "untitled", Tier: mcp.TierAutoExecute}})
	})
	mustPanic(t, "a description-less tool can only be selected by the shape of its name", func() {
		spec := objectSpec("undescribed", principal.ScopeRead)
		spec.Description = "  "
		NewRegistry(nil, nil).Register(echoTool{spec: spec})
	})
	mustPanic(t, "a runaway description is spent out of every run's prompt, which never elides it", func() {
		spec := objectSpec("verbose", principal.ScopeRead)
		spec.Description = strings.Repeat("a", maxDescriptionRunes+1)
		NewRegistry(nil, nil).Register(echoTool{spec: spec})
	})
	// The bound has to admit what the surface actually ships, or it is a rule
	// against writing the descriptions this change exists to write.
	longest := 0
	for _, spec := range fullRegistry(t).Specs() {
		if n := len([]rune(spec.Description)); n > longest {
			longest = n
		}
	}
	if longest >= maxDescriptionRunes {
		t.Errorf("the longest shipped description is %d runes against a %d ceiling — the bound is "+
			"refusing prose this surface already writes", longest, maxDescriptionRunes)
	}
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

// assertObjectSchemas is what Register enforces; the error has to name the
// tool and the offending type, because a boot panic is read without a
// debugger.
func TestAssertObjectSchemasNamesTheToolAndTheType(t *testing.T) {
	spec := objectSpec("run_report", principal.ScopeRead)
	spec.OutputSchema = json.RawMessage(`{"type":"string"}`)

	err := assertObjectSchemas(spec)

	if err == nil {
		t.Fatal("assertObjectSchemas accepted a string output schema")
	}
	for _, want := range []string{"run_report", "string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if err := assertObjectSchemas(objectSpec("read_record", principal.ScopeRead)); err != nil {
		t.Errorf("assertObjectSchemas rejected an object schema: %v", err)
	}
	// An absent OUTPUT schema is a choice — the tool promises no shape, so
	// tools/call owes it no structured content. An absent INPUT schema is not:
	// the protocol requires one.
	noOutput := objectSpec("no_output", principal.ScopeRead)
	noOutput.OutputSchema = nil
	if err := assertObjectSchemas(noOutput); err != nil {
		t.Errorf("assertObjectSchemas rejected an absent OutputSchema: %v", err)
	}
	if err := assertObjectSchemas(mcp.ToolSpec{Name: "no_input"}); err == nil {
		t.Error("assertObjectSchemas accepted a tool with no InputSchema; MCP requires one")
	}
}

// The registered surface is the universe this walks: every tool the product
// ships has to carry a title, and Register is what makes that true for tools
// registered anywhere else too — including an extension's.
func TestEveryCoreToolCarriesADisplayTitle(t *testing.T) {
	specs := fullRegistry(t).Specs()
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

// The whole tools/list response has to encode, and the schemas are the part of
// it that can stop it: they are hand-written JSON literals spliced from
// constants and embedded verbatim, so one misplaced brace in one tool takes
// every tool down behind a 500 rather than breaking its own entry.
//
// This walks the registered surface and marshals the real response, because
// that is the failure — a live tools/list answering 500 while every unit test
// was green, which is how it was actually found.
func TestTheWholeToolListEncodes(t *testing.T) {
	s := NewDispatcher(fullRegistry(t), bindAuthenticated, "margince-crm", "test")

	listed := s.toolList(scopedAgentCtx(
		principal.ScopeRead, principal.ScopeDraft, principal.ScopeWrite, principal.ScopeSend))
	if len(listed) == 0 {
		t.Fatal("no tools listed — this walk would pass vacuously")
	}

	// Per tool first: a single failing Marshal of the whole slice names no
	// tool, and the point of the boot assertion is that it says which one.
	for _, tool := range listed {
		if _, err := json.Marshal(tool); err != nil {
			t.Errorf("tool %v does not encode: %v", tool[fieldName], err)
		}
	}
	if _, err := json.Marshal(map[string]any{"tools": listed}); err != nil {
		t.Fatalf("the tools/list result does not encode: %v", err)
	}
}

// The hint has to be true of every tool it is emitted for, which is a stronger
// claim than "the derivation looks right": a scope shared by a tool that writes
// and one that does not cannot carry it. draft_follow_ups_for persists a draft
// activity through the same provider write path every other tool rides, so
// ScopeDraft answering read-only would put a false claim on the wire for it.
func TestNoWritingToolIsAdvertisedAsReadOnly(t *testing.T) {
	writers := map[string]bool{
		// Persists a draft activity on the deal's timeline.
		"draft_follow_ups_for": true,
	}
	saw := 0
	for _, spec := range fullRegistry(t).Specs() {
		if !writers[spec.Name] {
			continue
		}
		saw++
		if spec.ReadOnly() {
			t.Errorf("%s writes but advertises readOnlyHint true", spec.Name)
		}
	}
	if saw != len(writers) {
		t.Errorf("checked %d of %d known writers — a name here no longer registers, so this pin is stale", saw, len(writers))
	}
}

// probeReportCatalog stands in for the engine's catalog wherever a test builds
// the full registry. It carries a REAL entry rather than nothing, because an
// empty catalog takes the branch that omits the enum — so a registry built on
// nil would conformance-check a schema no deployment serves.
var probeReportCatalog = []ReportCatalogEntry{{
	Report:     "deals-by-stage",
	GroupBy:    []string{"pipeline_id", "stage_id", "status"},
	Filters:    []string{"owner_id", "pipeline_id", "status"},
	Aggregates: []string{"amount_minor"},
	Defaults:   "count as deals grouped by stage_id",
}}

// A result that misses its own declared schema is OUR defect, and the client
// must not be handed structuredContent that violates what this server just
// advertised. It still gets the whole answer in the text block — the omission
// is a statement about the structured member, not a refusal to answer.
func TestAResultThatMissesItsSchemaIsReportedAndLeftOutOfStructuredContent(t *testing.T) {
	spec := objectSpec("misdeclared", principal.ScopeRead)
	spec.OutputSchema = json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`)
	var log strings.Builder
	s := dispatchWith(t, echoTool{spec: spec, out: json.RawMessage(`{"count":"seven"}`)}, &log)

	res := s.call(scopedAgentCtx(principal.ScopeRead), json.RawMessage(`{"name":"misdeclared","arguments":{}}`))
	if _, structured := res["structuredContent"]; structured {
		t.Error("a result violating its declared schema was served as structuredContent")
	}
	content, ok := res["content"].([]map[string]any)
	if !ok || len(content) == 0 || !strings.Contains(content[0][fieldText].(string), "seven") {
		t.Errorf("the caller lost the answer entirely: %#v", res)
	}
	if !strings.Contains(log.String(), "does not satisfy the schema") {
		t.Errorf("the operator was not told which promise was broken: %s", log.String())
	}
}

// And the other direction, so the check cannot pass by refusing everything: a
// result that KEEPS its schema is served as structuredContent.
func TestAConformingResultIsServedAsStructuredContent(t *testing.T) {
	spec := objectSpec("conforming", principal.ScopeRead)
	spec.OutputSchema = json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`)
	var log strings.Builder
	s := dispatchWith(t, echoTool{spec: spec, out: json.RawMessage(`{"count":7}`)}, &log)

	res := s.call(scopedAgentCtx(principal.ScopeRead), json.RawMessage(`{"name":"conforming","arguments":{}}`))
	if _, structured := res["structuredContent"]; !structured {
		t.Errorf("a conforming result was withheld from structuredContent: %#v", res)
	}
}

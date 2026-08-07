// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The MCP method dispatcher: the protocol subset a tools-only server needs —
// initialize, tools/list, tools/call, ping — with every call routed through
// the Registry, which means through the admission gate. Tool failures travel
// IN-BAND as isError results (the agent should read them and adapt); only
// malformed JSON-RPC is a protocol error.
//
// It owns no transport. httpmcp.go builds one of these per handler and feeds
// it decoded requests, so method dispatch, the tool surface and the
// scrubbed-error rules are defined once rather than per transport. A second
// transport, should one return, gets the same object and therefore cannot
// answer a call differently.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// supportedProtocolVersions are the MCP revisions this server satisfies,
// NEWEST FIRST. initialize echoes the client's requested revision when we
// support it and otherwise answers with the newest — a stale list silently
// downgrades every modern client, so this is verified against the spec when
// it changes.
//
// The list stops at 2025-11-25 on purpose: 2026-07-28 and later are a
// different era — the spec's own terminology names them "modern" — with no
// initialize handshake at all (the protocol version travels per-request in
// the _meta key io.modelcontextprotocol/protocolVersion, server/discover is
// mandatory, and a version mismatch answers UnsupportedProtocolVersionError
// -32022). This server is "legacy": it establishes a session via initialize,
// full stop. Prepending a modern revision here would advertise a handshake
// this server does not honor; modern-era support is a named follow-up.
// 2024-11-05 is excluded too — it predates Streamable HTTP (HTTP+SSE only),
// a transport this server does not serve.
var supportedProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// The JSON-RPC and MCP wire tokens both transports repeat. Named once so a
// typo in one of them cannot make a handler answer a member no client reads.
const (
	jsonRPCVersion   = "2.0"
	methodInitialize = "initialize"
	// fieldName is the "name" member of both serverInfo and a tools/list
	// entry — the same identifier in both, so it stays one spelling.
	fieldName = "name"
	// fieldText is BOTH the content-block kind and the member carrying it in
	// an MCP tool result ({"type":"text","text":…}) — one spelling for both,
	// because a typo in either makes a result no client renders.
	fieldText = "text"
	// fieldTitle is the display name, carried BOTH at the top level of a
	// tools/list entry and inside its annotations — one spelling for both.
	fieldTitle = "title"
)

// negotiateProtocolVersion answers the client's requested MCP revision when
// this server satisfies it, and otherwise the newest revision this server
// satisfies — never the client's unsupported one, which would silently
// promise a handshake we cannot honor.
func negotiateProtocolVersion(requested string) string {
	if slices.Contains(supportedProtocolVersions, requested) {
		return requested
	}
	return supportedProtocolVersions[0]
}

// Binder authenticates one tool call: it returns a context carrying the
// workspace, the agent Principal and a fresh correlation scope. It runs
// PER CALL, not per session — revoking the passport (or demoting the
// granting human) takes effect on the very next call, not after a
// reconnect.
type Binder func(ctx context.Context) (context.Context, error)

// Dispatcher answers decoded MCP requests. It is transport-agnostic: the
// caller owns framing and hands it one rpcRequest at a time.
type Dispatcher struct {
	registry *Registry
	bind     Binder
	name     string
	version  string
	// log receives the true cause of failures the tool client only sees
	// generically — the client is an untrusted agent, so infrastructure
	// detail (DSNs, hosts, wrap chains) stays server-side.
	log *slog.Logger
}

// NewDispatcher builds the dispatcher for one server identity. name and version
// are what initialize reports as serverInfo.
func NewDispatcher(registry *Registry, bind Binder, name, version string) *Dispatcher {
	return &Dispatcher{registry: registry, bind: bind, name: name, version: version, log: slog.Default()}
}

// WithLogger routes server-side diagnostics to log. They are kept away from
// the tool client on purpose: it is an untrusted agent, so the true cause of a
// failure goes here while the client sees only the scrubbed answer.
func (s *Dispatcher) WithLogger(log *slog.Logger) *Dispatcher {
	if log != nil {
		s.log = log
	}
	return s
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Dispatcher) handle(ctx context.Context, req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID}
	switch req.Method {
	case methodInitialize:
		var params struct {
			//nolint:tagliatelle // protocolVersion is the MCP wire member, camelCase by the protocol
			ProtocolVersion string `json:"protocolVersion"`
		}
		// Params is optional on the wire; only unmarshal when the client sent
		// some, so an omitted field (not malformed JSON) falls through to the
		// negotiator's absent-value default rather than an error.
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				resp.Error = &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
				return resp
			}
		}
		resp.Result = map[string]any{
			"protocolVersion": negotiateProtocolVersion(params.ProtocolVersion),
			// listChanged is FALSE because this server has no way to send the
			// notification: notifications/tools/list_changed travels on the GET
			// SSE stream, and GET /mcp answers 405 here. The surface really
			// does change — tools/list is scope-filtered per caller — so the
			// claim would promise a message that can never arrive.
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":   map[string]any{fieldName: s.name, "version": s.version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolList(ctx)}
	case "tools/call":
		resp.Result = s.call(ctx, req.Params)
	case "resources/list":
		// This server has no resources; claude.ai calls this right after
		// initialize regardless, and an unadvertised capability answering
		// -32601 there reads as a broken server rather than a legitimate
		// empty catalog.
		resp.Result = map[string]any{"resources": []any{}}
	case "resources/templates/list":
		resp.Result = map[string]any{"resourceTemplates": []any{}}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": []any{}}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

// invocableByCaller reports whether the calling principal's passport scopes
// would let it invoke spec at all. It mirrors the scope arm of auth.Gate.Admit
// deliberately — a surface that advertises what the gate will refuse is a
// surface that lies, and the client's only way to discover the truth is to
// call and be denied.
//
// It answers the SCOPE axis only, which is what §5.7 promises. The seat
// ceiling and object RBAC are re-derived per call through the authority seam
// and are a named follow-up (§10.2); this filter must not pretend to enforce
// them, and Registry.Invoke remains the authority for every one of them.
//
// A ctx with no principal shows nothing rather than everything: the caller of
// a tools/list that never authenticated has no scopes, and an empty surface is
// the honest answer.
func invocableByCaller(ctx context.Context, spec mcp.ToolSpec) bool {
	p, ok := principal.Actor(ctx)
	if !ok {
		return false
	}
	// Humans and the system principal do not ride the scope model — their
	// authority is their RBAC, enforced at the store — so filtering them by a
	// passport scope they never carry would hide the whole surface.
	if p.Type != principal.PrincipalAgent {
		return true
	}
	return p.Scopes.Has(spec.RequiredScope)
}

// DescribeForClient is the description one tool is advertised with: what the
// tool is FOR, written on its spec, followed by how this server will govern the
// call. It is exported because tools/list is not the only surface that serves
// it — the operator console reads the same text through GET /v1/agent-tools,
// and a second rendering there would be a second answer to what a client is
// told.
//
// The order is the point. The written text answers the question a model is
// actually asking — which of thirty tools serves this goal — and the governance
// clause answers what happens once it has chosen. A description carrying only
// the second tells a model the passport scope of every tool and the purpose of
// none.
//
// The tier and scope are re-stated from the spec the admission gate enforces,
// so they cannot disagree with it. The crm.yaml operation family is NOT here:
// it is developer documentation, and a model has no use for the name of an
// endpoint it has no way to call. It stays on ToolSpec.OpenAPIOp, which is what
// the contract-parity gate reads.
func DescribeForClient(spec mcp.ToolSpec) string {
	// Every arm is named, and the fallthrough is the CONSERVATIVE reading, not
	// the convenient one: the admission gate treats anything that is not
	// TierAutoExecute as confirm-first, so a tier added without updating this
	// switch must not be advertised as running unattended. The same posture
	// tierWire takes on the REST side, for the same reason.
	tier := "a person approves every call before it runs"
	switch spec.Tier {
	case mcp.TierAutoExecute:
		tier = "runs immediately"
	case mcp.TierConfirmationRequired:
		tier = "a person approves every call before it runs"
	case mcp.TierDynamic:
		tier = "some calls run immediately and others a person approves first, decided per call from its arguments"
	}
	return fmt.Sprintf("%s (Governance: %s; requires passport scope %q.)", spec.Description, tier, spec.RequiredScope)
}

func (s *Dispatcher) toolList(ctx context.Context) []map[string]any {
	specs := s.registry.Specs()
	tools := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		if !invocableByCaller(ctx, spec) {
			continue
		}
		tool := map[string]any{
			fieldName: spec.Name,
			// Top-level title outranks annotations.title for display, and both
			// outrank the name. Registry.Register refuses a title-less tool, so
			// neither is ever the empty string here.
			fieldTitle:    spec.Title,
			"description": DescribeForClient(spec),
			"inputSchema": spec.InputSchema,
			// The two hints this server can state as FACTS, both read off the
			// spec the admission gate itself enforces rather than restated by
			// hand: what a tool may change is its scope, and whether it leaves
			// the workspace is its egress flag.
			//
			// destructiveHint and idempotentHint are deliberately absent: their
			// protocol defaults (destructive, non-idempotent) are already the
			// conservative reading, and only the looser value would need a
			// per-tool judgement, with nothing to hold it true.
			"annotations": map[string]any{
				fieldTitle:      spec.Title,
				"readOnlyHint":  spec.ReadOnly(),
				"openWorldHint": spec.Egress,
			},
		}
		if spec.OutputSchema != nil {
			tool["outputSchema"] = spec.OutputSchema
		}
		tools = append(tools, tool)
	}
	return tools
}

func (s *Dispatcher) call(ctx context.Context, params json.RawMessage) map[string]any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolError("malformed tools/call params: " + err.Error())
	}
	if p.Arguments == nil {
		p.Arguments = json.RawMessage(`{}`)
	}

	callCtx, err := s.bind(ctx)
	if err != nil {
		// The bind failure's cause (revoked vs expired vs infrastructure)
		// is server-side knowledge; the client only learns that its
		// credential no longer works.
		s.log.Warn("mcp: authentication failed", "tool", p.Name, "err", err)
		return toolError("authentication failed: the passport for this session was not accepted " +
			"(it may be revoked, expired, or bound to another workspace). Nothing was changed — " +
			"mint a new passport or contact the workspace admin.")
	}
	out, err := s.registry.Invoke(callCtx, p.Name, p.Arguments)
	if err != nil {
		return toolError(s.explain(p.Name, err))
	}
	return s.result(p.Name, out)
}

// result renders one successful tool return.
//
// The serialized JSON travels in a TextContent block, and — for a tool that
// declared an outputSchema, which every tool here does — ALSO as
// structuredContent: the spec makes that a MUST ("Servers MUST provide
// structured results that conform to this schema"). The text block stays
// beside it on the spec's own advice, so a client that predates structured
// content still reads the same answer rather than an empty result.
//
// The conformance actually checked is OBJECT-NESS, not the full schema. That
// is sufficient today only because every outputSchema on this surface is the
// bare `{"type":"object"}`, for which the two are the same claim. Registration
// would accept a richer one (required, enum, nested types), and the day a tool
// declares it, this owes it a real validation pass rather than the shape check
// below — so the narrower guarantee is written down instead of being inferred
// from a fleet that happens to be uniform.
func (s *Dispatcher) result(name string, out json.RawMessage) map[string]any {
	res := map[string]any{"content": []map[string]any{{"type": fieldText, fieldText: string(out)}}}
	if structured, ok := s.structuredContent(name, out); ok {
		res["structuredContent"] = structured
	}
	return res
}

// structuredContent answers the handler's own bytes when they satisfy the
// outputSchema the tool advertised, and reports it as a server defect when
// they do not.
//
// It passes those bytes THROUGH rather than re-marshalling a decoded copy.
// structuredContent and the text block are two renderings of one answer and a
// client may compare them, while a round trip through map[string]any would
// widen every integer to a float64 and reorder every key — so the two would
// disagree on exactly the tools that return a version or a count.
//
// A tool that declares an object schema and then answers with something else
// is OUR defect, not the caller's, and NOTHING detects it before this point:
// registration checks the declared schema, never a handler's answer, so the
// two halves of that agreement are held apart — one at boot, one only here, at
// the moment a real result exists. That is why this branch reports rather than
// assumes. The member is left off because omitting an optional one beats
// emitting one that violates the schema this same server just advertised, and
// the caller still gets the whole answer in the text block.
func (s *Dispatcher) structuredContent(name string, out json.RawMessage) (json.RawMessage, bool) {
	spec, ok := s.registry.Spec(name)
	if !ok || spec.OutputSchema == nil {
		return nil, false
	}
	// Decoding into map[string]json.RawMessage both proves out is a JSON
	// object and leaves its bytes untouched. A literal null decodes into a nil
	// map with no error, so it is refused explicitly rather than passing as an
	// object with no members.
	var object map[string]json.RawMessage
	if err := json.Unmarshal(out, &object); err != nil {
		s.log.Error("mcp: tool declares an object outputSchema but did not return a JSON object", "tool", name, "err", err)
		return nil, false
	}
	if object == nil {
		s.log.Error("mcp: tool declares an object outputSchema but returned JSON null", "tool", name)
		return nil, false
	}
	// And then the schema itself, which object-ness used to stand in for while a
	// bare object schema claimed nothing more. It now names which members are
	// present and what they hold, so that is what gets checked.
	if defect := ResultDefect(spec.OutputSchema, out); defect != "" {
		s.log.Error("mcp: tool result does not satisfy the schema this server advertised for it",
			"tool", name, "defect", defect)
		return nil, false
	}
	return out, true
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": fieldText, fieldText: msg}},
	}
}

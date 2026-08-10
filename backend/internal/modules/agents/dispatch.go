// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The MCP method dispatcher: the protocol subset a tools-only server needs —
// tools/list, tools/call, ping, the resource reads, and each era's own opening
// call — with every call routed through the Registry, which means through the
// admission gate. Tool failures travel IN-BAND as isError results (the agent
// should read them and adapt); only malformed JSON-RPC is a protocol error.
//
// It answers TWO framings, and the difference between them stops at parsing
// and rendering: modern.go decides which era a request is in and what its
// answer carries, while every arm below reaches records through the one
// registry. A framing able to alter what a call may do would be a second
// admission path, which ADR-0055 forbids.
//
// It owns no transport. httpmcp.go builds one of these per handler and feeds
// it decoded requests, so method dispatch, the tool surface and the
// scrubbed-error rules are defined once rather than per transport. A second
// transport, should one return, gets the same object and therefore cannot
// answer a call differently.

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// legacyProtocolVersions are the handshake-era MCP revisions this server
// satisfies, NEWEST FIRST. initialize echoes the client's requested revision
// when we support it and otherwise answers with the newest — a stale list
// silently downgrades every client, so this is verified against the spec when
// it changes.
//
// The window is a written-down decision rather than an implied one (ADR-0092
// §3), and it is exactly these two. A client on any older revision is answered
// the newest of them instead of a framing nobody maintains; 2024-11-05 is
// outside it for a second reason, since it predates Streamable HTTP (HTTP+SSE
// only) and this server serves no other transport. The modern era is not in
// this list because it establishes no session at all — it is
// modernProtocolVersion, and supportedProtocolVersions is the two together.
var legacyProtocolVersions = []string{"2025-11-25", "2025-06-18"}

// The JSON-RPC and MCP wire tokens both framings repeat. Named once so a typo
// in one of them cannot make a handler answer a member no client reads — and,
// for the method names, so the dispatch switch and the caching contract in
// modern.go cannot name two different sets of methods.
const (
	jsonRPCVersion              = "2.0"
	methodInitialize            = "initialize"
	methodPing                  = "ping"
	methodDiscover              = "server/discover"
	methodToolsList             = "tools/list"
	methodToolsCall             = "tools/call"
	methodResourcesList         = "resources/list"
	methodResourcesRead         = "resources/read"
	methodResourceTemplatesList = "resources/templates/list"
	methodPromptsList           = "prompts/list"
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

// negotiateLegacyVersion answers the client's requested MCP revision when this
// server satisfies it in the handshake era, and otherwise the newest one it
// does — never the client's unsupported one, which would silently promise a
// handshake we cannot honor. It never answers the modern revision: initialize
// is the legacy era's own method, and a client that reached it has already
// told us which era it speaks.
func negotiateLegacyVersion(requested string) string {
	if slices.Contains(legacyProtocolVersions, requested) {
		return requested
	}
	return legacyProtocolVersions[0]
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
	// resources publishes the read-only documents beside the tool surface
	// (the query vocabulary today). Nil is a server with no resources, which
	// is why the capability is advertised conditionally: claiming one with
	// nothing behind it sends a client to a resources/read that can only
	// fail.
	resources mcp.ResourceProvider
	// tasks is the durable half of the io.modelcontextprotocol/tasks extension.
	// Nil is a composition that never hands out a handle, and the three task
	// methods answer -32601 there rather than a not-found for an id no client
	// can be holding.
	tasks Tasks
	// taskApprovals is the decision this surface polls on behalf of a handle.
	// It arrives WITH the store, because neither half of the extension is
	// usable without the other.
	taskApprovals TaskApprovals
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

// WithResources wires the resource provider. Compose calls it: the documents
// published here are composed by other modules (the query vocabulary is the
// search module's), and a module never reaches for a sibling.
func (s *Dispatcher) WithResources(provider mcp.ResourceProvider) *Dispatcher {
	s.resources = provider
	return s
}

// WithTasks wires the durable task store, which is what turns the Tasks
// extension on for this server: without it the capability is not advertised and
// a staged 🟡 call answers the same refusal it always did.
func (s *Dispatcher) WithTasks(tasks Tasks, approvals TaskApprovals) *Dispatcher {
	s.tasks, s.taskApprovals = tasks, approvals
	return s
}

// tasksServed reports whether this server can answer the extension at all. Both
// halves or neither: a store with no way to read decisions would hand out
// handles that never move.
func (s *Dispatcher) tasksServed() bool { return s.tasks != nil && s.taskApprovals != nil }

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
	// Data carries the structured half of an error a client is expected to act
	// on rather than display. It is omitted when there is nothing to act on,
	// and it is TYPED so the wire shape of an error stays as constrained as the
	// wire shape of a result.
	Data *rpcErrorData `json:"data,omitempty"`
}

// rpcErrorData is every structured member a JSON-RPC error on this surface can
// carry. Both producers exist for the same reason — a client can read the
// member and fix the request rather than display it — and naming the shape
// keeps the next one from being an open map.
type rpcErrorData struct {
	Supported []string `json:"supported,omitempty"`
	Requested string   `json:"requested,omitempty"`
	//nolint:tagliatelle // requiredCapabilities is the specification's own member name
	RequiredCapabilities *requiredCapabilities `json:"requiredCapabilities,omitempty"`
}

// requiredCapabilities is the declaration a MissingRequiredClientCapability
// refusal asks for, rendered in the shape the client would send it back in.
//
// The empty struct as the map's value is the extension capability's own type:
// the specification defines it as an object with no members, so `{}` is the
// declaration and anything richer would be inventing a setting.
type requiredCapabilities struct {
	Extensions map[string]struct{} `json:"extensions,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handle answers one decoded request in the framing the transport decided for
// it. The framing chooses which methods exist and how the answer is rendered;
// every arm below that reaches a record reaches it through the same registry,
// so it cannot choose what the call may do.
func (s *Dispatcher) handle(ctx context.Context, req rpcRequest, fr framing) rpcResponse {
	resp := s.dispatch(ctx, req, fr)
	if fr.modern {
		return s.finishModern(resp, req.Method)
	}
	return resp
}

func (s *Dispatcher) dispatch(ctx context.Context, req rpcRequest, fr framing) rpcResponse {
	if answered, owned := s.eraOwned(req, fr); owned {
		return answered
	}
	resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID}
	switch req.Method {
	case methodToolsList:
		resp.Result = map[string]any{"tools": s.toolList(ctx, fr)}
	case methodToolsCall:
		resp.Result = s.call(ctx, req.Params, fr)
	case methodResourcesList:
		// Answered even with no provider wired: claude.ai calls this right
		// after initialize regardless, and an unadvertised capability
		// answering -32601 there reads as a broken server rather than a
		// legitimate empty catalog.
		resp.Result = map[string]any{"resources": s.resourceList(ctx)}
	case methodResourcesRead:
		// Assigned on separate branches so a failed read never carries a
		// result alongside its error, which JSON-RPC forbids.
		if result, rpcErr := s.readResource(ctx, req.Params); rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
	case methodResourceTemplatesList:
		resp.Result = map[string]any{"resourceTemplates": []any{}}
	case methodPromptsList:
		resp.Result = map[string]any{"prompts": []any{}}
	case methodTasksGet, methodTasksUpdate, methodTasksCancel:
		// Modern-only, and not through eraOwned because these need the request
		// context: they authenticate, read and can execute. In the handshake era
		// the method genuinely does not exist — the capability that admits it is
		// a per-request `_meta` member that era cannot carry.
		if !fr.modern {
			return methodNotFound(req)
		}
		return s.taskMethod(ctx, req, fr)
	default:
		return methodNotFound(req)
	}
	return resp
}

// eraOwned answers the calls that exist in ONE framing only, and reports
// whether the method was one of them at all.
//
// Each era's opening call belongs here because answering the other era's would
// tell a client it had reached the kind of server it was probing for, which is
// exactly the question those two calls exist to settle. ping is here for a
// different reason: the 2026-07-28 revision REMOVED it along with the handshake
// it kept alive, so it stays answered only in the era that still defines it.
func (s *Dispatcher) eraOwned(req rpcRequest, fr framing) (rpcResponse, bool) {
	resp := rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID}
	switch req.Method {
	case methodInitialize:
		if fr.modern {
			return methodNotFound(req), true
		}
		if result, rpcErr := s.initialize(req.Params); rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resp.Result = result
		}
	case methodDiscover:
		if !fr.modern {
			return methodNotFound(req), true
		}
		resp.Result = s.discover()
	case methodPing:
		if fr.modern {
			return methodNotFound(req), true
		}
		resp.Result = map[string]any{}
	default:
		return rpcResponse{}, false
	}
	return resp, true
}

func methodNotFound(req rpcRequest) rpcResponse {
	return rpcResponse{
		JSONRPC: jsonRPCVersion, ID: req.ID,
		Error: &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method},
	}
}

// initialize answers the handshake era's opening call: the revision this
// server will speak with THIS client, what it can do, and who it is.
func (s *Dispatcher) initialize(rawParams json.RawMessage) (map[string]any, *rpcError) {
	var params struct {
		//nolint:tagliatelle // protocolVersion is the MCP wire member, camelCase by the protocol
		ProtocolVersion string `json:"protocolVersion"`
	}
	// Params is optional on the wire; only unmarshal when the client sent
	// some, so an omitted field (not malformed JSON) falls through to the
	// negotiator's absent-value default rather than an error.
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			// The decoder's own message names Go types, which is server-side
			// detail an untrusted client has no use for. It is told what to
			// send instead.
			s.log.Warn("mcp: initialize params did not decode", "err", err)
			return nil, &rpcError{
				Code:    codeInvalidParams,
				Message: `invalid params: initialize takes an object whose "protocolVersion" is a string`,
			}
		}
	}
	return map[string]any{
		"protocolVersion": negotiateLegacyVersion(params.ProtocolVersion),
		"capabilities":    s.capabilities(false),
		"serverInfo":      s.identity(),
	}, nil
}

// capabilities is what this server claims it can do — initialize reports it to
// a handshake client and server/discover to a modern one, and the FEATURE
// entries are identical in both, because two spellings of one claim is how a
// client ends up told different things by the same server.
//
// The EXTENSIONS entry is the one thing only one era carries, and not as a
// variant of the same claim. An extension is negotiated per request, in a
// `_meta` member the handshake era has no place for, so advertising one to a
// legacy client would offer a negotiation it cannot enter — the same reason
// ping is answered in one era only.
//
// listChanged is FALSE on both feature entries because this server has no way
// to send the notification: notifications/*/list_changed travels on a stream
// this transport does not open. Both surfaces really do change — each is
// filtered per caller — so the claim would promise a message that can never
// arrive.
func (s *Dispatcher) capabilities(modern bool) map[string]any {
	capabilities := map[string]any{"tools": map[string]any{"listChanged": false}}
	if s.resources != nil {
		// subscribe is FALSE for the same reason, and separately: a
		// per-caller document has no shared state to subscribe to.
		capabilities["resources"] = map[string]any{"listChanged": false, "subscribe": false}
	}
	if modern && s.tasksServed() {
		// Advertised only where it is real. Without a task store this server
		// never hands out a handle, and a client that saw the extension
		// advertised would be entitled to expect one.
		capabilities["extensions"] = map[string]any{extensionTasks: map[string]any{}}
	}
	// The App extension, on the same terms and for the same reason: a host told
	// this server serves views is entitled to a document to prefetch, so the
	// claim is derived from the assembled surface rather than declared.
	if modern && s.appsServed() {
		extensions, claimed := capabilities["extensions"].(map[string]any)
		if !claimed {
			extensions = map[string]any{}
			capabilities["extensions"] = extensions
		}
		extensions[extensionUI] = map[string]any{}
	}
	return capabilities
}

// identity is the serverInfo both framings report — the handshake era in its
// initialize result, the modern era in every result's _meta.
func (s *Dispatcher) identity() map[string]any {
	return map[string]any{fieldName: s.name, "version": s.version}
}

// call answers tools/call. Its return is `any` rather than a result map because
// a confirm-first call has TWO shapes: the refusal every client understands,
// and — for a client that declared the Tasks extension on this request — a task
// handle it can poll until the person decides.
//
//craft:ignore naked-any the protocol makes this result polymorphic (CallToolResult or CreateTaskResult) and the framing tells them apart by TYPE — a named wrapper here would be a naked any wearing a hat, and collapsing both into one map would make resultType a member two components read differently
func (s *Dispatcher) call(ctx context.Context, params json.RawMessage, fr framing) any {
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
		if handle, minted := s.mintTask(callCtx, fr, p.Name, err); minted {
			return handle
		}
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
// What is checked is the DECLARED SCHEMA, not object-ness. Object-ness was
// sufficient only while every outputSchema on this surface was the bare
// {"type":"object"}, for which the two are the same claim; a tool now advertises
// the exact shape its handler marshals, so a result that misses it is a promise
// this server made and did not keep. structuredContent below is the member that
// carries that promise, and it is withheld rather than served in violation.
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
// A tool that declares a shape and then answers with something else is OUR
// defect, not the caller's, and NOTHING detects it before this point:
// registration checks the declared schema, never a handler's answer, so the
// two halves of that agreement are held apart — one at boot, one only here, at
// the moment a real result exists. That is why this branch reports rather than
// assumes. The member is left off because omitting an optional one beats
// emitting one that violates the schema this same server just advertised, and
// the caller still gets the whole answer in the text block.
//
// ONE check, not two. The envelope is built by this server, so its object-ness
// is not in question; what can still part company is the payload under `data`
// against the shape the tool declared for it — and the declared schema states
// that the envelope is an object with `data` in it, so reading the result
// against the schema asks both questions at once. A separate object-ness probe
// here would be a second, weaker definition of the same word.
func (s *Dispatcher) structuredContent(name string, out json.RawMessage) (json.RawMessage, bool) {
	spec, ok := s.registry.Spec(name)
	if !ok || spec.OutputSchema == nil {
		return nil, false
	}
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

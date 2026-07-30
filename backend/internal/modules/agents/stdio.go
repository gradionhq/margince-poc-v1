// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The A1 local MCP server: JSON-RPC 2.0 over stdio, one message per line
// (the MCP stdio transport). It speaks the protocol subset a tools-only
// server needs — initialize, tools/list, tools/call, ping — and dispatches
// every call through the Registry, which means through the admission
// auth. Tool failures travel IN-BAND as isError results (the agent should
// read them and adapt); only malformed JSON-RPC is a protocol error.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// StdioServer serves MCP over one in/out pipe pair.
type StdioServer struct {
	registry *Registry
	bind     Binder
	name     string
	version  string
	// log receives the true cause of failures the tool client only sees
	// generically — the client is an untrusted agent, so infrastructure
	// detail (DSNs, hosts, wrap chains) stays server-side.
	log *slog.Logger
}

func NewStdioServer(registry *Registry, bind Binder, name, version string) *StdioServer {
	return &StdioServer{registry: registry, bind: bind, name: name, version: version, log: slog.Default()}
}

// WithLogger routes server-side diagnostics to log (protocol traffic
// owns stdout, so callers point this at stderr or a file).
func (s *StdioServer) WithLogger(log *slog.Logger) *StdioServer {
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

// Serve reads requests until EOF or ctx cancellation. Responses are
// written in request order — the loop is sequential by design (an agent
// session is a conversation, and the store serializes on the database
// anyway).
func (s *StdioServer) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if err := enc.Encode(rpcResponse{JSONRPC: jsonRPCVersion, Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if req.ID == nil {
			// A notification (notifications/initialized etc.) gets no
			// response by JSON-RPC rule.
			continue
		}
		if err := enc.Encode(s.handle(ctx, req)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *StdioServer) handle(ctx context.Context, req rpcRequest) rpcResponse {
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
			// listChanged: true claims the notification the GET SSE stream
			// (a later phase) actually fires; nothing else is claimed.
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":   map[string]any{fieldName: s.name, "version": s.version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolList()}
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

func (s *StdioServer) toolList() []map[string]any {
	specs := s.registry.Specs()
	tools := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		tier := "auto_execute (runs immediately)"
		switch spec.Tier {
		case mcp.TierConfirmationRequired:
			tier = "confirmation_required (requires human approval)"
		case mcp.TierDynamic:
			tier = "auto_execute, except moves that close a deal require human approval"
		}
		desc := fmt.Sprintf("Autonomy: %s. Requires passport scope %q.", tier, spec.RequiredScope)
		if spec.OpenAPIOp != "" {
			// Extension tools map to no crm.yaml operation; only append the
			// clause when there is one, rather than rendering "Maps to .".
			desc += fmt.Sprintf(" Maps to %s.", spec.OpenAPIOp)
		}
		tool := map[string]any{
			fieldName:     spec.Name,
			"description": desc,
			"inputSchema": spec.InputSchema,
		}
		if spec.OutputSchema != nil {
			tool["outputSchema"] = spec.OutputSchema
		}
		tools = append(tools, tool)
	}
	return tools
}

func (s *StdioServer) call(ctx context.Context, params json.RawMessage) map[string]any {
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
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(out)}}}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	}
}

// explain turns the sentinel taxonomy into messages an agent can act on —
// the distinction between "you may never" and "a human must say yes"
// changes what the agent should do next. Anything outside the taxonomy
// is an internal failure whose text (driver errors, hosts, wrap chains)
// must not cross the trust boundary to the tool client: it surfaces
// generically and the real cause is logged server-side.
func (s *StdioServer) explain(tool string, err error) string {
	switch {
	case errors.Is(err, apperrors.ErrRequiresApproval):
		return "This is a confirm-first (🟡) action: it needs human approval before it runs. " +
			"Ask the user to perform it in the CRM, or wait for the approval flow. Nothing was changed. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrScopeExceeded):
		return "The passport this session runs under does not grant the scope this tool needs. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrPermissionDenied):
		return "The human this passport acts for is not permitted to do this — the agent inherits exactly their access. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrNotFound):
		return "No such record in this workspace (or it is outside the acting user's row scope). (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrVersionSkew):
		return "The record changed since it was read; re-read it and retry with the new version. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrApprovalTokenInvalid):
		return "The approval token was not accepted — it may be consumed, expired, or for a different call. " +
			"Ask for a fresh approval and retry. (" + err.Error() + ")"
	case errors.Is(err, apperrors.ErrUnsupportedBySoR):
		// A DECLARED capability gap (AC-OV-2), not a fault: this workspace's
		// records live in a system that cannot answer this tool. Saying so —
		// and saying do-not-retry — is the whole point of declaring it;
		// falling through to the generic branch would tell the agent to retry
		// a permanent refusal and burn a scheduled run's whole step budget on
		// it.
		return "This workspace's system of record cannot serve this tool, and no retry will change that. " +
			"Do not retry it; use another tool, or tell the user this capability is unavailable here. (" + err.Error() + ")"
	default:
		s.log.Error("mcp: tool call failed", "tool", tool, "err", err)
		return "The tool failed for an internal reason; nothing may have changed. " +
			"Retry, and if it keeps failing contact the workspace admin."
	}
}

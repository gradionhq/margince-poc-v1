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

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// protocolVersion is the MCP revision this server implements.
const protocolVersion = "2025-03-26"

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
}

func NewStdioServer(registry *Registry, bind Binder, name, version string) *StdioServer {
	return &StdioServer{registry: registry, bind: bind, name: name, version: version}
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
			if err := enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}); err != nil {
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
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolList()}
	case "tools/call":
		resp.Result = s.call(ctx, req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func (s *StdioServer) toolList() []map[string]any {
	specs := s.registry.Specs()
	tools := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		tier := "green (runs immediately)"
		switch spec.Tier {
		case mcp.TierYellow:
			tier = "yellow (requires human approval)"
		case mcp.TierDynamic:
			tier = "green, except moves that close a deal require human approval"
		}
		tools = append(tools, map[string]any{
			"name":        spec.Name,
			"description": fmt.Sprintf("Autonomy: %s. Requires passport scope %q. Maps to %s.", tier, spec.RequiredScope, spec.OpenAPIOp),
			"inputSchema": spec.InputSchema,
		})
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
		return toolError("authentication failed: " + err.Error())
	}
	out, err := s.registry.Invoke(callCtx, p.Name, p.Arguments)
	if err != nil {
		return toolError(explain(err))
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
// changes what the agent should do next.
func explain(err error) string {
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
	default:
		return err.Error()
	}
}

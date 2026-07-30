// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The A2 hosted transport (B-EP06.18a): the SAME tool surface as A1
// over streamable HTTP — one JSON-RPC exchange per POST. Nothing here
// adds capability: registry, admission, staging and audit are shared
// with stdio, so "two transports, one gate" is a property of the
// construction, not a discipline.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
)

// mcpCallDeadline bounds one JSON-RPC exchange's response write. A dynamic
// tool call can block on a model call, which modules/ai budgets at 120s per
// request, so the deadline must outlast that budget with headroom or the
// slowest legitimate call dies mid-response.
const mcpCallDeadline = 150 * time.Second

// ResourceMetadataChallenge builds the RFC 9728 WWW-Authenticate challenge a
// 401 on this transport carries: the "Bearer" scheme name plus a pointer at
// the protected-resource document. The pointer is ABSOLUTE on the request's
// own origin because a client dereferences it as given — a bare path only
// resolves for a client that already knows where it is talking to, which is
// the one thing discovery exists to tell it.
//
// The scope hint is not decoration: absent it, a client requests every scope
// the protected-resource metadata advertises in scopes_supported, including
// send. Naming "read draft" makes the conservative grant the default, with
// the human free to widen it on the consent page.
func ResourceMetadataChallenge(r *http.Request) string {
	return `Bearer resource_metadata="` + httpserver.RequestOrigin(r) + `/.well-known/oauth-protected-resource", scope="read draft"` // NOSONAR: RFC 9728 challenge, not a secret
}

// NewHTTPHandler serves MCP over HTTP. authenticate runs PER REQUEST —
// each exchange re-derives the passport and the granting human's live
// authority, so revocation binds between any two calls exactly as the
// A1 loop guarantees. challenge builds the 401's RFC 9728 pointer from
// the request, so the origin (and the scopes a deployment asks for) is the
// mounting server's decision rather than a constant frozen in here.
func NewHTTPHandler(registry *Registry, authenticate func(*http.Request) (context.Context, error), challenge func(*http.Request) string, name, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "MCP is POST-only on this transport", http.StatusMethodNotAllowed)
			return
		}
		ctx, err := authenticate(r)
		if err != nil {
			// RFC 9728: the 401 names where the client can discover the
			// authorization server.
			w.Header().Set("WWW-Authenticate", challenge(r))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			//craft:ignore swallowed-errors a failed write of the 401 body means the client hung up — there is no channel left to report on
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_token"})
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "reading body", http.StatusBadRequest)
			return
		}
		var req rpcRequest
		w.Header().Set("Content-Type", "application/json")
		if err := json.Unmarshal(body, &req); err != nil {
			//craft:ignore swallowed-errors a failed write of the parse-error response means the client hung up — there is no channel left to report on
			_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			return
		}
		// A present-and-unsupported MCP-Protocol-Version is refused with a
		// plain 400 whose body is prose, NEVER a modern-era -32022
		// UnsupportedProtocolVersionError: the spec has a dual-era client
		// identify a legacy server by seeing a 4xx without a recognized
		// modern error body, and only then fall back to initialize. A -32022
		// body would read as a modern server rejecting a mismatched version,
		// so the client would retry with a modern (handshake-free,
		// _meta-carrying) request this server can never serve — turning a
		// working fallback into a hard failure. initialize itself is exempt:
		// negotiation for that call happens through its own request body,
		// not this header, and older clients omit the header entirely until
		// they have a negotiated version to send.
		if v := r.Header.Get("MCP-Protocol-Version"); req.Method != "initialize" && v != "" &&
			!slices.Contains(supportedProtocolVersions, v) {
			http.Error(w, "unsupported MCP-Protocol-Version; this server supports: "+
				strings.Join(supportedProtocolVersions, ", "), http.StatusBadRequest)
			return
		}
		if req.ID == nil {
			// A notification gets no response by JSON-RPC rule.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// A dynamic tool call can block on a model call, which modules/ai
		// budgets at 120s per request; the api's server-wide WriteTimeout is
		// 30s. Extend the deadline for THIS route only — raising the server's
		// would weaken every other endpoint. An error here means the handler
		// chain lost Unwrap(); fail loudly rather than serve responses that
		// die mid-write.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(mcpCallDeadline)); err != nil {
			httperr.WriteJSON(w, http.StatusInternalServerError,
				map[string]string{"error": "this server chain cannot extend the response deadline"})
			return
		}
		// bind is a passthrough: this request's ctx IS the authenticated
		// session, minted moments ago.
		server := NewStdioServer(registry, func(context.Context) (context.Context, error) { return ctx, nil }, name, version)
		//craft:ignore swallowed-errors the JSON-RPC result carries its own error member; a failed response write means the client hung up
		_ = json.NewEncoder(w).Encode(server.handle(ctx, req))
	})
}

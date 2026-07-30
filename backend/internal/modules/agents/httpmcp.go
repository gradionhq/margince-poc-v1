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
	"sync"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// mcpCallDeadline bounds one JSON-RPC exchange's response write. A dynamic
// tool call can block on a model call, which modules/ai budgets at 120s per
// request, so the deadline must outlast that budget with headroom or the
// slowest legitimate call dies mid-response.
const mcpCallDeadline = 150 * time.Second

// sessionKey identifies one live MCP session. The session id ALONE is
// deliberately not enough to act on (DESIGN §10.4): every request
// re-authenticates via the Bearer passport, which is where authority
// comes from, so the id itself is unvalidated. Pairing it with the
// presenting passport means DELETE can only ever close a session that
// passport itself opened — keying on the id alone would let any
// authenticated agent close another connector's session by guessing or
// replaying its value.
type sessionKey struct {
	passportID ids.UUID
	sessionID  string
}

// sessionRegistry is in-process bookkeeping for open MCP sessions,
// scoped to ONE handler instance rather than a package-level global —
// a global would leak state between two handlers (or two tests) in the
// same process. A later phase's GET stream registers its cancel func
// here under the id `initialize` mints; DELETE cancels and removes it.
type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[sessionKey]context.CancelFunc
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[sessionKey]context.CancelFunc)}
}

// register records a new session under the presenting passport. cancel
// runs exactly once, from close.
func (r *sessionRegistry) register(passportID ids.UUID, sessionID string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sessionKey{passportID, sessionID}] = cancel
}

// close cancels and removes the session sessionID owned by passportID.
// It reports false, leaving the registry untouched, when no session
// exists under that exact pair — including a sessionID that IS live but
// under a different passport. Those two cases must read identically:
// telling them apart would let a DELETE probe confirm another
// connector's session id is currently open.
func (r *sessionRegistry) close(passportID ids.UUID, sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := sessionKey{passportID, sessionID}
	cancel, ok := r.sessions[key]
	if !ok {
		return false
	}
	delete(r.sessions, key)
	cancel()
	return true
}

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

// writeRPCResponse writes one JSON-RPC response, framed per the client's
// Accept header (DESIGN §5.3): `text/event-stream` gets a single `data:`
// frame and then the stream closes — there is no ongoing push on this
// path, only the one exchange the request asked for. Anything else,
// including an absent Accept, gets the plain JSON body.
func writeRPCResponse(w http.ResponseWriter, r *http.Request, resp rpcResponse) {
	body, err := json.Marshal(resp)
	if err != nil {
		// Every field of resp is a type this package constructs (rpcResponse,
		// rpcError, JSON-safe map[string]any results) — a marshal failure
		// here is a programming error, not a client-caused condition to
		// route through the JSON-RPC error member.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   "unencodable_response",
			Detail: "This server produced a response it cannot encode.",
		})
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		//craft:ignore swallowed-errors a failed write means the client hung up — there is no channel left to report on
		_, _ = w.Write([]byte("data: " + string(body) + "\n\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	//craft:ignore swallowed-errors a failed write of the JSON-RPC result means the client hung up
	_, _ = w.Write(body)
}

// httpMCPHandler is the concrete type behind NewHTTPHandler's http.Handler
// return value. It is unexported — callers outside this package see only
// the interface — but a whitebox test in this package can reach `sessions`
// directly to assert registry state DELETE alone cannot prove (a 404 looks
// the same whether a session never existed or belongs to someone else; the
// registry is the only place that distinction is visible).
type httpMCPHandler struct {
	registry     *Registry
	authenticate func(*http.Request) (context.Context, error)
	challenge    func(*http.Request) string
	name         string
	version      string
	sessions     *sessionRegistry
}

// NewHTTPHandler serves MCP over HTTP. authenticate runs PER REQUEST —
// each exchange re-derives the passport and the granting human's live
// authority, so revocation binds between any two calls exactly as the
// A1 loop guarantees. challenge builds the 401's RFC 9728 pointer from
// the request, so the origin (and the scopes a deployment asks for) is the
// mounting server's decision rather than a constant frozen in here.
func NewHTTPHandler(registry *Registry, authenticate func(*http.Request) (context.Context, error), challenge func(*http.Request) string, name, version string) http.Handler {
	return &httpMCPHandler{
		registry:     registry,
		authenticate: authenticate,
		challenge:    challenge,
		name:         name,
		version:      version,
		sessions:     newSessionRegistry(),
	}
}

func (h *httpMCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodDelete:
	default:
		// The GET stream is a later phase; every other verb is refused
		// outright.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusMethodNotAllowed,
			Code:   "method_not_allowed",
			Detail: "MCP is POST and DELETE only on this transport.",
		})
		return
	}
	ctx, err := h.authenticate(r)
	if err != nil {
		// RFC 9728: the 401 names where the client can discover the
		// authorization server. DELETE authenticates exactly like POST —
		// there is no unauthenticated teardown path.
		w.Header().Set("WWW-Authenticate", h.challenge(r))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		//craft:ignore swallowed-errors a failed write of the 401 body means the client hung up — there is no channel left to report on
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_token"})
		return
	}

	// The authenticated context rides ON the request from here down rather
	// than beside it: one value to pass means no handler below can read the
	// unauthenticated r.Context() by accident. authenticate derives ctx from
	// r.Context(), but it does so behind an injected closure, which is why
	// this needs saying to the linter as well as to the reader.
	r = r.WithContext(ctx) //nolint:contextcheck // ctx is derived from r.Context() inside the injected authenticate closure
	if r.Method == http.MethodDelete {
		h.teardownSession(w, r)
		return
	}
	h.servePost(w, r)
}

// servePost handles the one JSON-RPC exchange a POST carries: parse,
// negotiate the protocol version, dispatch through the shared stdio
// handler, and — on a successful initialize — mint and register the
// session this connection now owns.
func (h *httpMCPHandler) servePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadRequest,
			Code:   "unreadable_body",
			Detail: "This request's body could not be read to the end.",
		})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCResponse(w, r, rpcResponse{JSONRPC: jsonRPCVersion, Error: &rpcError{Code: -32700, Message: "parse error"}})
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
	if v := r.Header.Get("MCP-Protocol-Version"); req.Method != methodInitialize && v != "" &&
		!slices.Contains(supportedProtocolVersions, v) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadRequest,
			Code:   "unsupported_protocol_version",
			Detail: "Unsupported MCP-Protocol-Version; this server supports: " +
				strings.Join(supportedProtocolVersions, ", ") + ".",
		})
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
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   "deadline_not_extendable",
			Detail: "This server chain cannot extend the response deadline.",
		})
		return
	}
	// bind is a passthrough: this request's ctx IS the authenticated
	// session, minted moments ago.
	server := NewStdioServer(h.registry, func(context.Context) (context.Context, error) { return ctx, nil }, h.name, h.version)
	resp := server.handle(ctx, req)
	if req.Method == methodInitialize && resp.Error == nil {
		h.mintSession(ctx, w)
	}
	writeRPCResponse(w, r, resp)
}

// mintSession registers a fresh session under the passport that just
// initialized and returns its id as Mcp-Session-Id. The passport is what
// "closes only your own" (§10.4) has to key on: the session id itself
// carries no authority. actor.PassportID is the zero value for a human
// call, which is fine — it just means every session-less human shares one
// bucket in this registry, a case this transport (agent passports only)
// does not reach in production.
func (h *httpMCPHandler) mintSession(ctx context.Context, w http.ResponseWriter) {
	actor, _ := principal.Actor(ctx)
	sessionID := ids.NewV7().String()
	h.sessions.register(actor.PassportID, sessionID, func() {})
	w.Header().Set("Mcp-Session-Id", sessionID)
}

// teardownSession answers DELETE /mcp: it closes only the session the
// PRESENTING passport opened. A missing header is a client error (400);
// a session id that does not exist under this exact passport — whether it
// never existed or belongs to someone else — answers 404, identically, so
// a probe cannot tell the two apart.
func (h *httpMCPHandler) teardownSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadRequest,
			Code:   "missing_session_id",
			Detail: "Closing a session needs the Mcp-Session-Id header initialize returned.",
		})
		return
	}
	actor, _ := principal.Actor(r.Context())
	if !h.sessions.close(actor.PassportID, sessionID) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusNotFound,
			Code:   "session_not_open",
			Detail: "No session is open under this id for this passport.",
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

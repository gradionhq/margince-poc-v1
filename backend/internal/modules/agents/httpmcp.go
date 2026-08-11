// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The A2 hosted transport (B-EP06.18a): the governed tool surface over
// streamable HTTP — one JSON-RPC exchange per POST. It is the ONLY MCP
// transport; A1 stdio and its cmd/mcp binary are retired (SCR-9).
//
// Nothing here adds capability. Registry, admission, staging and audit all
// live behind the Dispatcher this handler builds, so a route cannot widen
// what an agent may do — that is a property of the construction, not a
// discipline.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// mcpCallDeadline bounds one JSON-RPC exchange's response write. A dynamic
// tool call can block on a model call, which modules/ai budgets at 120s per
// request, so the deadline must outlast that budget with headroom or the
// slowest legitimate call dies mid-response.
const mcpCallDeadline = 150 * time.Second

// ErrAuthUnavailable marks an authenticate failure that says nothing about the
// presented credential — the installation would not resolve, or the database
// behind the passport lookup was unreachable. The transport answers those 503,
// never 401.
//
// It is a sentinel the injected authenticate closure wraps rather than a
// condition this package detects, because the conditions live in identity and
// a module never imports a sibling: compose composes both and therefore owns
// the classification (see compose/mcpedge.go).
var ErrAuthUnavailable = errors.New("agents: the credential could not be verified")

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

// writeRPCResponse writes one JSON-RPC response under status, framed per the
// client's Accept header (DESIGN §5.3): `text/event-stream` gets a single
// `data:` frame and then the stream closes — there is no ongoing push on this
// path, only the one exchange the request asked for. Anything else,
// including an absent Accept, gets the plain JSON body.
//
// The status is a parameter because the modern framing pins one for several of
// its answers — 400 for a malformed or mismatched request, 404 for a method
// this server does not answer — and it is those statuses, together with a
// recognized JSON-RPC error body, that let a dual-era client tell a modern
// server from a legacy one.
func writeRPCResponse(w http.ResponseWriter, r *http.Request, resp rpcResponse, status int) {
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
	// BYO-WIRE-1: a scope-filtered catalog caches `private`. Every answer on
	// this path is derived from the presenting passport — tools/list is cut to
	// the scopes it holds, and each call is re-authorized against live
	// authority — so a stored copy is two wrong things at once: one
	// principal's surface served to another, and an authority claim that went
	// stale the moment a passport was revoked mid-session. Neither request
	// reaches the server to be audited, which is what makes this a disclosure
	// rather than a staleness bug.
	//
	// no-store rather than a bare private for the second reason: private
	// permits a browser's own cache to keep it, and this answer may not
	// outlive the authority it was computed under.
	//
	// Set above the framing branch because it is a property of the ANSWER
	// rather than of how the client asked for it — the event-stream frame
	// carries the same scope-filtered body.
	w.Header().Set("Cache-Control", "private, no-store")
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		//craft:ignore swallowed-errors a failed write means the client hung up — there is no channel left to report on
		_, _ = w.Write([]byte("data: " + string(body) + "\n\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//craft:ignore swallowed-errors a failed write of the JSON-RPC result means the client hung up
	_, _ = w.Write(body)
}

// httpMCPHandler is the concrete type behind NewHTTPHandler's http.Handler
// return value. It is unexported — callers outside this package see only the
// interface.
//
// It holds NO per-connection state, which is the whole of ADR-0092 §6: a call
// can land on any api replica because there is nothing here for it to have
// landed on before. What the in-process session registry used to hold was
// bookkeeping plus an implicit volume bound, and the bound now lives in Redis
// (platform/agentquota) where every replica reads the same number.
type httpMCPHandler struct {
	// server is the SAME dispatcher the stdio transport runs, built once:
	// method dispatch, the tool surface and the scrubbed-error rules are
	// shared rather than re-derived per request, so the two transports cannot
	// answer one call differently. It holds no per-request state — the
	// request's own authenticated context is what dispatch runs on.
	server       *Dispatcher
	authenticate func(*http.Request) (context.Context, error)
	challenge    func(*http.Request) string
}

// NewHTTPHandler serves MCP over HTTP. authenticate runs PER REQUEST —
// each exchange re-derives the passport and the granting human's live
// authority, so revocation binds between any two calls exactly as the
// A1 loop guarantees. challenge builds the 401's RFC 9728 pointer from
// the request, so the origin (and the scopes a deployment asks for) is the
// mounting server's decision rather than a constant frozen in here.
//
// log is the mounting process's configured logger, and it is not optional
// plumbing: a scrubbed tool failure tells the untrusted client nothing about
// its cause, so the one place that cause survives is this logger. A nil one
// falls back to slog.Default(), which in a process that never called
// SetDefault means the record is written somewhere nobody is reading.
func NewHTTPHandler(registry *Registry, authenticate func(*http.Request) (context.Context, error), challenge func(*http.Request) string, name, version string, log *slog.Logger, opts ...HTTPOption) http.Handler {
	server := NewDispatcher(registry, bindAuthenticated, name, version).WithLogger(log)
	for _, opt := range opts {
		opt(server)
	}
	return &httpMCPHandler{
		server:       server,
		authenticate: authenticate,
		challenge:    challenge,
	}
}

// HTTPOption configures the dispatcher behind the HTTP transport. What it
// carries is composed by OTHER modules and injected at the composition edge,
// so it is variadic rather than a positional parameter: a caller that mounts
// no such module does not have to name one.
type HTTPOption func(*Dispatcher)

// WithResourceProvider publishes read-only documents beside the tool surface.
func WithResourceProvider(provider mcp.ResourceProvider) HTTPOption {
	return func(d *Dispatcher) { d.WithResources(provider) }
}

// WithHeldViews tells the tool surface which `ui://` documents are actually
// being served, so a tool never names one this deployment does not publish.
func WithHeldViews(held func(uri string) bool) HTTPOption {
	return func(d *Dispatcher) { d.viewHeld = held }
}

// WithTaskStore turns the io.modelcontextprotocol/tasks extension on: a staged
// confirm-first call answers a durable handle to a client that declared it, and
// the three task methods start being served. Without it the surface behaves
// exactly as it did before — the extension is not advertised, and a 🟡 refusal
// is the same sentence for every client.
func WithTaskStore(tasks Tasks, approvals TaskApprovals) HTTPOption {
	return func(d *Dispatcher) { d.WithTasks(tasks, approvals) }
}

// bindAuthenticated is the Binder the shared dispatcher gets on this
// transport: the edge already authenticated THIS request and the context it
// produced is the one dispatch runs on, so there is nothing left to bind. The
// stdio transport's binder authenticates instead, because its session is one
// long-lived pipe rather than one request.
func bindAuthenticated(ctx context.Context) (context.Context, error) { return ctx, nil }

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
	if errors.Is(err, ErrAuthUnavailable) {
		// The server could not REACH a verdict on the credential, so it must
		// not imply one. A 401 here tells a client its token is bad: a
		// well-behaved one then discards a perfectly good token and re-runs the
		// whole OAuth dance against a server that is down, turning an outage
		// into mass re-consent. 503 is the honest answer, and the only one a
		// client retries.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusServiceUnavailable,
			Code:   "authentication_unavailable",
			Detail: "This server cannot verify credentials right now. Retry with the same token.",
		})
		return
	}
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
		// NEITHER era has a session to tear down, and the answer is the same
		// sentence for both. `Mcp-Session-Id` is optional in the handshake era
		// and this server no longer mints one, so a legacy client has nothing
		// to name here either — and telling it so beats a 404 it would read as
		// "your session expired" and re-handshake over.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusMethodNotAllowed,
			Code:   "method_not_allowed",
			Detail: "This server establishes no session, so there is none to close.",
		})
		return
	}
	h.servePost(w, r)
}

// servePost handles the one JSON-RPC exchange a POST carries: parse, decide
// which framing the request is in, hold it to that framing's preconditions,
// and dispatch.
//
// The era is decided HERE and nowhere else, and it travels down as a value.
// Deciding it twice is how the two framings would start to disagree about a
// request, and the framing decides how a call is parsed — never what it may
// do, which is the registry's business either way.
func (h *httpMCPHandler) servePost(w http.ResponseWriter, r *http.Request) {
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
		// The body is what normally decides the era, and this one does not
		// decode, so the header is the only thing left that can say. A caller
		// that named the modern revision gets the status that framing pins for
		// a malformed request; anything else keeps the answer it always had.
		status := http.StatusOK
		if servesAsModern(declaredTransportVersion(r.Header)) {
			status = http.StatusBadRequest
		}
		writeRPCResponse(w, r,
			rpcResponse{JSONRPC: jsonRPCVersion, Error: &rpcError{Code: codeParseError, Message: "parse error"}},
			status)
		return
	}
	// Before either era looks at it: a version header sent twice has two
	// readings, and no path here acts on one. The answer is a JSON-RPC error
	// rather than a plain 4xx so a modern client recognizes it as this server
	// refusing rather than as a legacy server to fall back to.
	if duplicatedVersionHeader(r.Header) {
		writeRPCResponse(w, r, rpcResponse{
			JSONRPC: jsonRPCVersion, ID: req.ID,
			Error: &rpcError{
				Code: codeHeaderMismatch,
				Message: "header mismatch: " + headerProtocolVersion + " was sent more than once, so this " +
					"server and an intermediary between us could read different versions from it",
			},
		}, http.StatusBadRequest)
		return
	}
	fr, refusal := modernPrecheck(req.Params, declaredTransportVersion(r.Header))
	if refusal == nil && fr.modern {
		refusal = validateModernHeaders(r.Header, req, fr)
	}
	if refusal != nil {
		// Every modern precondition failure is a 400 carrying a recognized
		// JSON-RPC error, which is the pair a dual-era client reads: the status
		// alone would send it back to the handshake, and the body alone would
		// not be seen.
		writeRPCResponse(w, r, rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Error: refusal},
			http.StatusBadRequest)
		return
	}
	if !fr.modern && !h.legacyVersionServed(w, r, req) {
		return
	}
	if req.ID == nil {
		// A notification gets no response by JSON-RPC rule — but it is judged
		// by the same framing rules first, which is why this sits below them.
		// The 2026-07-28 revision leaves a notification's header requirements
		// undefined and defines no client-to-server notification over this
		// transport at all, so no conforming client reaches here; holding one
		// to the request rules is the conservative reading, and inventing a
		// laxer path would be a second way in.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	h.exchange(w, r, req, fr)
}

// legacyVersionServed refuses a handshake-era request that names a revision
// outside the compatibility window, and reports whether the caller may
// proceed.
//
// The refusal names every revision this server serves, in both eras, so the
// client retries on one of them rather than guessing — which it can only do
// because this server does answer the modern framing a dual-era client would
// retry with. initialize is exempt: it negotiates through its own body, and a
// client has no version to put in this header until initialize has answered
// one.
func (h *httpMCPHandler) legacyVersionServed(w http.ResponseWriter, r *http.Request, req rpcRequest) bool {
	v := r.Header.Get(headerProtocolVersion)
	if req.Method == methodInitialize || v == "" || slices.Contains(legacyProtocolVersions, v) {
		return true
	}
	writeRPCResponse(w, r,
		rpcResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Error: unsupportedProtocolVersion(v)},
		http.StatusBadRequest)
	return false
}

// exchange dispatches one request and writes its answer under the status its
// framing pins.
func (h *httpMCPHandler) exchange(w http.ResponseWriter, r *http.Request, req rpcRequest, fr framing) {
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
	ctx := r.Context()
	resp := h.server.handle(ctx, req, fr)
	status := http.StatusOK
	if fr.modern {
		status = modernStatus(resp)
	}
	// NEITHER era is answered with an Mcp-Session-Id. A modern call carries its
	// own state by construction; a handshake-era `initialize` used to mint one,
	// and the id it minted was never authority — every request re-authenticates
	// on its Bearer passport, and the volume the session implicitly bounded is
	// now bounded per Passport in Redis (ADR-0092 §6). What the header cost was
	// real: it pinned a conversation to the process that answered initialize.
	writeRPCResponse(w, r, resp, status)
}

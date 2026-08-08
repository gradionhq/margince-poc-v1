// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The extension REST surface. An extension's contract fragment declares
// operations under /v1/ext/<unit>/…; this is where those declarations become
// mounted routes.
//
// They are NOT in ServerInterface, and cannot be: crmcontracts is generated
// from the BASE contract, which is installation-independent by design, while an
// extension operation exists only in the merged one. So the compile-error
// guarantee core routes have — `var _ crmcontracts.ServerInterface = Server{}`
// in server.go, which makes a declared operation with no handler a build
// failure — is unavailable here. extparity_test.go is the runtime equivalent,
// and it has to fail in BOTH directions: a declared verb with no registration
// is a documented route answering 404, and a registration nothing declares is
// a reachable endpoint no contract, client or manifest knows about.
//
// The routes mount on the UNIT'S OWN router — a dedicated *http.ServeMux — and
// that router is spliced into the /v1 chain INSIDE the session middleware
// (routes.go). Mounting them on the operational mux instead would be a
// security defect rather than a style choice: ServeMux prefers the longest
// matching pattern, so a "/v1/ext/" registration there would win over the
// "/v1/" entry that carries authH.Middleware, and every extension route would
// serve without a session or a workspace.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// toolInvoker is the seam the mounted routes call: the SAME
// agents.Registry.Invoke the MCP transport uses, so a REST call and a
// tools/call of one verb pass the identical admission gate, spend the
// identical cap, and cannot diverge on who may run what.
type toolInvoker func(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error)

// MountedRoute is one mounted extension pattern and the state it was mounted
// in. The state matters to the parity sweep, which has THREE cases to tell
// apart, not two:
//
//   - declared, mounted, handled — a unit's Go Tools entry backs the verb;
//   - declared, mounted, unimplemented — a contract-only governed request
//     (fixtures/extensions/crm-hello's hello_ping is exactly this), legitimate
//     and answered 501;
//   - declared and missing — the defect the sweep exists to catch.
//
// Collapsing the first two is what let a published route reach the registry for
// a verb nothing registered, and answer an opaque logged 500.
type MountedRoute struct {
	Pattern string
	Verb    extension.Verb
	// Implemented reports whether a served tool stands behind the verb, i.e.
	// whether the unit shipped a Handle for it.
	Implemented bool
}

// maxExtensionRequestBody bounds the argument document a mounted route reads.
// A tool's arguments are a small JSON object by construction (the declared
// input schema is one), and the body is fully buffered before the handler runs,
// so an unbounded read would be a per-request memory cost any authenticated
// seat could set.
const maxExtensionRequestBody = 1 << 20 // 1 MiB

// MountExtensionRoutes mounts one route per declared extension operation onto
// mux and reports the patterns it registered.
//
// The signature takes VERBS, not []extension.Extension as the slice plan
// sketched. After the narrowing there is nothing in an extension.Extension a
// route could be derived from: a Tool is {Name, Handle}, and the route, the
// method and the operation id live in the contract. Passing the declarations
// this mounts from is the difference between a seam whose inputs are visible
// and one that reads a package variable.
//
// served names the verbs a composed unit actually registered a handler for,
// keyed by (unit, tool) — compose's composedServedVerbs at the call site, built
// on the same verbKey the behavior-to-contract join uses. A declared verb
// outside it is still MOUNTED — the contract publishes the route either way,
// and a 404 would contradict the document a client generated its call from —
// but it answers a named 501 instead of reaching a registry that has never
// heard of it.
//
// The PAIR, never the bare verb: an `x-mcp-tool` value is a string a unit
// writes into its own contract fragment, so a bare-verb key let unit B declare
// a contract-only operation naming unit A's served verb and inherit A's
// handler — B's published route executing A's tier, scope, RBAC object and
// schemas. See composedServedVerbs.
//
// Returned routes are the registration side of the parity pair. They are
// returned rather than recorded in a package variable because a ServeMux cannot
// be enumerated: if the caller did not learn what was mounted, nothing could
// ever check the two directions against each other. See extparity_test.go for
// the residual that remains even so.
func MountExtensionRoutes(mux *http.ServeMux, verbs []extension.Verb, served map[string]bool, invoke toolInvoker) ([]MountedRoute, error) {
	if invoke == nil {
		return nil, errors.New("compose: extension routes need a tool registry to invoke through — mounting them without one would publish routes that answer nothing")
	}
	mounted := make([]MountedRoute, 0, len(verbs))
	seen := make(map[string]extension.Name, len(verbs))
	for _, v := range verbs {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("compose: extension %q: %w", v.Unit, err)
		}
		// Method-and-path patterns (Go 1.22's ServeMux), so a declared POST
		// route answers 405 rather than 200 on a GET — the contract said POST.
		//
		// ServedPath, never Route: a contract path is relative to the
		// contract's own servers url (which ends in /v1) and this mux serves a
		// bare host, so the base path is put back HERE — the one place it is,
		// and the reason Verb carries the contract's spelling rather than two
		// spellings that could disagree.
		pattern := v.Method + " " + v.ServedPath()
		if owner, dup := seen[pattern]; dup {
			// ServeMux PANICS on a duplicate pattern. That panic would be a
			// boot crash naming a path and nothing else; this names both units.
			return nil, fmt.Errorf("compose: extensions %q and %q both declare %s", owner, v.Unit, pattern)
		}
		seen[pattern] = v.Unit
		implemented := served[verbKey(v.Unit, v.Tool)]
		mux.Handle(pattern, extensionRouteHandler(v, implemented, invoke))
		mounted = append(mounted, MountedRoute{Pattern: pattern, Verb: v, Implemented: implemented})
	}
	return mounted, nil
}

// extensionRouteHandler serves one declared operation by invoking its tool.
//
// A REST extension route is deliberately NOT a second execution path. It reads
// the body, hands it to the registry, and writes what comes back — every
// authority decision (scope, tier, staging, row scope) is made inside Invoke,
// by the same gate the MCP transport goes through. That is what keeps "an
// extension gets one governed surface" true rather than "two surfaces that
// agree today".
func extensionRouteHandler(v extension.Verb, implemented bool, invoke toolInvoker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !implemented {
			// A contract-only declaration is a legitimate state, not an error:
			// the unit published the operation and shipped no behavior for it,
			// which is what a governed request awaiting an operator's decision
			// looks like. Answered HERE rather than left to reach the registry,
			// because agents.UnknownToolError has no httperr classifier — it
			// would become an opaque 500 plus an "unhandled error" log line on
			// every call, and an operator reading that log would be looking for
			// a fault that does not exist.
			//
			// 501, not 404: the merged contract publishes this route, so a
			// client generated a call for it from a document that is telling the
			// truth. "Not implemented" is what is actually the case.
			httperr.NotImplemented(w, r, v.OperationID)
			return
		}
		args, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExtensionRequestBody))
		if err != nil {
			httperr.Write(w, r, httperr.Validation("body", "malformed_json", "the request body could not be read"))
			return
		}
		if len(args) == 0 {
			// The declared input schema is an object, so an absent body is the
			// empty object rather than a refusal: a tool taking no arguments is
			// callable with no body.
			args = json.RawMessage(`{}`)
		}
		if !json.Valid(args) {
			httperr.Write(w, r, httperr.Validation("body", "malformed_json", "the request body is not valid JSON"))
			return
		}
		// The bare verb is the right key HERE, unlike the served lookup above:
		// the registry's namespace is global and buildExtensionTools refuses two
		// units serving one name, so a verb resolves to at most one handler. What
		// the (unit, tool) check bought is that this line is only reached by the
		// unit that owns the name — a route belonging to anyone else answered 501
		// before getting here.
		out, err := invoke(r.Context(), v.Tool, args)
		if err != nil {
			// Straight through httperr: Invoke's refusals are already the
			// product's own problem+json vocabulary (an admission denial, a
			// staged approval, a validation error), and re-wrapping them here
			// would give an extension route a different refusal shape from the
			// core route beside it.
			httperr.Write(w, r, err)
			return
		}
		// The DECLARED payload, not the governed-tool envelope Invoke sealed it
		// into. This is the seam's one translation and it is load-bearing.
		//
		// Invoke answers every tool with `{schema_version, trace_id, freshness,
		// trust, evidence, warnings, data}`, because an AGENT needs the answer
		// and the provenance of the answer in one object. A REST client reads
		// the CONTRACT, and the contract says the 200 body is whatever the
		// unit's own `responses.200` schema declares — the same schema the
		// generated client types, the docs and any SDK are built from. Writing
		// the envelope there made the published document a lie: every read the
		// composed SPA performed came back `undefined`, and no gate saw it
		// because the types agreed with the contract and the tests stubbed the
		// contract's shape. Task 14's UAT found it by clicking (F1).
		//
		// Unwrapping HERE rather than declaring the envelope in the contract,
		// for two reasons that both cut the same way:
		//   - a unit author would otherwise have to write the envelope into
		//     every response schema they ever declare, forever, to describe
		//     transport they do not own;
		//   - gen-composition reads that same 200 schema and emits it as the
		//     MCP tool's OutputSchema, so the envelope would be advertised to
		//     models as part of the tool's RESULT — describing the wrapper as
		//     if it were the answer.
		// The envelope stays on the agent path, which is where the trust tier
		// and the evidence set are the point.
		payload, trace, err := unwrapToolEnvelope(out)
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		// The trace id is the one part of the envelope a REST caller genuinely
		// loses, and it is the handle that makes a call findable in the audit
		// log. It moves to a header rather than into the body, because the body
		// belongs to the unit's declared schema and nothing else may appear in
		// it.
		if trace != "" {
			w.Header().Set(extensionTraceHeader, trace)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//craft:ignore swallowed-errors WriteHeader already committed the response — nothing can report a write failure to the client anymore
		_, _ = w.Write(payload)
	})
}

// extensionTraceHeader carries the governed call's correlation id on the REST
// surface. `X-` prefixes are deprecated (RFC 6648) and the product's own header
// vocabulary is unprefixed.
const extensionTraceHeader = "Trace-Id"

// unwrapToolEnvelope takes the unit's own payload out of the sealed result.
//
// It is strict about the ENVELOPE and permissive about the PAYLOAD. The
// envelope is this codebase's own invariant — Registry.Invoke seals every
// successful result — so bytes that are not one mean the seam changed
// underneath this function, and answering with them anyway is how the original
// defect looked from the outside: a 200 whose body is not what the contract
// promised. The payload is the unit's business and is passed through untouched,
// `null` included.
func unwrapToolEnvelope(sealed json.RawMessage) (payload json.RawMessage, trace string, err error) {
	var envelope struct {
		TraceID string          `json:"trace_id"`
		Data    json.RawMessage `json:"data"`
	}
	if jsonErr := json.Unmarshal(sealed, &envelope); jsonErr != nil || envelope.Data == nil {
		return nil, "", fmt.Errorf("compose: the tool registry answered with something that is not a sealed envelope, "+
			"so this route has no payload to serve at the shape its contract declares: %w", errors.ErrUnsupported)
	}
	return envelope.Data, envelope.TraceID, nil
}

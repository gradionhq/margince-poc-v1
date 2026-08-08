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
// Returned patterns are the registration side of the parity pair. They are
// returned rather than recorded in a package variable because a ServeMux cannot
// be enumerated: if the caller did not learn what was mounted, nothing could
// ever check the two directions against each other.
func MountExtensionRoutes(mux *http.ServeMux, verbs []extension.Verb, invoke toolInvoker) ([]string, error) {
	if invoke == nil {
		return nil, errors.New("compose: extension routes need a tool registry to invoke through — mounting them without one would publish routes that answer nothing")
	}
	patterns := make([]string, 0, len(verbs))
	seen := make(map[string]extension.Name, len(verbs))
	for _, v := range verbs {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("compose: extension %q: %w", v.Unit, err)
		}
		// Method-and-path patterns (Go 1.22's ServeMux), so a declared POST
		// route answers 405 rather than 200 on a GET — the contract said POST.
		pattern := v.Method + " " + v.Route
		if owner, dup := seen[pattern]; dup {
			// ServeMux PANICS on a duplicate pattern. That panic would be a
			// boot crash naming a path and nothing else; this names both units.
			return nil, fmt.Errorf("compose: extensions %q and %q both declare %s", owner, v.Unit, pattern)
		}
		seen[pattern] = v.Unit
		mux.Handle(pattern, extensionRouteHandler(v, invoke))
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// extensionRouteHandler serves one declared operation by invoking its tool.
//
// A REST extension route is deliberately NOT a second execution path. It reads
// the body, hands it to the registry, and writes what comes back — every
// authority decision (scope, tier, staging, row scope) is made inside Invoke,
// by the same gate the MCP transport goes through. That is what keeps "an
// extension gets one governed surface" true rather than "two surfaces that
// agree today".
func extensionRouteHandler(v extension.Verb, invoke toolInvoker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// The tool's own result bytes, verbatim. Not re-encoded through a Go
		// type: the response shape is the CONTRACT's (the operation's declared
		// 200 schema), and a struct here would be a third statement of it that
		// could disagree with both the contract and the handler.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//craft:ignore swallowed-errors WriteHeader already committed the response — nothing can report a write failure to the client anymore
		_, _ = w.Write(out)
	})
}

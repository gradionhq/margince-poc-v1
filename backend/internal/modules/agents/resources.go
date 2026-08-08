// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// resources/list and resources/read — the read-only half of the surface.
//
// A resource takes no arguments and changes nothing, so it does not ride the
// tool admission gate. What it does ride is the caller's own context: every
// provider composes its document from what THIS principal may read, which is
// what keeps a resource from becoming the discovery channel the scope-filtered
// tool list is careful not to be.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// resourceNotFound is the protocol's code for a URI the server does not
// serve. It is also what a URI the CALLER cannot see answers, deliberately
// indistinguishable — the same existence-hiding the record surface applies.
const resourceNotFound = -32002

// resourceDescriptor is one resources/list entry on the wire.
type resourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	//nolint:tagliatelle // mimeType is the MCP wire member, camelCase by the protocol
	MIMEType string `json:"mimeType"`
}

// resourceContents is one resources/read result: the protocol carries a list
// of blocks, and this server always answers with exactly one.
type resourceContents struct {
	Contents []resourceContentBlock `json:"contents"`
}

type resourceContentBlock struct {
	URI string `json:"uri"`
	//nolint:tagliatelle // mimeType is the MCP wire member, camelCase by the protocol
	MIMEType string `json:"mimeType"`
	Text     string `json:"text"`
}

// resourceList advertises what this caller may read. A server with no
// provider answers an empty list rather than an error: an empty catalog is a
// legitimate state, and a client that calls resources/list right after
// initialize should not read it as a broken server.
func (s *Dispatcher) resourceList(ctx context.Context) []resourceDescriptor {
	if s.resources == nil {
		return []resourceDescriptor{}
	}
	published := s.resources.Resources(ctx)
	out := make([]resourceDescriptor, 0, len(published))
	for _, r := range published {
		if !readableByCaller(ctx, r) {
			continue
		}
		out = append(out, resourceDescriptor{
			URI: r.URI, Name: r.Name, Title: r.Title,
			Description: r.Description, MIMEType: r.MIMEType,
		})
	}
	return out
}

// readResource answers one document, or a protocol error — never both, which
// is why the caller assigns them on separate branches.
func (s *Dispatcher) readResource(ctx context.Context, params json.RawMessage) (resourceContents, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return resourceContents{}, &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
	}
	// An absent, null or empty uri is a request this server could not read,
	// not a resource that is missing — a different thing for the caller to
	// fix, and "" can never name a document any provider serves.
	if p.URI == "" {
		return resourceContents{}, &rpcError{Code: codeInvalidParams, Message: "invalid params: resources/read needs a non-empty \"uri\""}
	}
	if s.resources == nil {
		return resourceContents{}, &rpcError{Code: resourceNotFound, Message: "no resource at " + p.URI}
	}
	if !s.scopeAdmitsRead(ctx, p.URI) {
		// The same answer an unknown URI gets: a caller whose scopes do not
		// reach a document must not learn that it exists.
		return resourceContents{}, &rpcError{Code: resourceNotFound, Message: "no resource at " + p.URI}
	}
	contents, err := s.resources.ReadResource(ctx, p.URI)
	if errors.Is(err, apperrors.ErrNotFound) {
		return resourceContents{}, &rpcError{Code: resourceNotFound, Message: "no resource at " + p.URI}
	}
	if err != nil {
		// The cause is server-side knowledge (a pool fault, a wrapped SQL
		// error); the client learns only that the read did not happen.
		s.log.Error("mcp: reading resource", "uri", p.URI, "err", err)
		return resourceContents{}, &rpcError{Code: codeInternalError, Message: "the resource could not be read; retry, and if it persists ask an administrator to check the server logs"}
	}
	return resourceContents{Contents: []resourceContentBlock{{
		URI: contents.URI, MIMEType: contents.MIMEType, Text: contents.Text,
	}}}, nil
}

// readableByCaller reports whether the calling principal's passport scopes
// reach this document. It mirrors the scope arm of the tool surface's own
// filter (invocableByCaller) deliberately: a resource is a read, and a
// surface that advertises what it will then refuse is a surface that lies.
//
// Humans and the system principal do not ride the scope model — their
// authority is their RBAC, which the provider itself applies — so filtering
// them by a passport scope they never carry would hide the whole catalogue.
// A ctx with no principal shows nothing, which is the honest answer for a
// caller that never authenticated.
func readableByCaller(ctx context.Context, resource mcp.Resource) bool {
	p, ok := principal.Actor(ctx)
	if !ok {
		return false
	}
	if p.Type != principal.PrincipalAgent {
		return true
	}
	return p.Scopes.Has(resource.RequiredScope)
}

// scopeAdmitsRead answers whether this caller may read the named URI, by
// asking the provider what it publishes and applying the same scope filter
// the catalogue does. Going through the published set rather than a separate
// per-URI lookup is what keeps the two answers from drifting: a document the
// catalogue hides can never be readable.
func (s *Dispatcher) scopeAdmitsRead(ctx context.Context, uri string) bool {
	for _, r := range s.resources.Resources(ctx) {
		if r.URI == uri {
			return readableByCaller(ctx, r)
		}
	}
	// Not published at all — ReadResource answers its own not-found, and this
	// filter has nothing to say about a URI no provider claims.
	return true
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// stubResources stands in for whatever module composes a document. It is a
// seam, which is what a stub is for; nothing here asserts how it was called.
type stubResources struct {
	published []mcp.Resource
	contents  map[string]mcp.ResourceContents
	err       error
}

func (s stubResources) Resources(context.Context) []mcp.Resource { return s.published }

func (s stubResources) ReadResource(_ context.Context, uri string) (mcp.ResourceContents, error) {
	if s.err != nil {
		return mcp.ResourceContents{}, s.err
	}
	contents, ok := s.contents[uri]
	if !ok {
		return mcp.ResourceContents{}, apperrors.ErrNotFound
	}
	return contents, nil
}

func dispatcherWith(provider mcp.ResourceProvider) *Dispatcher {
	d := NewDispatcher(NewRegistry(nil, nil), bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
	if provider != nil {
		d = d.WithResources(provider)
	}
	return d
}

// agentHolding is the context an authenticated passport call arrives on: the
// resource surface is scope-filtered for agents exactly as the tool list is,
// so a test that posted an actor-less context would be asserting against an
// empty surface for the wrong reason.
func agentHolding(scopes ...principal.Scope) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:   principal.PrincipalAgent,
		ID:     "agent:test",
		Scopes: principal.NewScopeSet(scopes...),
	})
}

func rpc(t *testing.T, d *Dispatcher, method string, params string) rpcResponse {
	t.Helper()
	return rpcAs(agentHolding(principal.ScopeRead), t, d, method, params)
}

func rpcAs(ctx context.Context, t *testing.T, d *Dispatcher, method string, params string) rpcResponse {
	t.Helper()
	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return d.handle(ctx, req, legacyFraming)
}

// A wired provider's documents are advertised; the catalogue is what a client
// reads instead of probing for URIs.
func TestResourcesListAdvertisesTheWiredProvider(t *testing.T) {
	d := dispatcherWith(stubResources{published: []mcp.Resource{{
		URI: "margince://schema/query", Name: "query_vocabulary", Title: "Workspace query vocabulary",
		Description: "what you may ask", MIMEType: "application/json",
		RequiredScope: principal.ScopeRead,
	}}})

	result := decodeResult[resourceListResult](t, rpc(t, d, "resources/list", ""))
	if len(result.Resources) != 1 {
		t.Fatalf("resources/list returned %d entries", len(result.Resources))
	}
	r := result.Resources[0]
	if r.URI == "" || r.Name == "" || r.Title == "" || r.Description == "" || r.MIMEType == "" {
		t.Errorf("advertised resource is incomplete on the wire: %+v", r)
	}
}

// resourceListResult and resourceReadResult decode what a CLIENT receives —
// deliberately declared here rather than reusing the production types, so a
// renamed json member fails these tests instead of travelling with them.
type resourceListResult struct {
	Resources []struct {
		URI         string `json:"uri"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		//nolint:tagliatelle // mimeType is the MCP wire member, camelCase by the protocol
		MIMEType string `json:"mimeType"`
	} `json:"resources"`
}

type resourceReadResult struct {
	Contents []struct {
		URI string `json:"uri"`
		//nolint:tagliatelle // mimeType is the MCP wire member, camelCase by the protocol
		MIMEType string `json:"mimeType"`
		Text     string `json:"text"`
	} `json:"contents"`
}

// With no provider the catalogue is empty rather than an error: claude.ai
// calls resources/list right after initialize, and a -32601 there reads as a
// broken server rather than an empty, valid catalog.
func TestResourcesListIsEmptyRatherThanAnErrorWithNoProvider(t *testing.T) {
	resp := rpc(t, dispatcherWith(nil), "resources/list", "")
	if resp.Error != nil {
		t.Fatalf("resources/list → error %d %q", resp.Error.Code, resp.Error.Message)
	}
	result := decodeResult[resourceListResult](t, resp)
	if len(result.Resources) != 0 {
		t.Errorf("a server with no provider advertised %d resources", len(result.Resources))
	}
}

// The document comes back as the protocol's contents block, with the URI it
// was asked for.
func TestResourcesReadAnswersTheDocument(t *testing.T) {
	d := dispatcherWith(stubResources{contents: map[string]mcp.ResourceContents{
		"margince://schema/query": {URI: "margince://schema/query", MIMEType: "application/json", Text: `{"version":"v1"}`},
	}})

	result := decodeResult[resourceReadResult](t, rpc(t, d, "resources/read", `{"uri":"margince://schema/query"}`))
	if len(result.Contents) != 1 {
		t.Fatalf("resources/read returned %d content blocks", len(result.Contents))
	}
	if result.Contents[0].URI != "margince://schema/query" || result.Contents[0].Text != `{"version":"v1"}` {
		t.Errorf("content block came back as %+v", result.Contents[0])
	}
}

// A URI the server does not serve and one the CALLER cannot see answer
// identically, which is the existence-hiding the record surface applies.
func TestAnUnservedURIIsResourceNotFound(t *testing.T) {
	for name, d := range map[string]*Dispatcher{
		"no provider wired": dispatcherWith(nil),
		"provider has no such document": dispatcherWith(stubResources{
			contents: map[string]mcp.ResourceContents{"margince://schema/query": {}},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			resp := rpc(t, d, "resources/read", `{"uri":"margince://schema/nothing"}`)
			if resp.Error == nil || resp.Error.Code != resourceNotFound {
				t.Fatalf("read of an unserved URI answered %+v; want code %d", resp.Error, resourceNotFound)
			}
			if resp.Result != nil {
				t.Error("a failed read carried a result as well as an error")
			}
		})
	}
}

// A provider fault reaches the untrusted client scrubbed: it learns the read
// did not happen and nothing about why.
func TestAProviderFaultIsScrubbedBeforeItReachesTheClient(t *testing.T) {
	d := dispatcherWith(stubResources{err: errors.New("pgx: dial tcp 10.0.0.5:5432: connection refused")})
	resp := rpc(t, d, "resources/read", `{"uri":"margince://schema/query"}`)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("provider fault answered %+v; want an internal error", resp.Error)
	}
	for _, leak := range []string{"pgx", "10.0.0.5", "connection refused"} {
		if strings.Contains(resp.Error.Message, leak) {
			t.Errorf("the scrubbed error leaks %q: %s", leak, resp.Error.Message)
		}
	}
}

// Malformed params are a protocol error, not a not-found: the client sent a
// request this server could not read, which is a different fix.
func TestMalformedResourceParamsAreAProtocolError(t *testing.T) {
	resp := rpc(t, dispatcherWith(stubResources{}), "resources/read", `{"uri":`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("malformed params answered %+v; want -32602", resp.Error)
	}
}

// The capability is advertised only when something is behind it: claiming
// resources with no provider sends a client to a read that can only fail.
func TestTheResourcesCapabilityIsAdvertisedOnlyWhenItIsReal(t *testing.T) {
	for name, tc := range map[string]struct {
		provider mcp.ResourceProvider
		want     bool
	}{
		"with a provider": {provider: stubResources{}, want: true},
		"with none":       {provider: nil, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			result := decodeResult[initializeResult](t,
				rpc(t, dispatcherWith(tc.provider), methodInitialize, `{"protocolVersion":"2025-11-25"}`))
			_, advertised := result.Capabilities["resources"]
			if advertised != tc.want {
				t.Errorf("resources capability advertised = %v, want %v", advertised, tc.want)
			}
			if _, ok := result.Capabilities["tools"]; !ok {
				t.Error("the tools capability stopped being advertised")
			}
		})
	}
}

// decodeResult round-trips a dispatcher result through JSON, which is the
// only thing a client ever sees of it.
// initializeResult reads only the capability map, which is all these tests
// judge.
type initializeResult struct {
	Capabilities map[string]json.RawMessage `json:"capabilities"`
}

func decodeResult[T any](t *testing.T, resp rpcResponse) T {
	t.Helper()
	var into T
	if resp.Error != nil {
		t.Fatalf("call failed: %d %q", resp.Error.Code, resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &into); err != nil {
		t.Fatal(err)
	}
	return into
}

// A resource is a read, and an agent whose passport does not carry the read
// scope must not learn what the workspace holds — not even the NAMES of its
// custom columns. The catalogue hides it and the read answers not-found, the
// same pair an unknown URI gets, so scope cannot be probed either.
func TestAPassportWithoutTheScopeNeitherSeesNorReadsTheDocument(t *testing.T) {
	d := dispatcherWith(readScopedProvider())

	listed := decodeResult[resourceListResult](t,
		rpcAs(agentHolding(principal.ScopeDraft), t, d, "resources/list", ""))
	if len(listed.Resources) != 0 {
		t.Errorf("a draft-only passport is advertised %d read-scoped resources", len(listed.Resources))
	}

	resp := rpcAs(agentHolding(principal.ScopeDraft), t, d, "resources/read",
		`{"uri":"margince://schema/query"}`)
	if resp.Error == nil || resp.Error.Code != resourceNotFound {
		t.Fatalf("out-of-scope read answered %+v; want the same not-found an unknown URI gets", resp.Error)
	}
	if resp.Result != nil {
		t.Error("an out-of-scope read carried a result")
	}
}

// The holder of the scope gets the document, so the filter above is a filter
// and not an outage.
func TestAPassportHoldingTheScopeSeesAndReadsTheDocument(t *testing.T) {
	d := dispatcherWith(readScopedProvider())

	listed := decodeResult[resourceListResult](t,
		rpcAs(agentHolding(principal.ScopeRead), t, d, "resources/list", ""))
	if len(listed.Resources) != 1 {
		t.Fatalf("a read passport is advertised %d resources; want 1", len(listed.Resources))
	}
	read := decodeResult[resourceReadResult](t,
		rpcAs(agentHolding(principal.ScopeRead), t, d, "resources/read", `{"uri":"margince://schema/query"}`))
	if len(read.Contents) != 1 {
		t.Fatalf("a read passport got %d content blocks", len(read.Contents))
	}
}

// A human does not ride the scope model at all — their authority is their
// RBAC, which the PROVIDER applies when it composes the document. Filtering
// them by a passport scope they never carry would hide the whole catalogue.
func TestAHumanIsNotFilteredByAPassportScope(t *testing.T) {
	human := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
	})
	listed := decodeResult[resourceListResult](t,
		rpcAs(human, t, dispatcherWith(readScopedProvider()), "resources/list", ""))
	if len(listed.Resources) != 1 {
		t.Errorf("a human is advertised %d resources; want the whole catalogue", len(listed.Resources))
	}
}

// A caller that never authenticated sees nothing, which is the honest answer
// for a principal-less context.
func TestAnUnauthenticatedCallerSeesNoResources(t *testing.T) {
	listed := decodeResult[resourceListResult](t,
		rpcAs(context.Background(), t, dispatcherWith(readScopedProvider()), "resources/list", ""))
	if len(listed.Resources) != 0 {
		t.Errorf("a caller with no principal is advertised %d resources", len(listed.Resources))
	}
}

func readScopedProvider() stubResources {
	return stubResources{
		published: []mcp.Resource{{
			URI: "margince://schema/query", Name: "query_vocabulary", Title: "Workspace query vocabulary",
			Description: "what you may ask", MIMEType: "application/json",
			RequiredScope: principal.ScopeRead,
		}},
		contents: map[string]mcp.ResourceContents{
			"margince://schema/query": {URI: "margince://schema/query", MIMEType: "application/json", Text: `{"version":"v1"}`},
		},
	}
}

// An absent, null or empty uri is a request this server could not read, not a
// resource that is missing — a different thing for the caller to fix. The
// boundary between the two answers is what this pins.
func TestAnEmptyURIIsInvalidParamsRatherThanNotFound(t *testing.T) {
	d := dispatcherWith(readScopedProvider())
	for name, params := range map[string]string{
		"an empty uri":  `{"uri":""}`,
		"a null uri":    `{"uri":null}`,
		"no uri at all": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := rpc(t, d, "resources/read", params)
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("answered %+v; want -32602, not a not-found", resp.Error)
			}
			if !strings.Contains(resp.Error.Message, "uri") {
				t.Errorf("the refusal does not name what to fix: %q", resp.Error.Message)
			}
			if resp.Result != nil {
				t.Error("a refused read carried a result")
			}
		})
	}
}

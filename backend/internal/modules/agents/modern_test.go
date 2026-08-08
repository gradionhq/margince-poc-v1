// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the 2026-07-28 framing obliges this server to do with a request's body,
// and the property that makes serving two framings safe: they differ in how a
// call is parsed and rendered, and in nothing that decides what it may do.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The wire tokens, spelled here as the specification writes them rather than
// read off the constants the code writes. A test that reads the same constant
// proves only that this server is self-consistent, and a typo in a reserved
// `_meta` key produces a request that looks like it declared nothing — which
// this framing reads as the OTHER era.
func TestTheProtocolTokensAreSpelledAsTheSpecificationWritesThem(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{modernProtocolVersion, "2026-07-28"},
		{metaProtocolVersion, "io.modelcontextprotocol/protocolVersion"},
		{metaClientCapabilities, "io.modelcontextprotocol/clientCapabilities"},
		{metaServerInfo, "io.modelcontextprotocol/serverInfo"},
		{methodDiscover, "server/discover"},
		{headerProtocolVersion, "MCP-Protocol-Version"},
		{headerMethod, "Mcp-Method"},
		{headerName, "Mcp-Name"},
	} {
		if tc.got != tc.want {
			t.Errorf("token = %q, want the protocol's own spelling %q", tc.got, tc.want)
		}
	}
	for _, tc := range []struct {
		got  int
		want int
		name string
	}{
		{codeHeaderMismatch, -32020, "HeaderMismatch"},
		{codeUnsupportedProtocolVersion, -32022, "UnsupportedProtocolVersion"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d — the sub-range is reserved to the specification, "+
				"so a code may only carry the meaning it gives it", tc.name, tc.got, tc.want)
		}
	}
}

// modernMetaJSON is the per-request metadata a conforming modern client sends,
// written out rather than built from the constants for the reason above.
const modernMetaJSON = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
	`"io.modelcontextprotocol/clientCapabilities":{}}`

// modernParams wraps a call's own params in that metadata.
func modernParams(inner string) json.RawMessage {
	if inner == "" {
		return json.RawMessage(`{` + modernMetaJSON + `}`)
	}
	return json.RawMessage(`{` + modernMetaJSON + `,` + inner + `}`)
}

// The era is the ONE question this framing asks first, and both of its inputs
// are load-bearing. A request whose body declares a version is modern. So is
// one whose transport names the modern revision and whose body declares
// nothing — because every intermediary between the client and here routes on
// that header, and reading such a request as legacy would let a caller be
// routed as modern while skipping every modern check.
func TestTheEraIsDecidedByTheBodyOrByTheHeaderThatNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name             string
		params           json.RawMessage
		transportVersion string
		wantModern       bool
		wantRefusalCode  int
	}{
		{"a body that declares the modern revision", modernParams(""), modernProtocolVersion, true, 0},
		{
			"a body that declares it while the transport says nothing",
			modernParams(""), "", true, 0,
		},
		{
			"a transport naming the modern revision over a body that declares nothing",
			json.RawMessage(`{}`), modernProtocolVersion, true, codeInvalidParams,
		},
		{"neither", json.RawMessage(`{}`), "", false, 0},
		{"neither, with no params at all", nil, "", false, 0},
		{
			"a legacy transport version over a legacy body",
			json.RawMessage(`{}`), legacyProtocolVersions[0], false, 0,
		},
		{
			"params that are not an object declare nothing",
			json.RawMessage(`[1,2]`), "", false, 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr, refusal := modernPrecheck(tc.params, tc.transportVersion)

			if fr.modern != tc.wantModern {
				t.Fatalf("modern = %v, want %v", fr.modern, tc.wantModern)
			}
			switch {
			case tc.wantRefusalCode == 0 && refusal != nil:
				t.Fatalf("refused with %d %q, want admission", refusal.Code, refusal.Message)
			case tc.wantRefusalCode != 0 && refusal == nil:
				t.Fatalf("admitted, want refusal %d", tc.wantRefusalCode)
			case refusal != nil && refusal.Code != tc.wantRefusalCode:
				t.Fatalf("refusal code = %d, want %d", refusal.Code, tc.wantRefusalCode)
			}
		})
	}
}

// Both per-request fields are required, and their absence is a malformed
// request rather than an empty value — the distinction the pointer members
// exist to keep.
func TestAModernRequestMustCarryItsVersionAndItsCapabilities(t *testing.T) {
	for _, tc := range []struct{ name, params, wantNamed string }{
		{
			"no capabilities",
			`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`,
			metaClientCapabilities,
		},
		{
			"capabilities but no version, over a modern transport",
			`{"_meta":{"io.modelcontextprotocol/clientCapabilities":{}}}`,
			metaProtocolVersion,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, refusal := modernPrecheck(json.RawMessage(tc.params), modernProtocolVersion)

			if refusal == nil {
				t.Fatal("admitted a request missing a required _meta field")
			}
			if refusal.Code != codeInvalidParams {
				t.Errorf("code = %d, want %d", refusal.Code, codeInvalidParams)
			}
			if !strings.Contains(refusal.Message, tc.wantNamed) {
				t.Errorf("message %q must name the missing field %q", refusal.Message, tc.wantNamed)
			}
		})
	}
}

// A version this server does not serve per-request is refused with the list it
// does serve, so the client retries rather than guesses. An empty
// capabilities object is admitted in the same breath: no tool here needs
// sampling, elicitation or roots, so there is nothing whose absence could
// refuse a caller.
func TestAnUnservedModernVersionIsRefusedWithEveryVersionThisServerServes(t *testing.T) {
	for _, requested := range []string{"2025-11-25", "2025-03-26", "1999-01-01"} {
		t.Run(requested, func(t *testing.T) {
			params := fmt.Sprintf(`{"_meta":{"io.modelcontextprotocol/protocolVersion":%q,`+
				`"io.modelcontextprotocol/clientCapabilities":{}}}`, requested)

			_, refusal := modernPrecheck(json.RawMessage(params), "")

			if refusal == nil || refusal.Code != codeUnsupportedProtocolVersion {
				t.Fatalf("refusal = %#v, want %d", refusal, codeUnsupportedProtocolVersion)
			}
			data, ok := refusal.Data.(map[string]any)
			if !ok {
				t.Fatalf("data = %#v, want the supported/requested pair a client acts on", refusal.Data)
			}
			supported, ok := data["supported"].([]string)
			if !ok || !slices.Equal(supported, supportedProtocolVersions()) {
				t.Errorf("data.supported = %#v, want %v", data["supported"], supportedProtocolVersions())
			}
			if data["requested"] != requested {
				t.Errorf("data.requested = %v, want %q", data["requested"], requested)
			}
		})
	}
}

// supportedProtocolVersions is what a client chooses from, so the modern
// revision leads it and every legacy revision in the window follows.
func TestTheSupportedListLeadsWithTheModernRevisionAndCarriesTheWholeWindow(t *testing.T) {
	got := supportedProtocolVersions()

	want := append([]string{modernProtocolVersion}, legacyProtocolVersions...)
	if !slices.Equal(got, want) {
		t.Fatalf("supported = %v, want %v", got, want)
	}
	if slices.Contains(got, "2025-03-26") {
		t.Error("2025-03-26 left the compatibility window (ADR-0092 §3) and must not be advertised")
	}
}

// modernDispatcher is a server with one read tool, which is enough for every
// rendering question here: the framing decides how an answer is wrapped, not
// what is in it.
func modernDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	registry.Register(echoTool{
		spec: objectSpec("read_record", principal.ScopeRead),
		out:  json.RawMessage(`{"ok":true}`),
	})
	return NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
}

// modernRPC dispatches one modern call and returns the rendered result.
func modernRPC(ctx context.Context, t *testing.T, s *Dispatcher, method string, params json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	fr, refusal := modernPrecheck(params, modernProtocolVersion)
	if refusal != nil {
		t.Fatalf("%s refused before dispatch: %d %q", method, refusal.Code, refusal.Message)
	}
	resp := s.handle(ctx, rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: method, Params: params,
	}, fr)
	if resp.Error != nil {
		t.Fatalf("%s → error %d %q", method, resp.Error.Code, resp.Error.Message)
	}
	body, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshalling the %s result: %v", method, err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		t.Fatalf("the %s result is not a JSON object: %v", method, err)
	}
	return members
}

// Every modern result says what kind of result it is and who answered it —
// including the ones whose payload is empty, because a client reads those
// members before it reads anything else.
func TestEveryModernResultNamesItsTypeAndItsServer(t *testing.T) {
	s := modernDispatcher(t)
	ctx := scopedAgentCtx(principal.ScopeRead)

	for _, method := range append([]string{methodPing, methodDiscover, methodToolsCall}, modernPrivateCatalogs...) {
		t.Run(method, func(t *testing.T) {
			params := modernParams("")
			switch method {
			case methodToolsCall:
				params = modernParams(`"name":"read_record","arguments":{}`)
			case methodResourcesRead:
				// No provider is wired, so this one answers a refusal rather
				// than a result — covered by its own test below.
				t.Skip("resources/read with no provider is a refusal, asserted separately")
			}

			members := modernRPC(ctx, t, s, method, params)

			if got := string(members[fieldResultType]); got != `"`+resultTypeComplete+`"` {
				t.Errorf("%s = %s, want %q", fieldResultType, got, resultTypeComplete)
			}
			var meta map[string]json.RawMessage
			if err := json.Unmarshal(members[fieldMeta], &meta); err != nil {
				t.Fatalf("no _meta on the result: %v", err)
			}
			if !strings.Contains(string(meta[metaServerInfo]), `"margince-crm"`) {
				t.Errorf("_meta[%q] = %s, want this server's identity", metaServerInfo, meta[metaServerInfo])
			}
		})
	}
}

// A cacheable catalog says how long it stays fresh and who may hold it. Every
// catalog this server composes reads the CALLER's own context, so a shared
// cache entry would hand one agent another's surface — a disclosure that never
// reaches the server to be audited (ADR-0092 §5).
func TestEveryCallerDerivedCatalogIsCachedPrivately(t *testing.T) {
	s := modernDispatcher(t)
	ctx := scopedAgentCtx(principal.ScopeRead)

	for _, method := range modernPrivateCatalogs {
		if method == methodResourcesRead {
			continue // a refusal, and refusals carry no hint at all
		}
		t.Run(method, func(t *testing.T) {
			members := modernRPC(ctx, t, s, method, modernParams(""))

			if got := string(members[fieldCacheScope]); got != `"`+cacheScopePrivate+`"` {
				t.Errorf("%s = %s, want %q", fieldCacheScope, got, cacheScopePrivate)
			}
			if got := string(members[fieldTTLMs]); got != fmt.Sprint(catalogCacheTTLMs) {
				t.Errorf("%s = %s, want %d", fieldTTLMs, got, catalogCacheTTLMs)
			}
		})
	}
}

// server/discover is the one response allowed a shared cache, and this is what
// licenses it: the same bytes for every caller. If it ever grows a member
// derived from who asked, this fails rather than a gateway quietly serving one
// agent's answer to another.
func TestDiscoverAnswersEveryCallerIdentically(t *testing.T) {
	s := modernDispatcher(t)
	readOnly := scopedAgentCtx(principal.ScopeRead)
	privileged := principal.WithActor(principal.WithWorkspaceID(context.Background(), ids.NewV7()),
		principal.Principal{
			Type: principal.PrincipalAgent, ID: "agent:privileged", OnBehalfOf: ids.NewV7(),
			Scopes: principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite, principal.ScopeSend),
		})

	first := modernRPC(readOnly, t, s, methodDiscover, modernParams(""))
	second := modernRPC(privileged, t, s, methodDiscover, modernParams(""))
	unauthenticated := modernRPC(context.Background(), t, s, methodDiscover, modernParams(""))

	for _, other := range []map[string]json.RawMessage{second, unauthenticated} {
		for member, value := range first {
			if string(other[member]) != string(value) {
				t.Fatalf("discover.%s differs by caller (%s vs %s) — it is cached %q, "+
					"so a caller-derived member here is a disclosure",
					member, value, other[member], cacheScopePublic)
			}
		}
	}
	if got := string(first[fieldCacheScope]); got != `"`+cacheScopePublic+`"` {
		t.Errorf("%s = %s, want %q", fieldCacheScope, got, cacheScopePublic)
	}
}

// Discovery is what a client reads INSTEAD of probing, so it must name every
// revision this server serves and the same capabilities initialize reports.
func TestDiscoverAdvertisesTheWholeWindowAndTheSameCapabilitiesAsInitialize(t *testing.T) {
	s := modernDispatcher(t)

	members := modernRPC(scopedAgentCtx(principal.ScopeRead), t, s, methodDiscover, modernParams(""))

	var versions []string
	if err := json.Unmarshal(members["supportedVersions"], &versions); err != nil {
		t.Fatalf("supportedVersions: %v", err)
	}
	if !slices.Equal(versions, supportedProtocolVersions()) {
		t.Errorf("supportedVersions = %v, want %v", versions, supportedProtocolVersions())
	}
	handshake, rpcErr := s.initialize(nil)
	if rpcErr != nil {
		t.Fatalf("initialize: %#v", rpcErr)
	}
	fromHandshake, err := json.Marshal(handshake["capabilities"])
	if err != nil {
		t.Fatalf("marshalling the handshake capabilities: %v", err)
	}
	if string(members["capabilities"]) != string(fromHandshake) {
		t.Errorf("discover claims %s and initialize claims %s — one server, one claim",
			members["capabilities"], fromHandshake)
	}
}

// A tool result is not a catalog: it is one caller's answer to one question
// and must never be served twice. The caching contract is a closed set, so a
// method that is not in it carries no hint at all.
func TestAToolResultCarriesNoCachingHint(t *testing.T) {
	s := modernDispatcher(t)

	members := modernRPC(scopedAgentCtx(principal.ScopeRead), t, s, methodToolsCall,
		modernParams(`"name":"read_record","arguments":{}`))

	for _, member := range []string{fieldTTLMs, fieldCacheScope} {
		if _, present := members[member]; present {
			t.Errorf("tools/call result carries %q — a tool answer is not cacheable", member)
		}
	}
}

// Each era owns its opening call. Answering the other era's would tell a
// client it had reached the server it was probing for, which is the one thing
// those two calls exist to settle.
func TestEachEraAnswersOnlyItsOwnOpeningCall(t *testing.T) {
	s := modernDispatcher(t)
	for _, tc := range []struct {
		name   string
		method string
		fr     framing
	}{
		{"initialize in the modern framing", methodInitialize, framing{modern: true, version: modernProtocolVersion}},
		{"server/discover in the handshake framing", methodDiscover, legacyFraming},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.handle(context.Background(), rpcRequest{
				JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: tc.method,
			}, tc.fr)

			if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
				t.Fatalf("error = %#v, want %d method not found", resp.Error, codeMethodNotFound)
			}
			if resp.Result != nil {
				t.Errorf("result = %#v alongside an error, which JSON-RPC forbids", resp.Result)
			}
		})
	}
}

// -32002 was retired with the handshake era and its meaning moved to -32602.
// The legacy framing still answers the code its own clients recognize, so the
// remap is a rendering decision rather than a change at the raise site.
func TestAResourceRefusalCarriesTheCodeItsOwnEraUses(t *testing.T) {
	s := modernDispatcher(t)
	read := func(fr framing) *rpcError {
		return s.handle(scopedAgentCtx(principal.ScopeRead), rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodResourcesRead,
			Params: modernParams(`"uri":"margince://schema/query"`),
		}, fr).Error
	}

	modern := read(framing{modern: true, version: modernProtocolVersion})
	if modern == nil || modern.Code != codeInvalidParams {
		t.Fatalf("modern refusal = %#v, want %d — -32002 must not be emitted in this era", modern, codeInvalidParams)
	}
	legacy := read(legacyFraming)
	if legacy == nil || legacy.Code != resourceNotFound {
		t.Fatalf("legacy refusal = %#v, want %d", legacy, resourceNotFound)
	}
}

// A handshake-era client never reads the modern members, and a client that
// validates strictly would refuse a result carrying them.
func TestALegacyResultIsRenderedExactlyAsItWasBefore(t *testing.T) {
	s := modernDispatcher(t)

	resp := s.handle(scopedAgentCtx(principal.ScopeRead), rpcRequest{
		JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodToolsList,
	}, legacyFraming)

	body, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshalling the result: %v", err)
	}
	for _, member := range []string{fieldResultType, fieldMeta, fieldTTLMs, fieldCacheScope} {
		if strings.Contains(string(body), `"`+member+`"`) {
			t.Errorf("a legacy result carries the modern member %q: %s", member, body)
		}
	}
}

// The caching contract names methods by hand, and a name that no longer
// dispatches would leave a MUST unmet in silence. Deriving the check from the
// dispatcher is what keeps the two sets from drifting apart.
func TestEveryMethodDeclaredCacheableIsAnsweredByThisServer(t *testing.T) {
	s := modernDispatcher(t)

	for _, method := range append([]string{methodDiscover}, modernPrivateCatalogs...) {
		if _, cacheable := modernCacheHint(method); !cacheable {
			t.Fatalf("%s is in the cacheable set but carries no hint", method)
		}
		resp := s.handle(scopedAgentCtx(principal.ScopeRead), rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: method,
			Params: modernParams(""),
		}, framing{modern: true, version: modernProtocolVersion})
		if resp.Error != nil && resp.Error.Code == codeMethodNotFound {
			t.Errorf("%s is declared cacheable and this server does not answer it", method)
		}
	}
}

// The property that makes two framings safe: they decide how a call is parsed
// and rendered, and nothing else. The same tool, called by the same
// under-scoped principal, is refused identically in both — and the same tool
// called by a principal that holds the scope answers the same bytes.
func TestBothFramingsReachOneAdmissionGate(t *testing.T) {
	s := modernDispatcher(t)
	modern := framing{modern: true, version: modernProtocolVersion}
	call := func(ctx context.Context, fr framing) map[string]any {
		resp := s.handle(ctx, rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodToolsCall,
			Params: modernParams(`"name":"read_record","arguments":{}`),
		}, fr)
		if resp.Error != nil {
			t.Fatalf("tools/call → protocol error %d %q, want an in-band result", resp.Error.Code, resp.Error.Message)
		}
		result, ok := resp.Result.(map[string]any)
		if ok {
			return result
		}
		decorated, ok := resp.Result.(modernResult)
		if !ok {
			t.Fatalf("result = %#v", resp.Result)
		}
		inner, ok := decorated.inner.(map[string]any)
		if !ok {
			t.Fatalf("decorated result = %#v", decorated.inner)
		}
		return inner
	}

	// A passport without the read scope is refused, in band, by the registry —
	// the same registry both framings call.
	underScoped := scopedAgentCtx(principal.ScopeDraft)
	legacyRefusal, modernRefusal := call(underScoped, legacyFraming), call(underScoped, modern)
	if legacyRefusal["isError"] != true || modernRefusal["isError"] != true {
		t.Fatalf("an under-scoped call was admitted: legacy=%v modern=%v", legacyRefusal, modernRefusal)
	}
	if fmt.Sprint(legacyRefusal["content"]) != fmt.Sprint(modernRefusal["content"]) {
		t.Errorf("the two framings refuse differently:\nlegacy %v\nmodern %v",
			legacyRefusal["content"], modernRefusal["content"])
	}

	// And an admitted call answers the same thing through either framing. The
	// comparison is the tool's own payload rather than the whole envelope,
	// because one member of that envelope — the trace id — is minted per call
	// and would differ between any two calls at all, including two in the same
	// framing.
	scoped := scopedAgentCtx(principal.ScopeRead)
	answer := func(fr framing) string {
		sealed, ok := call(scoped, fr)["structuredContent"].(json.RawMessage)
		if !ok {
			t.Fatalf("no structuredContent on an admitted %v call", fr)
		}
		return string(payloadOf(t, sealed))
	}
	if got, want := answer(modern), answer(legacyFraming); got != want {
		t.Errorf("the two framings answer differently:\nmodern %s\nlegacy %s", got, want)
	}
}

// The tool surface a caller is shown is scope-filtered in both framings —
// the filter is the dispatcher's, and a framing that re-implemented it would
// be the second answer this design exists to prevent.
func TestBothFramingsFilterTheToolListByTheCallersScopes(t *testing.T) {
	registry := NewRegistry(nil, auth.NewGate(fullSeatAuthority{}))
	for name, scope := range map[string]principal.Scope{
		"read_record": principal.ScopeRead, "send_email": principal.ScopeSend,
	} {
		registry.Register(echoTool{spec: objectSpec(name, scope), out: json.RawMessage(`{}`)})
	}
	s := NewDispatcher(registry, bindAuthenticated, "margince-crm", "test").WithLogger(discardLog())
	ctx := scopedAgentCtx(principal.ScopeRead)

	names := func(fr framing) []string {
		resp := s.handle(ctx, rpcRequest{
			JSONRPC: jsonRPCVersion, ID: json.RawMessage(`1`), Method: methodToolsList,
			Params: modernParams(""),
		}, fr)
		body, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("marshalling tools/list: %v", err)
		}
		var listed struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &listed); err != nil {
			t.Fatalf("decoding tools/list: %v", err)
		}
		out := make([]string, 0, len(listed.Tools))
		for _, tool := range listed.Tools {
			out = append(out, tool.Name)
		}
		return out
	}

	modern, legacy := names(framing{modern: true, version: modernProtocolVersion}), names(legacyFraming)
	if !slices.Equal(modern, []string{"read_record"}) {
		t.Errorf("modern tools/list = %v, want only the tool this passport may invoke", modern)
	}
	if !slices.Equal(modern, legacy) {
		t.Errorf("modern tools/list = %v, legacy = %v — one surface, one filter", modern, legacy)
	}
}

// A result this server cannot render as an object is its own defect, and it
// must surface as one rather than as a result the framing cannot describe.
func TestAModernResultThatIsNotAnObjectIsReportedRatherThanShipped(t *testing.T) {
	_, err := json.Marshal(modernResult{
		inner:   []string{"not", "an", "object"},
		members: map[string]any{fieldResultType: resultTypeComplete},
	})

	if err == nil {
		t.Fatal("a non-object result marshalled cleanly, so the framing's own members went missing in silence")
	}
	if !strings.Contains(err.Error(), "must be a JSON object") {
		t.Errorf("error = %v, want it to name what is wrong", err)
	}
}

// The handler's own bytes reach the client unchanged. A round trip through
// map[string]any would widen this version past float64's exact integer range,
// and the decorated result would disagree with the text block beside it.
func TestDecoratingAResultDoesNotRewriteTheHandlersBytes(t *testing.T) {
	const exact = `{"version":9007199254740993}`

	body, err := json.Marshal(modernResult{
		inner:   map[string]any{"structuredContent": json.RawMessage(exact)},
		members: map[string]any{fieldResultType: resultTypeComplete},
	})
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if !strings.Contains(string(body), `9007199254740993`) {
		t.Errorf("result = %s, want the handler's integer unchanged", body)
	}
}

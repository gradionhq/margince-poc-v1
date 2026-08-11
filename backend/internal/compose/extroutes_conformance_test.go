// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Does a mounted extension route answer the shape its CONTRACT declares?
//
// Nothing asked that question before, and the answer was no. Every mounted
// route served the governed-tool envelope — `{schema_version, trace_id, …,
// data:{…}}` — while the composed contract declared the bare payload, so the
// generated client types, the docs and any SDK all described a body the server
// never sent. Task 14's UAT found it by clicking: every read the Demo Notepad
// performed came back `undefined`, and the screen was non-functional.
//
// WHY NO GATE CAUGHT IT, which is as much the finding as the mismatch:
//
//   - `tsc` typechecks the screen against the contract. The contract was right;
//     the server was wrong. Both sides of the compile agreed with each other.
//   - The screen's unit tests stub `fetch` with the contract's shape, so they
//     asserted the same wrong-side-of-the-mismatch fixture the types did.
//   - The Go tests mounted routes and asserted status codes and refusals, never
//     the BODY against the declared schema.
//
// Nothing in CI issued a real request through a real mounted route and compared
// what came back to what the unit published. That is what this file does, and
// it is written against `extension.Verb.OutputSchema` — the same
// contract-derived value the client types are generated from — so it covers
// whatever a future unit declares rather than the one shape notes happens to
// return today.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// envelopeKeys are the sealed result's own fields. None of them may appear at
// the top level of a REST response: the body is the unit's declared payload,
// and a payload that happened to carry one of these names would be
// indistinguishable from the wrapper leaking again.
var envelopeKeys = []string{"schema_version", "trace_id", "freshness", "trust", "evidence", "warnings", "data"}

// notepadPayload is what the tool under test returns — the same shape
// notes's `signing_key_status` answers with, chosen because it is the
// read whose `undefined` the UAT saw first.
const notepadPayload = `{"stored":true}`

// declaredStatusVerb is the contract half: a route whose 200 schema says the
// body is an object with a required `stored` boolean.
func declaredStatusVerb() extension.Verb {
	v := unitVerb("demo", "key_status", extension.TierAutoExecute, extension.ScopeRead)
	v.OutputSchema = json.RawMessage(
		`{"type":"object","additionalProperties":false,"required":["stored"],"properties":{"stored":{"type":"boolean"}}}`)
	return v
}

// mountedRouteResponse drives one real request through the real mux, the real
// registry and the real admission gate, and hands back what a client would see.
func mountedRouteResponse(t *testing.T, verb extension.Verb, payload string) *httptest.ResponseRecorder {
	t.Helper()
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: verb.Tool,
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(payload), nil
			},
		}},
	}}, []extension.Verb{verb})
	if err != nil {
		t.Fatal(err)
	}
	registry := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	for _, tool := range tools {
		registry.Register(tool)
	}
	mux := http.NewServeMux()
	if _, err := MountExtensionRoutes(mux, []extension.Verb{verb},
		map[string]bool{verbKey(verb.Unit, verb.Tool): true}, registry.Invoke); err != nil {
		t.Fatal(err)
	}
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		Scopes: principal.NewScopeSet(principal.ScopeRead),
	})
	req := httptest.NewRequest(http.MethodPost, verb.ServedPath(), strings.NewReader(`{}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAMountedRouteAnswersTheBodyItsContractDeclares is the regression test for
// the UAT's F1.
func TestAMountedRouteAnswersTheBodyItsContractDeclares(t *testing.T) {
	verb := declaredStatusVerb()
	rec := mountedRouteResponse(t, verb, notepadPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}

	// 1. Verbatim. The handler's own bytes, which is what the contract's schema
	//    describes and what a generated client will decode into.
	if got := strings.TrimSpace(rec.Body.String()); got != notepadPayload {
		t.Fatalf("body = %s, want the unit's own payload %s", got, notepadPayload)
	}

	// 2. Against the DECLARED schema, key by key, rather than against the one
	//    literal above — this is the part that covers a unit nobody has written
	//    yet. Every `required` property of the operation's 200 schema must be
	//    present at the top level of the body.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not a JSON object: %v", err)
	}
	for _, required := range requiredProperties(t, verb.OutputSchema) {
		if _, ok := body[required]; !ok {
			t.Errorf("the contract declares %q required in the 200 body, and it is absent — "+
				"a generated client reads undefined here, which is exactly how the envelope leak presented", required)
		}
	}

	// 3. And no envelope key survived. Asserting only (1) and (2) would pass a
	//    body that merged the envelope's fields alongside the payload.
	for _, key := range envelopeKeys {
		if _, leaked := body[key]; leaked {
			t.Errorf("the sealed envelope's %q reached the REST body — the contract describes the unit's payload, not the transport", key)
		}
	}

	// 4. The correlation id is not lost, only moved: it is the handle that
	//    makes a governed call findable in the audit log, and the body has no
	//    room for it.
	if rec.Header().Get(extensionTraceHeader) == "" {
		t.Errorf("no %s header — a REST caller has no way to find this call in the audit log", extensionTraceHeader)
	}
}

// TestTheAgentPathKeepsTheEnvelope: only ONE side was unwrapped. An agent needs
// the answer and the provenance of the answer together — the trust tier, the
// evidence set, the schema version — so the envelope is the point there. A fix
// that stripped it everywhere would trade this defect for a worse one.
func TestTheAgentPathKeepsTheEnvelope(t *testing.T) {
	verb := declaredStatusVerb()
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name: verb.Tool,
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(notepadPayload), nil
			},
		}},
	}}, []extension.Verb{verb})
	if err != nil {
		t.Fatal(err)
	}
	registry := agents.NewRegistry(nil, auth.NewGate(fullSeat{}))
	for _, tool := range tools {
		registry.Register(tool)
	}
	out, err := registry.Invoke(extAgentCtx(), verb.Tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var sealed map[string]json.RawMessage
	if err := json.Unmarshal(out, &sealed); err != nil {
		t.Fatal(err)
	}
	for _, key := range envelopeKeys {
		if _, ok := sealed[key]; !ok {
			t.Errorf("the agent path lost the envelope's %q", key)
		}
	}
	if got := string(sealed["data"]); got != notepadPayload {
		t.Errorf("data = %s, want the unit's payload nested under it", got)
	}
}

// TestAMountedRouteRefusesAnUnsealedResult: the unwrap is strict about the
// envelope on purpose. If the registry ever stops sealing, this route has no
// payload it can honestly serve at the declared shape — and serving the bytes
// anyway is precisely the 200-with-the-wrong-body that F1 was.
func TestAMountedRouteRefusesAnUnsealedResult(t *testing.T) {
	verb := declaredStatusVerb()
	mux := http.NewServeMux()
	// invoke stands in for a registry that answers without sealing.
	unsealed := func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(notepadPayload), nil
	}
	if _, err := MountExtensionRoutes(mux, []extension.Verb{verb}, map[string]bool{verbKey(verb.Unit, verb.Tool): true}, unsealed); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, verb.ServedPath(), strings.NewReader(`{}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("an unsealed result was served as a 200: %s", rec.Body.String())
	}
}

// requiredProperties reads a JSON Schema object's `required` list. It is a
// deliberate two-field read rather than a schema validator: what this file
// needs to know is which keys a client will reach for, and a full validator
// would be a dependency and a second source of truth about the same document.
func requiredProperties(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	if schema == nil {
		return nil
	}
	var doc struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("the declared output schema is not readable: %v", err)
	}
	slices.Sort(doc.Required)
	return doc.Required
}

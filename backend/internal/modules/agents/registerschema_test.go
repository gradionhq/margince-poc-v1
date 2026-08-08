// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// declaredProperties reads back what a schema lets a caller pass.
func declaredProperties(t *testing.T, schema json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &shape); err != nil {
		t.Fatalf("schema is not readable: %v", err)
	}
	return shape.Properties
}

func registeredSpec(t *testing.T, spec mcp.ToolSpec) mcp.ToolSpec {
	t.Helper()
	r := NewRegistry(nil, nil)
	r.Register(&fakeTool{spec: spec})
	registered, ok := r.Spec(spec.Name)
	if !ok {
		t.Fatalf("%s did not register", spec.Name)
	}
	return registered
}

func mutatingSpec(name string) mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: name, Title: name, Version: testToolVersion, Description: describedForRegistration,
		RequiredScope: principal.ScopeWrite, Tier: mcp.TierAutoExecute,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"deal_id":{"type":"string","format":"uuid"}},` +
			`"required":["deal_id"],"additionalProperties":false}`),
	}
}

func TestAMutatingToolIsAdvertisedWithTheRetryKey(t *testing.T) {
	registered := registeredSpec(t, mutatingSpec("archive_record"))
	props := declaredProperties(t, registered.InputSchema)
	if _, advertised := props[idempotencyKeyArg]; !advertised {
		t.Fatalf("a mutating tool's schema does not advertise %s: %s", idempotencyKeyArg, registered.InputSchema)
	}
	if _, kept := props["deal_id"]; !kept {
		t.Error("the splice dropped the tool's own argument")
	}
	// The rest of the schema is the tool's, untouched: a splice that quietly
	// widened `additionalProperties` would let through everything the strict
	// decode then refuses.
	if !strings.Contains(string(registered.InputSchema), `"additionalProperties":false`) {
		t.Errorf("the splice lost additionalProperties:false: %s", registered.InputSchema)
	}
	if !strings.Contains(string(registered.InputSchema), `"required":["deal_id"]`) {
		t.Errorf("the splice lost the tool's `required`: %s", registered.InputSchema)
	}
}

func TestAReadOnlyToolIsNotAdvertisedWithTheRetryKey(t *testing.T) {
	spec := mutatingSpec("read_record")
	spec.RequiredScope = principal.ScopeRead
	registered := registeredSpec(t, spec)
	if _, advertised := declaredProperties(t, registered.InputSchema)[idempotencyKeyArg]; advertised {
		t.Fatalf("a read tool advertises a key that would protect nothing: %s", registered.InputSchema)
	}
}

// A mutating tool that takes no arguments still gets the key: having arguments
// says nothing about whether repeating the call is safe.
func TestAToolWithNoPropertiesStillGetsTheRetryKey(t *testing.T) {
	spec := mutatingSpec("sweep")
	spec.InputSchema = json.RawMessage(`{"type":"object"}`)
	registered := registeredSpec(t, spec)
	if _, advertised := declaredProperties(t, registered.InputSchema)[idempotencyKeyArg]; !advertised {
		t.Fatalf("an argument-less mutation was not offered the key: %s", registered.InputSchema)
	}
}

// Two definitions of one member, and only one can survive a splice — refused at
// boot rather than resolved silently, because they could disagree about type or
// bound and the surface would enforce whichever this happened to keep.
func TestAToolDeclaringTheRetryKeyItselfIsRefusedAtBoot(t *testing.T) {
	spec := mutatingSpec("create_record")
	spec.InputSchema = json.RawMessage(`{"type":"object","properties":{"idempotency_key":{"type":"integer"}}}`)
	mustPanic(t, "a tool declared the surface's own argument", func() {
		NewRegistry(nil, nil).Register(&fakeTool{spec: spec})
	})
}

func TestSpliceRetryKeyRefusesSchemasItCannotRead(t *testing.T) {
	for _, tc := range []struct{ name, schema string }{
		{name: "not an object", schema: `["nope"]`},
		{name: "properties is not an object", schema: `{"type":"object","properties":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := spliceRetryKey(json.RawMessage(tc.schema)); err == nil {
				t.Fatalf("%s was spliced", tc.schema)
			}
		})
	}
}

// The advertised property is what the surface enforces. A schema promising a
// bound the pop does not hold to — or holding to one it does not promise —
// is the mismatch A4 exists to close, one axis over.
// Read as members rather than into a struct: these are JSON Schema's own
// keywords, camelCase by that spec and not this codebase's to rename.
func TestTheAdvertisedKeyBoundIsTheOneTheSurfaceEnforces(t *testing.T) {
	var declared map[string]any
	if err := json.Unmarshal([]byte(retryKeyProperty), &declared); err != nil {
		t.Fatalf("the advertised property is not readable JSON: %v", err)
	}
	if declared["type"] != "string" {
		t.Errorf("advertised type = %v, want string", declared["type"])
	}
	bound, isNumber := declared["maxLength"].(float64)
	if !isNumber {
		t.Fatalf("the advertised property declares no maxLength: %v", declared)
	}
	if int(bound) != maxRetryKeyLen {
		t.Errorf("advertised maxLength = %d, but the surface refuses past %d", int(bound), maxRetryKeyLen)
	}
}

// A composed schema cannot be spliced by adding one top-level member: a closed
// branch inside `allOf` still rejects the key the surface just advertised, so a
// schema-aware client would be told to send an argument its own validator
// refuses.
func TestAComposedInputSchemaIsRefusedAtBoot(t *testing.T) {
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "$ref"} {
		t.Run(keyword, func(t *testing.T) {
			spec := mutatingSpec("compose_probe")
			spec.InputSchema = json.RawMessage(`{"type":"object","properties":{},"` + keyword + `":[]}`)
			mustPanic(t, "a composed schema was spliced anyway", func() {
				NewRegistry(nil, nil).Register(&fakeTool{spec: spec})
			})
		})
	}
}

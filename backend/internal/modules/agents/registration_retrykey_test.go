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

// unitTool is a fakeTool an extension unit shipped: the same tool, plus the one
// fact mcp.UnitScopedTool carries. A core tool cannot answer that question at
// all, which is what the surface reads it by.
type unitTool struct {
	*fakeTool
	unit string
}

func (u unitTool) OwningUnit() string { return u.unit }

// An extension's records never enter the datasource seam a replay re-proves its
// evidence through, so its recorded result could never pass the replay gate.
// Advertising the key there would promise a recovery the surface cannot perform
// — and a caller that believes it is protected repeats an irreversible call.
func TestAMutatingExtensionToolIsNotAdvertisedWithTheRetryKey(t *testing.T) {
	spec := mutatingSpec("notes_create_note")
	r := NewRegistry(nil, nil)
	r.Register(unitTool{fakeTool: &fakeTool{spec: spec}, unit: "notes"})
	registered, ok := r.Spec(spec.Name)
	if !ok {
		t.Fatalf("%s did not register", spec.Name)
	}
	props := declaredProperties(t, registered.InputSchema)
	if _, advertised := props[idempotencyKeyArg]; advertised {
		t.Fatalf("an extension tool advertises `%s`, whose promise its records cannot keep: %s",
			idempotencyKeyArg, registered.InputSchema)
	}
	// The exclusion withholds ONE member and touches nothing else the unit
	// declared: a tool that lost its own arguments to it would be unusable
	// rather than merely unprotected.
	if _, kept := props["deal_id"]; !kept {
		t.Errorf("the exclusion dropped the tool's own argument: %s", registered.InputSchema)
	}
}

// The other half of the same rule, so neither can move without the other: the
// exclusion is about WHO OWNS THE RECORDS, not about mutation, and a core
// mutating tool's replay machinery is unchanged.
func TestAMutatingCoreToolKeepsTheRetryKeyBesideAnExtensionsTool(t *testing.T) {
	r := NewRegistry(nil, nil)
	r.Register(&fakeTool{spec: mutatingSpec("archive_record")})
	r.Register(unitTool{fakeTool: &fakeTool{spec: mutatingSpec("notes_create_note")}, unit: "notes"})
	core, ok := r.Spec("archive_record")
	if !ok {
		t.Fatal("the core tool did not register")
	}
	if _, advertised := declaredProperties(t, core.InputSchema)[idempotencyKeyArg]; !advertised {
		t.Fatalf("a core mutating tool lost `%s`: %s", idempotencyKeyArg, core.InputSchema)
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
func TestTheAdvertisedKeyBoundIsTheOneTheSurfaceEnforces(t *testing.T) {
	var declared struct {
		Type string `json:"type"`
		// A POINTER, so "declared 0" and "declared nothing" stay different
		// answers — the second is the one that would leave the surface
		// enforcing a bound it never published.
		MaxLength *int `json:"maxLength"` //nolint:tagliatelle // JSON Schema's keyword, not ours to case
	}
	if err := json.Unmarshal([]byte(retryKeyProperty), &declared); err != nil {
		t.Fatalf("the advertised property is not readable JSON: %v", err)
	}
	if declared.Type != "string" {
		t.Errorf("advertised type = %q, want string", declared.Type)
	}
	if declared.MaxLength == nil {
		t.Fatal("the advertised property declares no maxLength, so the bound the surface enforces is unpublished")
	}
	if *declared.MaxLength != maxRetryKeyLen {
		t.Errorf("advertised maxLength = %d, but the surface refuses past %d", *declared.MaxLength, maxRetryKeyLen)
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

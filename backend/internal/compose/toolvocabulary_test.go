// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A tool and its route are two transports onto one behaviour, and a vocabulary
// is part of that behaviour: a phase or a link target the REST body accepts and
// the tool refuses is the same divergence as a missing tool, arriving one
// argument at a time. The tool declares its set in an InputSchema and the
// contract declares it in a request schema, so the two are compared here —
// against the contract, which is the source of truth for both.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// toolEnumsFromTheContract names, for each tool argument that carries a closed
// vocabulary, the OPERATION whose request body the REST twin takes it from —
// the operation rather than a component, because the contract declares some of
// these inline and both spellings have to be readable from one pin.
var toolEnumsFromTheContract = []struct {
	tool, arg         string
	operation, member string
}{
	{tool: "relink_activity", arg: "entity_type", operation: "relinkActivity", member: "entity_type"},
	{tool: "advance_project_phase", arg: "to_phase", operation: "advanceProjectPhase", member: "to_phase"},
}

func TestEveryToolEnumMatchesTheContractItMirrors(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("loading the contract: %v", err)
	}
	registry := NewRegistry(nil, SendPath{})

	for _, tc := range toolEnumsFromTheContract {
		t.Run(tc.tool+"."+tc.arg, func(t *testing.T) {
			spec, registered := registry.Spec(tc.tool)
			if !registered {
				t.Fatalf("%s is not registered — this pin no longer covers it", tc.tool)
			}
			want := contractEnum(t, doc, tc.operation, tc.member)
			got := schemaEnum(t, spec.InputSchema, tc.arg)
			if !slices.Equal(got, want) {
				t.Errorf("%s advertises %v for %s while the contract's %s body says %s: %v — a value the "+
					"REST call accepts and the tool refuses is the two transports disagreeing about one behaviour",
					tc.tool, got, tc.arg, tc.operation, tc.member, want)
			}
		})
	}
}

// contractEnum reads the declared vocabulary out of an operation's request
// body. Schemas resolve through their $refs, so a body naming a component and
// one declaring its properties inline read the same way.
func contractEnum(t *testing.T, doc *openapi3.T, operation, member string) []string {
	t.Helper()
	var body *openapi3.Schema
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op.OperationID != operation || op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			for _, media := range op.RequestBody.Value.Content {
				if media.Schema != nil && media.Schema.Value != nil {
					body = media.Schema.Value
				}
			}
		}
	}
	if body == nil {
		t.Fatalf("the contract has no request body for %s", operation)
	}
	prop, ok := body.Properties[member]
	if !ok || prop.Value == nil {
		t.Fatalf("%s's request body declares no %s", operation, member)
	}
	values := make([]string, 0, len(prop.Value.Enum))
	for _, v := range prop.Value.Enum {
		s, isString := v.(string)
		if !isString {
			t.Fatalf("%s.%s carries a non-string enum member %v", operation, member, v)
		}
		values = append(values, s)
	}
	if len(values) == 0 {
		t.Fatalf("%s.%s declares no enum — this pin would compare against nothing", operation, member)
	}
	slices.Sort(values)
	return values
}

// schemaEnum reads the same vocabulary out of the tool's advertised schema.
func schemaEnum(t *testing.T, inputSchema json.RawMessage, arg string) []string {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &doc); err != nil {
		t.Fatalf("the tool's input schema is not readable: %v", err)
	}
	prop, declared := doc.Properties[arg]
	if !declared {
		t.Fatalf("the tool's input schema declares no %s", arg)
	}
	values := slices.Clone(prop.Enum)
	slices.Sort(values)
	return values
}

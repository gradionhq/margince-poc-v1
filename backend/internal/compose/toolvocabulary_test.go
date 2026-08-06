// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A tool and its route are two transports onto one behaviour, and a vocabulary
// is part of that behaviour: a phase or a link target the REST body accepts and
// the tool refuses is the same divergence as a missing tool, arriving one
// argument at a time.
//
// Derived in both directions rather than listed. Every registered tool's
// advertised schema is walked for closed vocabularies, and every one is
// compared against the operation the policy table says backs that verb. A new
// tool with an enum is enrolled the day it is written, and a tool argument with
// no REST twin is skipped by the DERIVATION (no backing operation declares a
// property of that name), never by being forgotten.

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestEveryToolEnumMatchesTheContractItMirrors(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("loading the contract: %v", err)
	}
	bodies := requestBodiesByOperation(doc)
	registry := NewRegistry(nil, SendPath{})

	compared := 0
	for _, spec := range registry.Specs() {
		for arg, advertised := range advertisedEnums(t, spec.Name, spec.InputSchema) {
			for _, op := range backingOperations(spec.Name) {
				declared, isEnum := contractEnum(t, bodies[op], op, arg)
				if !isEnum {
					continue // the operation takes no such argument, or takes it open
				}
				compared++
				if !slices.Equal(advertised, declared) {
					t.Errorf("%s advertises %v for %s while %s's request body declares %v — a value the "+
						"REST call accepts and the tool refuses (or the reverse) is the two transports "+
						"disagreeing about one behaviour", spec.Name, advertised, arg, op, declared)
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("no tool argument was compared against a contract enum — this gate asserted nothing")
	}
}

// backingOperations answers the operationIds the generated policy table says a
// verb backs; a tool composing several operations (or none) yields each of them.
func backingOperations(tool string) []string {
	var ops []string
	for _, pol := range agentPolicies {
		if pol.Access == accessTool && pol.Tool == tool && !slices.Contains(ops, pol.Op) {
			ops = append(ops, pol.Op)
		}
	}
	slices.Sort(ops)
	return ops
}

// requestBodiesByOperation indexes each operation's request-body schema,
// resolved through its $refs so an inline body and a component read alike.
func requestBodiesByOperation(doc *openapi3.T) map[string]*openapi3.Schema {
	bodies := map[string]*openapi3.Schema{}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			for _, media := range op.RequestBody.Value.Content {
				if media.Schema != nil && media.Schema.Value != nil {
					bodies[op.OperationID] = media.Schema.Value
				}
			}
		}
	}
	return bodies
}

// contractEnum reads one property's declared vocabulary, reporting whether the
// operation declares that property as a closed set at all.
func contractEnum(t *testing.T, body *openapi3.Schema, operation, member string) ([]string, bool) {
	t.Helper()
	if body == nil {
		return nil, false
	}
	prop, declared := body.Properties[member]
	if !declared || prop.Value == nil || len(prop.Value.Enum) == 0 {
		return nil, false
	}
	values := make([]string, 0, len(prop.Value.Enum))
	for _, v := range prop.Value.Enum {
		// A nullable enum carries null as its "unset" member (logActivity's
		// direction does). That is the absence of a value, not one of them, and
		// a tool expresses the same thing by omitting the argument — so it is
		// not part of the vocabulary being compared.
		if v == nil {
			continue
		}
		s, isString := v.(string)
		if !isString {
			t.Fatalf("%s.%s carries a non-string enum member %v", operation, member, v)
		}
		values = append(values, s)
	}
	slices.Sort(values)
	return values, true
}

// advertisedEnums answers every closed vocabulary a tool's own schema declares.
func advertisedEnums(t *testing.T, tool string, inputSchema json.RawMessage) map[string][]string {
	t.Helper()
	var doc struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(inputSchema, &doc); err != nil {
		t.Fatalf("%s: input schema is not readable: %v", tool, err)
	}
	enums := map[string][]string{}
	for name, prop := range doc.Properties {
		if len(prop.Enum) == 0 {
			continue
		}
		values := slices.Clone(prop.Enum)
		slices.Sort(values)
		enums[name] = values
	}
	return enums
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The checker is the whole reason a declared schema is worth more than a
// comment, so every rule it enforces is proved against a document that BREAKS
// it — and against one that keeps it, so the rule cannot pass by refusing
// everything.
func TestTheSchemaCheckerReportsTheWayAResultMissesItsSchema(t *testing.T) {
	schema := schemaFor[ArchiveResult]()
	for _, tc := range []struct {
		name, value, want string
	}{
		{"a required member missing", `{"archived":true,"record_type":"person"}`, `required member "id" is missing`},
		{"a string where a boolean was declared", `{"archived":"yes","record_type":"person","id":"x"}`, "declared a boolean"},
		{"a number where a string was declared", `{"archived":true,"record_type":7,"id":"x"}`, "declared a string"},
		{"an array where the object was declared", `[]`, "declared an object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defect := ResultDefect(schema, json.RawMessage(tc.value))
			if !strings.Contains(defect, tc.want) {
				t.Errorf("defect = %q, want it to name %q", defect, tc.want)
			}
		})
	}
	kept := `{"archived":true,"record_type":"person","id":"0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e11"}`
	if defect := ResultDefect(schema, json.RawMessage(kept)); defect != "" {
		t.Errorf("a conforming result was reported as %q", defect)
	}
	// Open by design: a member the schema never named is not a violation, so a
	// result that grows a field does not break every client at once.
	extra := `{"archived":true,"record_type":"person","id":"x","reason":"duplicate"}`
	if defect := ResultDefect(schema, json.RawMessage(extra)); defect != "" {
		t.Errorf("an extra member was reported as %q; every schema here claims \"at least these\"", defect)
	}
}

// The nested cases, because a result is not flat: a defect inside an array item
// or a map value has to be found, and it has to say WHERE.
func TestTheSchemaCheckerReachesInsideArraysAndMaps(t *testing.T) {
	list := schemaFor[WhatsSlippingResult]()
	defect := ResultDefect(list, json.RawMessage(`{"deals":[{"rank":1,"deal_id":"x","name":"A","evidence":[]},{"rank":"two","deal_id":"y","name":"B","evidence":[]}]}`))
	if !strings.Contains(defect, "deals[1]") || !strings.Contains(defect, "declared a number") {
		t.Errorf("defect = %q, want it to name the offending item and what was declared", defect)
	}

	mapped := schemaFor[QualifyLeadResult]()
	defect = ResultDefect(mapped, json.RawMessage(`{"record_id":"x","filled":{"company_name":{"value":9,"evidence":[]}},"gaps":[]}`))
	if !strings.Contains(defect, "filled.company_name") || !strings.Contains(defect, "declared a string") {
		t.Errorf("defect = %q, want it to name the map member that failed", defect)
	}
}

// An optional member is optional in both directions: absent, and spelled out as
// null. Both are how "there is no note" reaches a caller, and neither is a
// defect — a checker that refused null would report every no-note answer.
func TestAnOptionalMemberMayBeAbsentOrNull(t *testing.T) {
	schema := schemaFor[ProgressDealResult]()
	for _, value := range []string{
		`{"deal":{"record_type":"deal","id":"x","fields":{}}}`,
		`{"deal":{"record_type":"deal","id":"x","fields":{}},"note_activity_id":null}`,
	} {
		if defect := ResultDefect(schema, json.RawMessage(value)); defect != "" {
			t.Errorf("%s was reported as %q", value, defect)
		}
	}
}

// The derivation's own claims, on the type that exercises every branch that
// matters: an embedded struct flattens, a pointer is optional, omitempty is
// optional, and a uuid is a string a caller can actually send back.
func TestTheDerivedSchemaReadsTheWireTagsAndNotTheGoNames(t *testing.T) {
	var schema jsonSchema
	if err := json.Unmarshal(schemaFor[UpdateWithStagedApprovalResult](), &schema); err != nil {
		t.Fatalf("decoding the derived schema: %v", err)
	}
	// wireRecord is embedded, so its members belong at THIS level — that is what
	// the result actually puts on the wire.
	for _, want := range []string{"record_type", "id", "fields", "staged_approval"} {
		if _, named := schema.Properties[want]; !named {
			t.Errorf("the derived schema does not name %q, which the result carries", want)
		}
	}
	if _, named := schema.Properties["wireRecord"]; named {
		t.Error("the embedded record was described as a member of its own, not flattened")
	}
	if id := schema.Properties["id"]; id == nil || id.Type != "string" || id.Format != "uuid" {
		t.Errorf("id = %+v, want a uuid-formatted string rather than its Go representation", id)
	}
	// `version` carries omitempty on wireRecord, so it must not be required.
	for _, name := range schema.Required {
		if name == "version" || name == "trust_tier" {
			t.Errorf("%q is optional on the wire but the schema requires it", name)
		}
	}
}

// A Go type this cannot describe would otherwise be advertised as something
// looser than it is, which is the failure exact schemas exist to end.
func TestDescribingAnUndescribableTypeIsAnError(t *testing.T) {
	if _, err := describeType(reflectTypeOfChan()); err == nil {
		t.Error("a type with no JSON rendering was described rather than refused")
	}
}

// reflectTypeOfChan is a type encoding/json cannot render at all — the check
// above needs one, and naming it here keeps the reflect import out of the test
// body where it would read as part of the assertion.
func reflectTypeOfChan() reflect.Type { return reflect.TypeOf(make(chan int)) }

// The checker's own failure modes, which a result reaching them means this
// server published something it cannot itself read.
func TestTheCheckerReportsASchemaItCannotRead(t *testing.T) {
	if defect := ResultDefect(json.RawMessage(`{"type":`), json.RawMessage(`{}`)); defect == "" {
		t.Error("an unreadable schema was reported as satisfied")
	}
	if defect := ResultDefect(json.RawMessage(`{"type":"widget"}`), json.RawMessage(`{}`)); !strings.Contains(defect, "unknown type") {
		t.Errorf("defect = %q, want it to name the unknown declared type", defect)
	}
}

// A schema that states no type states nothing about that node — which is what
// the two declared exceptions rely on, and what lets a passthrough result carry
// a document this surface did not build.
func TestASchemaWithNoTypeAcceptsAnything(t *testing.T) {
	for _, value := range []string{`{"anything":1}`, `[1,2]`, `"text"`, `null`} {
		if defect := ResultDefect(json.RawMessage(`{}`), json.RawMessage(value)); defect != "" {
			t.Errorf("%s was reported as %q against a schema that claims nothing", value, defect)
		}
	}
}

// A declared array that arrives as something else, and the summary a reader
// sees: a long document is cut short, because a defect line goes to a log a
// person reads and a result can carry captured text.
func TestTheCheckerNamesWhatItFoundWithoutQuotingItWhole(t *testing.T) {
	defect := ResultDefect(json.RawMessage(`{"type":"array","items":{"type":"string"}}`), json.RawMessage(`{"not":"an array"}`))
	if !strings.Contains(defect, "declared an array") {
		t.Errorf("defect = %q, want it to name what was declared", defect)
	}
	long := `"` + strings.Repeat("x", 200) + `"`
	defect = ResultDefect(json.RawMessage(`{"type":"boolean"}`), json.RawMessage(long))
	if !strings.Contains(defect, "…") || len(defect) > 120 {
		t.Errorf("defect = %q, want the found value summarized rather than quoted whole", defect)
	}
}

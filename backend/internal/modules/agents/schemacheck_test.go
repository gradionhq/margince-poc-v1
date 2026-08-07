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
	if !strings.Contains(defect, "deals[1]") || !strings.Contains(defect, "declared an integer") {
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

// A required member is required to have a VALUE, not merely a key. `null` in a
// required member is the shape of a bug — a handler that built the field and
// left it empty — and reporting it as satisfied would let exactly the defect
// these schemas exist to catch through.
func TestNullInARequiredMemberIsADefect(t *testing.T) {
	schema := schemaFor[ArchiveResult]()
	defect := ResultDefect(schema, json.RawMessage(`{"archived":null,"record_type":"person","id":"x"}`))
	if defect == "" {
		t.Error("a null in a required member was reported as satisfying the schema")
	}
}

// A hand-written schema is free to use JSON Schema's own vocabulary, and the
// checker reads a schema it did not derive on every call to an extension tool.
// `additionalProperties: false` is the shape that caught this: a reader that
// could not parse it reported a perfectly good schema as unreadable and
// withheld structured content from every call to the tool declaring it.
func TestAHandWrittenSchemaUsingJSONSchemasOwnVocabularyIsReadable(t *testing.T) {
	for name, schema := range map[string]string{
		"a closed object": `{"type":"object","properties":{"quote":{"type":"string"}},"required":["quote"],"additionalProperties":false}`,
		"an open object":  `{"type":"object","properties":{"quote":{"type":"string"}},"additionalProperties":true}`,
		"typed extras":    `{"type":"object","additionalProperties":{"type":"string"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if defect := ResultDefect(json.RawMessage(schema), json.RawMessage(`{"quote":"it ain't over"}`)); defect != "" {
				t.Errorf("a valid schema was reported as %q", defect)
			}
		})
	}
	// The typed arm still binds: extras of the wrong type are a defect.
	if defect := ResultDefect(json.RawMessage(`{"type":"object","additionalProperties":{"type":"string"}}`),
		json.RawMessage(`{"quote":7}`)); defect == "" {
		t.Error("a typed additionalProperties schema did not bind the members it describes")
	}
}

// An integer is not a number with a zero fraction: a version, a count and a
// rank are integers on this surface, and a caller told "integer" that receives
// 1.5 has been told something false.
func TestAFractionalValueDoesNotSatisfyADeclaredInteger(t *testing.T) {
	if defect := ResultDefect(json.RawMessage(`{"type":"object","properties":{"version":{"type":"integer"}}}`),
		json.RawMessage(`{"version":1.5}`)); defect == "" {
		t.Error("1.5 was reported as satisfying a declared integer")
	}
	if defect := ResultDefect(json.RawMessage(`{"type":"object","properties":{"version":{"type":"integer"}}}`),
		json.RawMessage(`{"version":3}`)); defect != "" {
		t.Errorf("a whole number was reported as %q against a declared integer", defect)
	}
}

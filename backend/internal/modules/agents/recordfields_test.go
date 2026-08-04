// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The write tools' schemas are JSON handed to a client, and their field
// descriptions are built at init by splicing a rendered string into a
// hand-written literal — so the first thing to prove is that they are still
// parseable. The second is that they name what a caller would otherwise have
// to discover by trial and error: the two record types whose name field
// differs (full_name vs display_name), the cf_ prefix extras must carry, and
// the fact that employment is a relationship this tool cannot write.
func TestWriteToolSchemasNameTheirFields(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"create_record": createRecord{}.Spec().InputSchema,
		"update_record": updateRecord{}.Spec().InputSchema,
	} {
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("%s InputSchema is not valid JSON: %v\n%s", name, err, raw)
		}
		props, ok := parsed["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s InputSchema has no properties object", name)
		}
		fields, ok := props["fields"].(map[string]any)
		if !ok {
			t.Fatalf("%s InputSchema has no fields property", name)
		}
		desc, ok := fields["description"].(string)
		if !ok {
			t.Fatalf("%s fields property carries no description", name)
		}
		for _, want := range []string{"full_name", "display_name", "cf_<slug>", "relationship"} {
			if !strings.Contains(desc, want) {
				t.Errorf("%s fields description does not mention %q: %s", name, want, desc)
			}
		}
	}
}

// The names come off the generated contract structs so they cannot drift from
// crm.yaml. That only holds while reflection actually reads json tags — a
// shape that answered no field names would render an empty, confidently wrong
// list, which is worse than the opaque object it replaced.
func TestContractFieldNamesReadsTheWireNames(t *testing.T) {
	for recordType, shape := range createShapes {
		names := contractFieldNames(shape)
		if len(names) == 0 {
			t.Errorf("%s create shape yielded no field names", recordType)
		}
		for _, name := range names {
			if name == "-" || strings.Contains(name, ",") {
				t.Errorf("%s: %q is a raw struct tag, not a wire field name", recordType, name)
			}
		}
	}
	person := strings.Join(contractFieldNames(createShapes["person"]), ",")
	if !strings.Contains(person, "full_name") || strings.Contains(person, "display_name") {
		t.Errorf("person create fields = %q, want the contract's full_name and no display_name", person)
	}
}

// jsonString leans on Go and JSON string quoting agreeing, which they do for
// every character except a control character. Forbidding those here is what
// makes that lean safe instead of lucky.
func TestDescriptionsCarryNoControlCharacters(t *testing.T) {
	for name, desc := range map[string]string{
		"create": describeRecordFields(createShapes, createRecordShapes),
		"update": describeRecordFields(updateShapes, updateRecordShapes),
	} {
		for i, r := range desc {
			if r < 0x20 || r == 0x7f {
				t.Errorf("%s description has control character %q at %d — Go would quote it in a form JSON rejects", name, r, i)
			}
		}
	}
}

// The two writes real sessions lost data to. Both returned 200 with the value
// discarded: organization_id is not a person field at all, and emails is a
// person field on CREATE and no field at all on UPDATE — the shape a caller is
// most likely to get wrong, because it exists next door.
func TestWriteToolsRefuseFieldsTheRecordCannotStore(t *testing.T) {
	cases := []struct {
		name       string
		shapes     map[datasource.EntityType]reflect.Type
		recordType string
		fields     string
		wantNamed  string
	}{
		{"organization_id on a person create", createShapes, "person", `{"full_name":"A","organization_id":"x"}`, "organization_id"},
		{"emails on a person update", updateShapes, "person", `{"emails":["a@b.c"]}`, "emails"},
		{"a typo next to a real field", createShapes, "organization", `{"displayname":"Firecrawl"}`, "displayname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectUnknownFields(tc.shapes, tc.recordType, json.RawMessage(tc.fields))
			if err == nil {
				t.Fatalf("%s was accepted — the value would be discarded with no signal", tc.wantNamed)
			}
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Fatalf("err = %v, want a BadArgsError the tool surface explains", err)
			}
			if !strings.Contains(err.Error(), tc.wantNamed) {
				t.Errorf("err = %q, want it to name %q", err, tc.wantNamed)
			}
			// Naming the field is half of it. The other half is that the
			// refusal still tells the caller what the type DOES accept —
			// otherwise it has replaced a silent drop with a dead end.
			if !strings.Contains(err.Error(), "accepts") {
				t.Errorf("err = %q, want it to list the fields the type accepts", err)
			}
			if !strings.Contains(err.Error(), customFieldPrefix) {
				t.Errorf("err = %q, want it to mention the custom-field channel", err)
			}
		})
	}
}

// What must still pass: real fields, and the custom-field channel. Whether a
// cf_ field is ACTIVE is the store's ratified question — refusing it here
// would break every workspace that defined one.
func TestWriteToolsAcceptRealFieldsAndTheCustomFieldChannel(t *testing.T) {
	for _, fields := range []string{
		`{"full_name":"Alex Nucci","title":"VP","emails":[{"email":"a@b.c"}]}`,
		`{"full_name":"Alex Nucci","cf_priority":"high"}`,
		`{}`,
	} {
		if err := rejectUnknownFields(createShapes, "person", json.RawMessage(fields)); err != nil {
			t.Errorf("rejectUnknownFields(%s) = %v, want accepted", fields, err)
		}
	}
	// An unknown record_type is the provider's refusal to make: it answers
	// with the vocabulary it serves, which this check cannot.
	if err := rejectUnknownFields(createShapes, "invoice", json.RawMessage(`{"anything":1}`)); err != nil {
		t.Errorf("unknown record_type = %v, want it left to the provider", err)
	}
}

// Every schema argument declaring a date-time must splice in timestampNote.
//
// Derived from the SOURCE of every schema(…) expression in this package, not
// from a list of tools: the first version of this gate hand-listed two tools
// and claimed "every", which is how a gate ends up certifying the four it never
// looked at. A new timestamp argument is covered the moment it is written.
func TestEveryTimestampArgumentDocumentsItsOffset(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	checked := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "schema" {
				return true
			}
			literal := schemaText(call.Args[0])
			// PER OCCURRENCE, not per schema: an earlier version asked only
			// whether the schema referenced timestampNote anywhere, so a field
			// that lost its note passed on a sibling's. Every "date-time" must
			// be followed immediately by the note's splice.
			for _, occurrence := range dateTimeOccurrences(literal) {
				checked++
				if !occurrence {
					t.Errorf("%s:%d: a date-time argument in this schema does not splice timestampNote directly "+
						"after its format keyword — the keyword alone does not tell a caller an offset is required",
						name, fset.Position(call.Pos()).Line)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no schema in this package declares a date-time argument — the scan is broken, not the code")
	}
}

// dateTimeOccurrences reports, for each `"format":"date-time"` in the flattened
// schema text, whether the note's splice follows it immediately. schemaText
// drops the interpolated expressions, so a spliced note leaves the literal
// halves adjoined as `…"date-time"}` — while a note that IS present leaves the
// marker text that timestampNote itself begins with.
func dateTimeOccurrences(literal string) []bool {
	const marker = `"format":"date-time"`
	var out []bool
	for i := 0; ; {
		at := strings.Index(literal[i:], marker)
		if at < 0 {
			return out
		}
		i += at + len(marker)
		out = append(out, strings.HasPrefix(literal[i:], `,"description":"RFC 3339`))
	}
}

// schemaText flattens a schema argument into the text a caller would receive.
// A schema is either one raw string or a `raw + const + raw` concatenation, so
// the spliced constants are substituted in rather than skipped — the gate reads
// the rendered result, not the source shape.
func schemaText(expr ast.Expr) string {
	var literal string
	var walk func(ast.Expr)
	walk = func(e ast.Expr) {
		switch node := e.(type) {
		case *ast.BinaryExpr:
			walk(node.X)
			walk(node.Y)
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				literal += strings.Trim(node.Value, "`")
			}
		case *ast.Ident:
			// timestampNote is a string constant; splice its value in so the
			// per-occurrence check sees the text a caller would receive.
			if node.Name == "timestampNote" {
				literal += timestampNote
			}
		case *ast.CallExpr:
			for _, arg := range node.Args {
				walk(arg)
			}
			walk(node.Fun)
		}
	}
	walk(expr)
	return literal
}

// The prefix is not a slug. `cf_` alone names no catalog column, so accepting
// it would let the one key the check is meant to catch through the one hole it
// leaves — and a null payload decodes to a nil map with no error and no keys at
// all, which is the same hole wearing a different shape.
func TestWriteToolsRefuseTheCustomFieldPrefixWithNoSlug(t *testing.T) {
	for name, fields := range map[string]string{
		"bare prefix": `{"full_name":"A","cf_":"x"}`,
		"null body":   `null`,
	} {
		t.Run(name, func(t *testing.T) {
			err := rejectUnknownFields(createShapes, "person", json.RawMessage(fields))
			if err == nil {
				t.Fatalf("%s was accepted — the value would be discarded with no signal", fields)
			}
			var bad *BadArgsError
			if !errors.As(err, &bad) {
				t.Errorf("err = %v, want a BadArgsError the tool surface explains", err)
			}
		})
	}
	// A real slug still passes: whether that field is ACTIVE is the store's
	// question, and refusing it here would break every workspace that has one.
	if err := rejectUnknownFields(createShapes, "person", json.RawMessage(`{"full_name":"A","cf_priority":"high"}`)); err != nil {
		t.Errorf("a real custom-field key was refused: %v", err)
	}
}

// The record_type enum a write tool advertises and the shapes it can actually
// decode are two spellings of one set, written in two places — a literal
// inside the schema string, and the keys of the shapes map. §8.2b of
// NEXT-MCP-MISSING is what happens when they drift: the contract declared
// update_record/activity, the shapes map deliberately omitted it to match an
// enum that omitted it too, and an activity became un-updatable over the tool
// surface while REST served the patch fine.
//
// Derived rather than listed, so a record type added to either place fails
// here until it is added to both.
func TestWriteToolEnumsMatchTheShapesTheyCanDecode(t *testing.T) {
	for _, tc := range []struct {
		tool   string
		schema json.RawMessage
		shapes map[datasource.EntityType]reflect.Type
	}{
		{"create_record", createRecord{}.Spec().InputSchema, createShapes},
		{"update_record", updateRecord{}.Spec().InputSchema, updateShapes},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			advertised := recordTypeEnum(t, tc.tool, tc.schema)
			decodable := make(map[string]bool, len(tc.shapes))
			for recordType := range tc.shapes {
				decodable[string(recordType)] = true
			}

			for _, recordType := range advertised {
				if !decodable[recordType] {
					t.Errorf("%s advertises record_type %q but describes no fields for it — "+
						"a caller reading the enum would send a patch the description never explains",
						tc.tool, recordType)
				}
				if !slices.Contains(datasource.EntityTypes(), datasource.EntityType(recordType)) {
					t.Errorf("%s advertises record_type %q, which is outside the datasource vocabulary — "+
						"no provider could serve it", tc.tool, recordType)
				}
			}
			for recordType := range decodable {
				if !slices.Contains(advertised, recordType) {
					t.Errorf("%s can decode %q but does not advertise it — the capability exists and no caller can reach it",
						tc.tool, recordType)
				}
			}
		})
	}
}

// recordTypeEnum reads the record_type enum out of a tool's advertised schema,
// which is the string a client actually reads.
func recordTypeEnum(t *testing.T, tool string, raw json.RawMessage) []string {
	t.Helper()
	var parsed struct {
		Properties struct {
			RecordType struct {
				Enum []string `json:"enum"`
			} `json:"record_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("%s InputSchema is not valid JSON: %v", tool, err)
	}
	if len(parsed.Properties.RecordType.Enum) == 0 {
		t.Fatalf("%s advertises no record_type enum — this walk would pass vacuously", tool)
	}
	return parsed.Properties.RecordType.Enum
}

// The create and patch descriptions must not converge.
//
// They differ on one thing that matters: an edge's endpoints can be NAMED on
// create and cannot be patched at all, because an edge's ends are what it is. The
// branch that says so was keyed on `shapes[EntityRelationship]` at first, and both
// maps carry that key — so the patch tool shipped the create tool's pairing rule
// and the sentence written for the patch tool was unreachable.
func TestTheCreateAndPatchDescriptionsDisagreeAboutEndpoints(t *testing.T) {
	create := describeRecordFields(createShapes, createRecordShapes)
	patch := describeRecordFields(updateShapes, updateRecordShapes)

	// The pairing rule is create-only: naming a pair is a thing only a create can
	// do. Keyed on the stable CLAIM, not on the sentence — a reworded advisory is
	// not a regression, a missing one is.
	const pairingRule = "endpoint pair"
	if !strings.Contains(create, pairingRule) {
		t.Errorf("create_record's description does not state the per-kind endpoint pairing rule, which "+
			"is invisible from a flat field list: %q", create)
	}
	if strings.Contains(patch, pairingRule) {
		t.Error("update_record's description states the endpoint pairing rule, and a patch cannot reach " +
			"an endpoint — so it is advice the caller cannot act on")
	}

	// And the patch tool says what IS true for it, rather than saying nothing.
	if !strings.Contains(patch, "cannot be patched") {
		t.Errorf("update_record's description never says an edge's endpoints are unpatchable, which is "+
			"the first thing a caller looks for and does not find: %q", patch)
	}

	// The same mistake in its sibling: an activity's links ARE settable on create
	// and not patchable, and both shape maps carry activity — so a record-type key
	// put the patch-only advice on the create tool too.
	const linksAdvisory = "links are NOT patchable"
	if !strings.Contains(patch, linksAdvisory) {
		t.Errorf("update_record's description does not say an activity's links are unpatchable: %q", patch)
	}
	if strings.Contains(create, linksAdvisory) {
		t.Error("create_record's description says an activity's links are NOT patchable — create_record " +
			"and log_activity both ACCEPT links, so it is advice against a field the tool takes")
	}
}

// The closing custom-field sentence must not promise carriage the surface
// withholds.
//
// Every record type's description ends by telling callers that extra cf_<slug>
// keys are read as custom-field values. That is false for activity and
// relationship: neither contract shape carries the additionalProperties bag a cf_
// value travels in, and customfields.FieldObjects deliberately excludes both — so
// an agent following the description writes a key the strict decoder refuses.
func TestTheCustomFieldSentenceNamesTheTypesThatTakeNone(t *testing.T) {
	for name, description := range map[string]string{
		"create": describeRecordFields(createShapes, createRecordShapes),
		"update": describeRecordFields(updateShapes, updateRecordShapes),
	} {
		if !strings.Contains(description, "cf_<slug>") {
			t.Fatalf("%s: the description no longer mentions the custom-field key shape at all: %q",
				name, description)
		}
		// The exclusion clause, isolated with Cut so the assertion reads the
		// SENTENCE rather than the whole description — "activity" appears earlier
		// in the field lists, so a substring test over the full text would pass
		// without any exclusion being stated at all.
		_, exclusion, found := strings.Cut(description, "No custom fields on")
		if !found {
			t.Errorf("%s: the description promises cf_ carriage and excludes nothing, though activity and "+
				"relationship carry no additionalProperties bag — a cf_ key on either is refused, not "+
				"stored: %q", name, description)
			continue
		}
		// Both, and by name: an agent reads prose, so "some types are excluded"
		// would leave it guessing which.
		for _, excluded := range []string{"activity", "relationship"} {
			if !strings.Contains(exclusion, excluded) {
				t.Errorf("%s: the cf_ exclusion clause %q does not name %q", name, exclusion, excluded)
			}
		}
	}
}

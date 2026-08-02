// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
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
		"create": describeRecordFields(createShapes),
		"update": describeRecordFields(updateShapes),
	} {
		for i, r := range desc {
			if r < 0x20 || r == 0x7f {
				t.Errorf("%s description has control character %q at %d — Go would quote it in a form JSON rejects", name, r, i)
			}
		}
	}
}

// Every date-time argument must SAY that RFC 3339 needs a zone offset. The
// format keyword alone already cost two refused calls, and the schemas are
// spliced strings, so a note that broke its schema would be worse than none.
func TestEveryTimestampArgumentDocumentsItsOffset(t *testing.T) {
	specs := map[string]json.RawMessage{}
	for _, tool := range []interface{ Spec() mcp.ToolSpec }{
		logActivity{}, checkAvailability{},
	} {
		spec := tool.Spec()
		specs[spec.Name] = spec.InputSchema
	}
	for name, raw := range specs {
		var parsed struct {
			Properties map[string]struct {
				Format      string `json:"format"`
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("%s InputSchema is not valid JSON: %v\n%s", name, err, raw)
		}
		found := 0
		for field, prop := range parsed.Properties {
			if prop.Format != "date-time" {
				continue
			}
			found++
			if !strings.Contains(prop.Description, "offset") {
				t.Errorf("%s.%s is a date-time with no offset requirement in its description: %q",
					name, field, prop.Description)
			}
		}
		if found == 0 {
			t.Errorf("%s exposes no date-time argument — this test is watching the wrong tools", name)
		}
	}
}

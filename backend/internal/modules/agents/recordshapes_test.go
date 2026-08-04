// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The rendered shapes come from crm.yaml; the decoder is filled by Go structs
// generated from the same file by a different tool. Two derivations of one
// contract can disagree, and the disagreement would be invisible: it lives in a
// description no test reads, telling an agent to send a field the decoder
// refuses — the exact trial-and-error the shapes exist to end.
//
// So the two are compared, for every record type on both tools, derived from the
// tables rather than listed here. A record type added to one map and not the
// other fails too.
func TestRenderedShapesDescribeExactlyWhatTheDecoderAccepts(t *testing.T) {
	for _, table := range []struct {
		tool     string
		decoded  map[datasource.EntityType]reflect.Type
		rendered map[string]string
	}{
		{"create_record", createShapes, createRecordShapes},
		{"update_record", updateShapes, updateRecordShapes},
	} {
		if len(table.decoded) != len(table.rendered) {
			t.Errorf("%s describes %d record types and decodes %d",
				table.tool, len(table.rendered), len(table.decoded))
		}
		for recordType, shape := range table.decoded {
			rendered, ok := table.rendered[string(recordType)]
			if !ok {
				t.Errorf("%s decodes %s but renders no shape for it", table.tool, recordType)
				continue
			}
			want := contractFieldNames(shape)
			got := renderedKeys(rendered)
			if !slices.Equal(got, want) {
				t.Errorf("%s %s renders keys %v, decoder accepts %v", table.tool, recordType, got, want)
			}
		}
	}
}

// renderedKeyPattern reads the key names back out of a rendered shape. Keys sit
// at the top level only: a nested `{domain: string}` never starts a segment,
// because the split is on the comma-space that separates top-level pairs and
// nested pairs are inside braces the scan skips.
var renderedKeyPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*\??$`)

// renderedKeys extracts the top-level key names from a rendered shape, in the
// order they appear (which the generator sorts).
func renderedKeys(rendered string) []string {
	body := strings.TrimSuffix(strings.TrimPrefix(rendered, "{"), "}")
	var keys []string
	depth := 0
	start := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				keys = appendKey(keys, body[start:i])
				start = i + 1
			}
		}
	}
	keys = appendKey(keys, body[start:])
	return keys
}

// appendKey takes the key half of one `name: shape` pair, dropping the optional
// marker so the names compare against the decoder's json tags.
func appendKey(keys []string, pair string) []string {
	name, _, found := strings.Cut(strings.TrimSpace(pair), ":")
	if !found {
		return keys
	}
	name = strings.TrimSuffix(name, "?")
	if !renderedKeyPattern.MatchString(name) {
		return keys
	}
	return append(keys, name)
}

// The shapes exist to answer the two questions a name list could not, so both
// are pinned against the payloads that were actually refused in the field: an
// organization's `domains` and an activity's `links` are arrays of OBJECTS, and
// a caller reading only names sends an array of strings.
func TestRenderedShapesCarryTheItemShapesThatWereGuessedWrong(t *testing.T) {
	for _, tc := range []struct {
		name, rendered, want string
	}{
		{"an org's domains on create", createRecordShapes["organization"], "domains?: [{domain: string, is_primary?: boolean}]"},
		{"an org's domains on update", updateRecordShapes["organization"], "domains?: [{domain: string, is_primary?: boolean}]"},
		{"an activity's links on create", createRecordShapes["activity"], "links?: [{entity_id: uuid, entity_type:"},
		{"a person's emails on create", createRecordShapes["person"], "emails?: [{email: email,"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.rendered, tc.want) {
				t.Errorf("shape does not say %q:\n%s", tc.want, tc.rendered)
			}
		})
	}
}

// A closed vocabulary is the half reflection cannot reach: Go renders these as a
// named string type whose values live in constants, so a description built by
// reflection calls `lifecycle` a string and a caller sends "Customer".
func TestRenderedShapesCarryEnumValues(t *testing.T) {
	for _, tc := range []struct {
		name, rendered, want string
	}{
		{"an org's lifecycle", updateRecordShapes["organization"], `lifecycle?: "unknown"|"target"|"prospect"`},
		{"an activity's kind", createRecordShapes["activity"], `kind: "email"|"call"|"meeting"|"note"|"task"`},
		{"a relationship's kind", createRecordShapes["relationship"], `kind: "employment"|"deal_stakeholder"`},
		{"a deal's status", updateRecordShapes["deal"], `status?: "open"|"won"|"lost"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.rendered, tc.want) {
				t.Errorf("shape does not say %q:\n%s", tc.want, tc.rendered)
			}
		})
	}
}

// Which keys are REQUIRED is the other thing a name list never said. A caller
// creating an organization had no way to learn that `display_name` is not
// optional until the write was refused.
func TestRenderedShapesMarkRequiredKeys(t *testing.T) {
	for _, tc := range []struct{ name, rendered, required string }{
		{"an org's display_name", createRecordShapes["organization"], "display_name: string"},
		{"a person's full_name", createRecordShapes["person"], "full_name: string"},
		{"a deal's pipeline_id", createRecordShapes["deal"], "pipeline_id: uuid"},
		{"an activity's kind", createRecordShapes["activity"], "kind: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.rendered, tc.required) {
				t.Errorf("shape does not mark %q required:\n%s", tc.required, tc.rendered)
			}
			if strings.Contains(tc.rendered, strings.TrimSuffix(tc.required, ": ")+"?:") {
				t.Errorf("shape marks %q optional:\n%s", tc.required, tc.rendered)
			}
		})
	}
}

// probeReportCatalog stands in for the engine's catalog wherever a test builds
// the full registry. It carries a REAL entry rather than nothing, because an
// empty catalog takes the branch that omits the enum — so a registry built on
// nil would conformance-check a schema no deployment serves.
var probeReportCatalog = []ReportCatalogEntry{{
	Report:     "deals-by-stage",
	GroupBy:    []string{"pipeline_id", "stage_id", "status"},
	Filters:    []string{"owner_id", "pipeline_id", "status"},
	Aggregates: []string{"amount_minor"},
	Defaults:   "count as deals grouped by stage_id",
}}

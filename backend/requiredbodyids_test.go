// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every contract request body that declares a required id must be accounted for.
//
// The defect behind this gate: oapi-codegen renders a REQUIRED body id as a
// non-pointer openapi_types.UUID, and encoding/json leaves an absent key at the
// zero value with no error. Nothing downstream distinguishes "the caller named
// this record" from "the caller named nothing" — so the zero UUID reaches a
// lookup or a link-target check, matches no row, and the caller is told a record
// it never mentioned does not exist. `create_record` for a deal and a project
// were unusable for exactly that reason.
//
// The obligation is derived from the generated contract, not from a list of the
// bodies someone remembered: the walk finds every request body with such a field
// and requires each to be either PROBED (a mapping refuses it by name, proved in
// the owning module's test) or WAIVED with a reason. A new required id upstream
// fails here until someone decides which it is, and a waiver that stops being
// true fails too — so the list of unguarded bodies can only shrink.
//
// Why a waiver list at all: the guarded pair sits in the deals mapping that both
// transports share, and the rest each need their own REST mapping touched. Naming
// them is what keeps the invariant from being quietly half-stated while the tool
// surface alone is fixed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

const generatedContract = "internal/contracts/api_gen.go"

// probedRequiredIDBodies are the bodies whose mapping refuses an omitted id by
// name today, proved by TestEveryRequiredBodyIDIsNamedWhenAbsent in the module
// that owns the mapping.
var probedRequiredIDBodies = map[string]bool{
	"CreateDealRequest":    true,
	"CreateProjectRequest": true,
}

// unguardedRequiredIDBodies names each body that still answers a bare not-found
// for an id the caller never sent, with where the fix belongs. Removing an entry
// means guarding the mapping and adding a probe beside the two above.
var unguardedRequiredIDBodies = map[string]string{
	"AdvanceDealRequest":            "to_stage_id — deals/handlers_deal.go passes it raw. The MCP twin IS guarded at Registry.Invoke, so REST currently answers worse for the same mistake; the sharpest one to fix next",
	"CreateStageRequest":            "pipeline_id — deals/handlers_stages.go",
	"AddListMemberRequest":          "entity_id — collections/handlers.go, reaches auth.EnsureLinkTarget",
	"ApplyTagRequest":               "entity_id — collections/handlers.go, reaches auth.EnsureLinkTarget",
	"RecordConsentRequest":          "purpose_id — consent/handlers.go",
	"IssueDoubleOptInJSONBody":      "purpose_id — consent/handlers.go",
	"SetProjectStakeholderRequest":  "person_id — people/handlers_projectstakeholder.go",
	"CreateRecordGrantRequest":      "record_id and subject_id — identity grants",
	"MergePersonJSONBody":           "target_id — people/handlers_person.go; MergePerson checks source!=target and then locks the pair",
	"MergeOrganizationJSONBody":     "target_id — people/handlers_organization.go, same shape",
	"RelinkActivityJSONBody":        "entity_id — activities/handlers_lifecycle.go",
	"UploadAttachmentMultipartBody": "entity_id — attachments",
	// Not a request body at all: the GDPR data-subject-request ENTITY, whose `id`
	// is its own primary key rather than a caller-supplied reference. Waived
	// because the name heuristic above cannot tell the two apart, and narrowing
	// the heuristic to exclude it would risk excluding a real body.
	"DataSubjectRequest": "not a request body — the DSR entity; `id` is its own key, not a caller-supplied reference",
}

func TestEveryContractBodyWithARequiredIDIsAccountedFor(t *testing.T) {
	bodies := contractBodiesWithARequiredID(t)
	if len(bodies) == 0 {
		t.Fatalf("no request body in %s declares a required non-pointer UUID — the walk is reading "+
			"the wrong shape", generatedContract)
	}

	names := make([]string, 0, len(bodies))
	for name := range bodies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if probedRequiredIDBodies[name] || unguardedRequiredIDBodies[name] != "" {
			continue
		}
		t.Errorf("%s declares required id(s) %v and is neither probed nor waived. An absent key "+
			"decodes to the zero UUID with no error, so it reaches a lookup that matches nothing and "+
			"the caller is told a record it never named does not exist. Guard it in the mapping both "+
			"transports share and probe it, or add it to unguardedRequiredIDBodies with a reason.",
			name, bodies[name])
	}
	// A stale entry on either list is a failure: it would outlive the thing it
	// describes and make the gap look different from what it is.
	for name := range unguardedRequiredIDBodies {
		if _, still := bodies[name]; !still {
			t.Errorf("unguardedRequiredIDBodies names %s, which no longer declares a required "+
				"non-pointer UUID — drop the entry", name)
		}
	}
	for name := range probedRequiredIDBodies {
		if _, still := bodies[name]; !still {
			t.Errorf("probedRequiredIDBodies names %s, which no longer declares a required "+
				"non-pointer UUID — drop the entry", name)
		}
	}
}

// contractBodiesWithARequiredID maps each generated request-body type to the wire
// names of its required (non-pointer) UUID fields.
//
// Non-pointer IS the generator's spelling of "required", which is what makes the
// set derivable. A pointer field is optional and absent means absent, so it
// carries none of this hazard.
func contractBodiesWithARequiredID(t *testing.T) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), generatedContract, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", generatedContract, err)
	}
	out := map[string][]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		structType, isStruct := spec.Type.(*ast.StructType)
		if !isStruct || !isRequestBodyName(spec.Name.Name) {
			return true
		}
		var required []string
		for _, field := range structType.Fields.List {
			if !isRequiredUUIDField(field) {
				continue
			}
			if name := jsonFieldName(field); name != "" {
				required = append(required, name)
			}
		}
		if len(required) > 0 {
			sort.Strings(required)
			out[spec.Name.Name] = required
		}
		return true
	})
	return out
}

// isRequestBodyName reports whether a generated type name is a request body.
// The generator's three shapes: a named schema (…Request), an inline JSON body
// (…JSONBody), and a multipart form (…MultipartBody).
func isRequestBodyName(name string) bool {
	return strings.HasSuffix(name, "Request") ||
		strings.HasSuffix(name, "JSONBody") ||
		strings.HasSuffix(name, "MultipartBody")
}

// isRequiredUUIDField reports whether a struct field is a bare (non-pointer)
// openapi_types.UUID.
func isRequiredUUIDField(field *ast.Field) bool {
	sel, ok := field.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "UUID" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "openapi_types"
}

// jsonFieldName reports the wire name a field's json tag binds.
func jsonFieldName(field *ast.Field) string {
	if field.Tag == nil {
		return ""
	}
	tag := strings.Trim(field.Tag.Value, "`")
	marker := `json:"`
	start := strings.Index(tag, marker)
	if start < 0 {
		return ""
	}
	rest := tag[start+len(marker):]
	value := rest[:strings.IndexByte(rest, '"')]
	name, _, _ := strings.Cut(value, ",")
	if name == "-" {
		return ""
	}
	return name
}

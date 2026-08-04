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
// Where the guard goes: httperr.RequireBodyID is the one implementation, called
// from the point where the decoded body becomes store input — a mapping where the
// module has one (deals), the store entry point where it does not, because that is
// the door every transport comes through. Never in the HTTP handler alone, which
// is what left the MCP twin and the REST route answering differently.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

const generatedContract = "internal/contracts/api_gen.go"

// probedRequiredIDBodies are the bodies whose mapping refuses an omitted id by
// name today, proved by TestEveryRequiredBodyIDIsNamedWhenAbsent in the module
// that owns the mapping.
var probedRequiredIDBodies = map[string]bool{
	"CreateDealRequest":            true,
	"CreateProjectRequest":         true,
	"AdvanceDealRequest":           true,
	"CreateStageRequest":           true,
	"AddListMemberRequest":         true,
	"ApplyTagRequest":              true,
	"RecordConsentRequest":         true,
	"IssueDoubleOptInJSONBody":     true,
	"SetProjectStakeholderRequest": true,
	"CreateRecordGrantRequest":     true,
	"MergePersonJSONBody":          true,
	"MergeOrganizationJSONBody":    true,
	"RelinkActivityJSONBody":       true,
}

// unguardedRequiredIDBodies is what is left, and both entries are WAIVERS rather
// than gaps: neither can produce the defect. Every body that could has a guard
// and a probe.
//
// The list is kept — rather than deleted along with the last real gap — because
// the walk fails on a body it can neither find probed nor find waived. A required
// id added upstream therefore lands here as a failure until someone decides which
// it is, which is the difference between having fixed eleven things and having
// closed the class.
var unguardedRequiredIDBodies = gatekit.Waive(map[string]string{
	// Not a request body at all: the GDPR data-subject-request ENTITY, whose `id`
	// is its own primary key rather than a caller-supplied reference. Waived
	// because the name heuristic above cannot tell the two apart, and narrowing
	// the heuristic to exclude it would risk excluding a real body.
	"DataSubjectRequest": "not a request body — the DSR entity; `id` is its own key, not a caller-supplied reference",
	// Multipart never decodes into this generated type. The handler
	// (activities/handlers_attachment.go) calls ParseMultipartForm and
	// FormValue("entity_id"), so an absent part yields "" and ids.Parse already
	// refuses it as a 422 naming the field. It cannot become a zero UUID reaching
	// a lookup, which is the only hazard this gate is about.
	"UploadAttachmentMultipartBody": "multipart; FormValue + ids.Parse already refuses an absent part as a 422",
})

func TestEveryContractBodyWithARequiredIDIsAccountedFor(t *testing.T) {
	defer unguardedRequiredIDBodies.AssertAllMatched(t)
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
		if probedRequiredIDBodies[name] || unguardedRequiredIDBodies.Waived(t, name) {
			continue
		}
		t.Errorf("%s declares required id(s) %v and is neither probed nor waived. An absent key "+
			"decodes to the zero UUID with no error, so it reaches a lookup that matches nothing and "+
			"the caller is told a record it never named does not exist. Guard it in the mapping both "+
			"transports share and probe it, or add it to unguardedRequiredIDBodies with a reason.",
			name, bodies[name])
	}
	// A stale entry is a failure: it would outlive the thing it describes and
	// make the gap look different from what it is.
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

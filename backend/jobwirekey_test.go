// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// One workspace arg, one spelling, and only where it means something. A kind
// that carries its tenant under a different wire key is invisible to
// `args->>'workspace_id'`; a dispatcher that carries one is counted as a
// tenant's pass it never made. Both directions have to hold, because the reads
// built on this treat a non-null workspace_id as "this job did tenant work" and
// a null as "a dispatcher" — and in each failure the wrong answer looks exactly
// like the reassuring one, which is why this is a gate and not a convention.

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// workspaceArgFloor guards against a vacuous pass. This gate only reports on
// the types it FINDS, so a walker that matched nothing would read green. The
// tree holds 26 workspace-scoped kinds today; the floor only has to be low
// enough never to false-alarm and high enough to catch a walker that broke.
const workspaceArgFloor = 20

// The ONE field name, type, and wire key every workspace-scoped args type
// carries. The FIELD is `Workspace` because Go forbids a field and a method of
// the same name and the accessor is WorkspaceID(); the KEY is `workspace_id`
// because `args->>'workspace_id'` has to be total over tenant jobs.
const (
	workspaceArgField = "Workspace"
	workspaceArgType  = "ids.UUID"
	workspaceArgKey   = "workspace_id"
)

func TestEveryWorkspaceScopedArgsSpellsItsWorkspaceKeyTheSameWay(t *testing.T) {
	dir := filepath.Join("internal", "compose")
	byType := methodsByType(t, dir)
	fset, files := parseGoFilesUnder(t, dir)

	checked := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// Kind() is what makes a type River job args. Filtering on the
				// role method alone would drag any unrelated compose type that
				// happens to expose WorkspaceID() into the args shape and fail
				// it for a rule it was never under.
				methods := byType[typeSpec.Name.Name]
				if !methods["Kind"] {
					continue
				}
				scoped, fleet := methods["WorkspaceID"], methods["FleetWide"]
				if !scoped && !fleet {
					continue // jobrole_test.go owns "declares a role at all".
				}
				pos := fset.Position(typeSpec.Pos())
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Errorf("%s:%d: %s declares a job role but is not a struct — River marshals args to a JSON object, and a non-object carries no workspace_id at all.",
						pos.Filename, pos.Line, typeSpec.Name.Name)
					continue
				}
				if scoped {
					checked++
					assertWorkspaceArg(t, fset, typeSpec.Name.Name, structType)
					continue
				}
				assertNoWorkspaceArg(t, fset, typeSpec.Name.Name, structType)
			}
		}
	}
	if checked < workspaceArgFloor {
		t.Fatalf("inspected only %d workspace-scoped args types, expected at least %d — the walker matched nothing and this gate would pass vacuously", checked, workspaceArgFloor)
	}
}

// assertWorkspaceArg checks one args struct carries its workspace under the
// sanctioned name, type, and wire key.
func assertWorkspaceArg(t *testing.T, fset *token.FileSet, typeName string, structType *ast.StructType) {
	t.Helper()
	for _, field := range structType.Fields.List {
		for _, name := range field.Names {
			if name.Name != workspaceArgField {
				continue
			}
			pos := fset.Position(field.Pos())
			if got := types.ExprString(field.Type); got != workspaceArgType {
				t.Errorf("%s:%d: %s.%s is %s, want %s — workspaceJobCtx binds one type.",
					pos.Filename, pos.Line, typeName, workspaceArgField, got, workspaceArgType)
			}
			if field.Tag == nil {
				t.Errorf("%s:%d: %s.%s carries no struct tag, want `json:%q` — an untagged field ships as %q and args->>'workspace_id' misses it.",
					pos.Filename, pos.Line, typeName, workspaceArgField, workspaceArgKey, workspaceArgField)
				return
			}
			if got := jsonKey(field); got != workspaceArgKey {
				t.Errorf("%s:%d: %s.%s ships as json:%q, want json:%q — a divergent key is invisible to args->>'workspace_id', and a null there reads as a dispatcher rather than as tenant work the query cannot see.",
					pos.Filename, pos.Line, typeName, workspaceArgField, got, workspaceArgKey)
			}
			return
		}
	}
	t.Errorf("%s declares WorkspaceID() but has no %s field — the accessor has to return something the wire carries.", typeName, workspaceArgField)
}

// assertNoWorkspaceArg holds the other half: a dispatcher does no tenant work,
// so it must not ship the key that says it did.
func assertNoWorkspaceArg(t *testing.T, fset *token.FileSet, typeName string, structType *ast.StructType) {
	t.Helper()
	for _, field := range structType.Fields.List {
		if jsonKey(field) != workspaceArgKey {
			continue
		}
		pos := fset.Position(field.Pos())
		t.Errorf("%s:%d: %s is a dispatcher (it declares FleetWide()) but ships a json:%q key — a non-null workspace_id has to mean tenant work, or a per-workspace read of river_job counts a fan-out as a tenant's pass.",
			pos.Filename, pos.Line, typeName, workspaceArgKey)
	}
}

// jsonKey returns a field's wire name, dropping the `,omitempty`-style options
// that would otherwise make an equality check miss.
func jsonKey(field *ast.Field) string {
	if field.Tag == nil {
		return ""
	}
	tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("json")
	name, _, _ := strings.Cut(tag, ",")
	return name
}

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
	"strconv"
	"strings"
	"testing"
)

// workspaceArgFloor guards against a vacuous pass on the scoped side. This
// gate only reports on the types it FINDS, so a walker that matched nothing
// would read green. The tree holds 30 workspace-scoped kinds today; the floor
// only has to be low enough never to false-alarm and high enough to catch a
// walker that broke, so it keeps six kinds of headroom under the count.
const workspaceArgFloor = 24

// dispatcherArgFloor is workspaceArgFloor's other half. assertNoWorkspaceArg
// only runs on types the walker routes to it as FleetWide; if a future
// receiver shape (an embedded marker, a generic receiver, a rename) stops
// methodsByType from seeing FleetWide(), those types fall into "declares
// neither role" and are skipped entirely — checked (the scoped count) stays
// unaffected and reads plausible while the dispatcher half is inspected zero
// times. The tree holds 23 FleetWide kinds today; same reasoning as
// workspaceArgFloor, with five kinds of headroom under the count.
const dispatcherArgFloor = 18

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

	checked, dispatcherChecked := 0, 0
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
				dispatcherChecked++
				assertNoWorkspaceArg(t, fset, typeSpec.Name.Name, structType)
			}
		}
	}
	if checked < workspaceArgFloor {
		t.Fatalf("inspected only %d workspace-scoped args types, expected at least %d — the walker matched nothing and this gate would pass vacuously", checked, workspaceArgFloor)
	}
	if dispatcherChecked < dispatcherArgFloor {
		t.Fatalf("inspected only %d dispatcher (FleetWide) args types, expected at least %d — the walker matched nothing on the dispatcher side and this gate would pass vacuously", dispatcherChecked, dispatcherArgFloor)
	}
}

// assertWorkspaceArg checks one args struct carries its workspace under the
// sanctioned name, type, and wire key — and carries it exactly ONCE. Stopping
// at the first field that looks right would miss the second field claiming the
// same key, which is the one way a type can satisfy every per-field rule here
// and still ship no workspace at all.
func assertWorkspaceArg(t *testing.T, fset *token.FileSet, typeName string, structType *ast.StructType) {
	t.Helper()
	carriers, declared := 0, false
	for _, field := range structType.Fields.List {
		if jsonKey(t, fset, typeName, field) == workspaceArgKey {
			carriers++
		}
		if !declaresName(field, workspaceArgField) {
			continue
		}
		declared = true
		pos := fset.Position(field.Pos())
		if got := types.ExprString(field.Type); got != workspaceArgType {
			t.Errorf("%s:%d: %s.%s is %s, want %s — workspaceJobCtx binds one type.",
				pos.Filename, pos.Line, typeName, workspaceArgField, got, workspaceArgType)
		}
		if field.Tag == nil {
			t.Errorf("%s:%d: %s.%s carries no struct tag, want `json:%q` — an untagged field ships as %q and args->>'workspace_id' misses it.",
				pos.Filename, pos.Line, typeName, workspaceArgField, workspaceArgKey, workspaceArgField)
			continue
		}
		if got := jsonKey(t, fset, typeName, field); got != workspaceArgKey {
			t.Errorf("%s:%d: %s.%s ships as json:%q, want json:%q — a divergent key is invisible to args->>'workspace_id', and a null there reads as a dispatcher rather than as tenant work the query cannot see.",
				pos.Filename, pos.Line, typeName, workspaceArgField, got, workspaceArgKey)
		}
	}

	pos := fset.Position(structType.Pos())
	if !declared {
		t.Errorf("%s:%d: %s declares WorkspaceID() but declares no %s field — an embedded type does not count, because this gate reads declared fields; want `%s %s` tagged json:%q, since the accessor has to return something the wire carries.",
			pos.Filename, pos.Line, typeName, workspaceArgField, workspaceArgField, workspaceArgType, workspaceArgKey)
	}
	if carriers > 1 {
		t.Errorf("%s:%d: %s declares %d fields under json:%q, want exactly one — encoding/json drops ALL of a set of conflicting fields at the same depth, so this type would carry no workspace on the wire at all and args->>'workspace_id' would read null for work a tenant really did.",
			pos.Filename, pos.Line, typeName, carriers, workspaceArgKey)
	}
}

// declaresName reports whether a struct field is declared under the given name.
// An embedded field has no names and so is never one — deliberately: this gate
// reads the fields declared here and cannot see into another package's type.
func declaresName(field *ast.Field, want string) bool {
	for _, name := range field.Names {
		if name.Name == want {
			return true
		}
	}
	return false
}

// assertNoWorkspaceArg holds the other half: a dispatcher does no tenant work,
// so it must not ship the key that says it did.
func assertNoWorkspaceArg(t *testing.T, fset *token.FileSet, typeName string, structType *ast.StructType) {
	t.Helper()
	for _, field := range structType.Fields.List {
		if len(field.Names) == 0 {
			// encoding/json flattens an embedded field's own fields into the
			// marshaled object, but this walker only reads declared fields —
			// it cannot see into another package's type to know whether the
			// embedded type carries a workspace_id. Require the field
			// declared here instead of resolving through it.
			pos := fset.Position(field.Pos())
			t.Errorf("%s:%d: %s embeds %s — a dispatcher's args must declare their fields, not embed them, because this gate reads declared fields and cannot see through an embedded type to a workspace key it might carry.",
				pos.Filename, pos.Line, typeName, types.ExprString(field.Type))
			continue
		}
		if jsonKey(t, fset, typeName, field) != workspaceArgKey {
			continue
		}
		pos := fset.Position(field.Pos())
		t.Errorf("%s:%d: %s is a dispatcher (it declares FleetWide()) but ships a json:%q key — a non-null workspace_id has to mean tenant work, or a per-workspace read of river_job counts a fan-out as a tenant's pass. If this job does one workspace's work, declare WorkspaceID() %s instead of FleetWide(); if it dispatches, the workspace belongs on the child jobs it enqueues, not on its own args.",
			pos.Filename, pos.Line, typeName, workspaceArgKey, workspaceArgType)
	}
}

// jsonKey returns a field's wire name, dropping the `,omitempty`-style options
// that would otherwise make an equality check miss. strconv.Unquote handles
// both a raw-string tag and a quoted-string tag — trimming backticks alone
// would silently misparse the second spelling into an empty key, which on the
// dispatcher side reads as "no workspace_id" for a field that ships one.
func jsonKey(t *testing.T, fset *token.FileSet, typeName string, field *ast.Field) string {
	t.Helper()
	if field.Tag == nil {
		return ""
	}
	raw, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		pos := fset.Position(field.Tag.Pos())
		t.Fatalf("%s:%d: %s's struct tag %s will not unquote: %v — a tag this gate cannot parse is a tag it cannot verify.",
			pos.Filename, pos.Line, typeName, field.Tag.Value, err)
	}
	tag := reflect.StructTag(raw).Get("json")
	name, _, _ := strings.Cut(tag, ",")
	return name
}

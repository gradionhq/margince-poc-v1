// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// Deriving the legacy cohort's NAMES from history (rbaclegacyinstall_test.go)
// closes half the hole. This file closes the other half.
//
// The migration replay seeds the committed legacy documents and asserts the
// upgraded end state matches what the server seeds today. If those documents
// carried hand-written GRANTS, the replay would be defeatable the same way the
// old exemption list was: a developer whose backfill does not work could edit
// the starting state to already contain the grant the migration failed to
// deliver, and the replay would go green on a broken upgrade. Nothing would
// have flagged it, because the object NAMES would not have changed.
//
// So the grants are derived too. This evaluates the `defaults` declaration as
// it stood at legacyCommit and asserts the committed fixture reproduces it
// exactly — every role, every object, every verb, and the row scope.
//
// The historical file is frozen forever, so this evaluator only ever has to
// understand the three forms that file actually uses: a named grant variable
// (`crud`, `readOnly`), an inline `grant{...}` composite literal, and a
// `crmctx.RowScope*` selector.

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// legacyGrant is one object's grant in the initial commit's declaration.
type legacyGrant struct {
	Create bool `json:"create"`
	Read   bool `json:"read"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

func TestLegacyInstallFixtureReproducesTheInitialCommitGrants(t *testing.T) {
	file := parseLegacyPolicy(t)
	objects := legacyObjectsInDeclarationOrder(t, file)
	derived := legacyDefaults(t, file, objects)

	fixture := readLegacyInstalls(t)
	install, ok := fixture.Installs["initial_commit"]
	if !ok {
		t.Fatalf("%s carries no 'initial_commit' installation", legacyInstallsFixture)
	}
	if len(derived) != len(install) {
		t.Errorf("%s:%s declares %d system roles, the initial_commit fixture carries %d",
			legacyCommit, legacyPolicyPath, len(derived), len(install))
	}

	for role, want := range derived {
		got, present := install[role]
		if !present {
			t.Errorf("the initial_commit fixture is missing role %q, which %s declares",
				role, legacyCommit)
			continue
		}
		compareLegacyDocument(t, role, got, want)
	}
}

func compareLegacyDocument(t *testing.T, role string, got installDocument, want legacyDocument) {
	t.Helper()
	if got.RowScope != want.RowScope {
		t.Errorf("role %q in the initial_commit fixture has row_scope %q, but %s declares %q",
			role, got.RowScope, legacyCommit, want.RowScope)
	}
	for object, wantGrant := range want.Objects {
		gotGrant, err := decodeGrant(got.Objects[object])
		if err != nil {
			t.Errorf("role %q's %s grant in the initial_commit fixture is not a grant document: %v",
				role, object, err)
			continue
		}
		if gotGrant != wantGrant {
			t.Errorf("role %q holds %s grant %+v in the initial_commit fixture, but %s declares %+v.\n"+
				"These documents are the state the migration replay starts from. Editing a grant into "+
				"them makes a backfill that never ran look as though it did.",
				role, object, gotGrant, legacyCommit, wantGrant)
		}
	}
}

type legacyDocument struct {
	Objects  map[string]legacyGrant
	RowScope string
}

// legacyDefaults evaluates the historical `defaults` map into role documents.
func legacyDefaults(t *testing.T, file *ast.File, objects []string) map[string]legacyDocument {
	t.Helper()
	named := namedGrants(t, file)
	documents := map[string]legacyDocument{}
	for _, element := range compositeElements(t, file, "defaults") {
		pair, isPair := element.(*ast.KeyValueExpr)
		if !isPair {
			continue
		}
		role := mustStringLiteral(t, pair.Key)
		value, isLiteral := pair.Value.(*ast.CompositeLit)
		if !isLiteral {
			t.Fatalf("role %q's default is not a composite literal", role)
		}
		documents[role] = legacyDocument{
			Objects:  zipObjects(t, role, value, named, objects),
			RowScope: rowScopeOf(t, role, value),
		}
	}
	if len(documents) == 0 {
		t.Fatalf("evaluated no role documents from %s:%s; the declaration has moved",
			legacyCommit, legacyPolicyPath)
	}
	return documents
}

// zipObjects reads the positional objects(...) call and zips it onto the
// vocabulary in declaration order, exactly as the historical helper did.
func zipObjects(t *testing.T, role string, document *ast.CompositeLit, named map[string]legacyGrant, objects []string) map[string]legacyGrant {
	t.Helper()
	call := fieldValue(t, role, document, "Objects")
	invocation, isCall := call.(*ast.CallExpr)
	if !isCall {
		t.Fatalf("role %q's Objects is not an objects(...) call", role)
	}
	if len(invocation.Args) != len(objects) {
		t.Fatalf("role %q passes %d grants to objects(), but the vocabulary has %d entries",
			role, len(invocation.Args), len(objects))
	}
	grants := map[string]legacyGrant{}
	for i, arg := range invocation.Args {
		grants[objects[i]] = evaluateGrant(t, role, arg, named)
	}
	return grants
}

// evaluateGrant resolves one argument: a named grant variable or an inline
// grant{...} literal.
func evaluateGrant(t *testing.T, role string, expr ast.Expr, named map[string]legacyGrant) legacyGrant {
	t.Helper()
	switch value := expr.(type) {
	case *ast.Ident:
		grant, known := named[value.Name]
		if !known {
			t.Fatalf("role %q names grant %q, which the declaration does not define", role, value.Name)
		}
		return grant
	case *ast.CompositeLit:
		return grantLiteral(t, value)
	default:
		t.Fatalf("role %q carries a grant this evaluator does not understand (%T)", role, expr)
		return legacyGrant{}
	}
}

// namedGrants reads the `var (crud = grant{...}; readOnly = grant{...})` block.
func namedGrants(t *testing.T, file *ast.File) map[string]legacyGrant {
	t.Helper()
	grants := map[string]legacyGrant{}
	for _, decl := range file.Decls {
		gd, isGen := decl.(*ast.GenDecl)
		if !isGen || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, isValue := spec.(*ast.ValueSpec)
			if !isValue || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			literal, isLiteral := vs.Values[0].(*ast.CompositeLit)
			if !isLiteral || !isGrantLiteral(literal) {
				continue
			}
			grants[vs.Names[0].Name] = grantLiteral(t, literal)
		}
	}
	if len(grants) == 0 {
		t.Fatalf("found no named grant variables in %s:%s", legacyCommit, legacyPolicyPath)
	}
	return grants
}

func isGrantLiteral(literal *ast.CompositeLit) bool {
	name, isIdent := literal.Type.(*ast.Ident)
	return isIdent && name.Name == "grant"
}

// grantLiteral reads a grant{Create: true, ...} literal. An omitted field is
// false, which is Go's own zero value and the declaration's intent.
func grantLiteral(t *testing.T, literal *ast.CompositeLit) legacyGrant {
	t.Helper()
	var grant legacyGrant
	for _, element := range literal.Elts {
		pair, isPair := element.(*ast.KeyValueExpr)
		if !isPair {
			t.Fatalf("grant literal uses positional fields; this evaluator reads keyed ones")
		}
		field, isIdent := pair.Key.(*ast.Ident)
		if !isIdent {
			t.Fatalf("grant literal has a non-identifier field name")
		}
		set := isTrue(t, pair.Value)
		switch field.Name {
		case "Create":
			grant.Create = set
		case "Read":
			grant.Read = set
		case "Update":
			grant.Update = set
		case "Delete":
			grant.Delete = set
		default:
			t.Fatalf("grant literal carries unknown field %q", field.Name)
		}
	}
	return grant
}

func isTrue(t *testing.T, expr ast.Expr) bool {
	t.Helper()
	ident, isIdent := expr.(*ast.Ident)
	if !isIdent || (ident.Name != "true" && ident.Name != "false") {
		t.Fatalf("grant field is not a bool literal")
	}
	return ident.Name == "true"
}

// rowScopeOf reads `RowScope: crmctx.RowScopeAll` and lowercases the token, so
// the derived value is the wire spelling the fixture stores.
func rowScopeOf(t *testing.T, role string, document *ast.CompositeLit) string {
	t.Helper()
	selector, isSelector := fieldValue(t, role, document, "RowScope").(*ast.SelectorExpr)
	if !isSelector {
		t.Fatalf("role %q's RowScope is not a package-qualified constant", role)
	}
	token, found := strings.CutPrefix(selector.Sel.Name, "RowScope")
	if !found || token == "" {
		t.Fatalf("role %q's RowScope names %q, which is not a RowScope* constant", role, selector.Sel.Name)
	}
	return strings.ToLower(token)
}

func fieldValue(t *testing.T, role string, literal *ast.CompositeLit, field string) ast.Expr {
	t.Helper()
	for _, element := range literal.Elts {
		pair, isPair := element.(*ast.KeyValueExpr)
		if !isPair {
			continue
		}
		if name, isIdent := pair.Key.(*ast.Ident); isIdent && name.Name == field {
			return pair.Value
		}
	}
	t.Fatalf("role %q's document declares no %s", role, field)
	return nil
}

// legacyObjectsInDeclarationOrder returns the vocabulary UNSORTED — the
// positional objects(...) call is zipped against declaration order, so sorting
// here would silently transpose every grant.
func legacyObjectsInDeclarationOrder(t *testing.T, file *ast.File) []string {
	t.Helper()
	objects := coreObjectsIn(t, file)
	if len(objects) == 0 {
		t.Fatalf("parsed no objects from %s:%s", legacyCommit, legacyPolicyPath)
	}
	return objects
}

func compositeElements(t *testing.T, file *ast.File, name string) []ast.Expr {
	t.Helper()
	for _, decl := range file.Decls {
		gd, isGen := decl.(*ast.GenDecl)
		if !isGen || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, isValue := spec.(*ast.ValueSpec)
			if !isValue || len(vs.Names) != 1 || vs.Names[0].Name != name || len(vs.Values) != 1 {
				continue
			}
			literal, isLiteral := vs.Values[0].(*ast.CompositeLit)
			if !isLiteral {
				t.Fatalf("%s is not a composite literal", name)
			}
			return literal.Elts
		}
	}
	t.Fatalf("%s:%s declares no %s", legacyCommit, legacyPolicyPath, name)
	return nil
}

func mustStringLiteral(t *testing.T, expr ast.Expr) string {
	t.Helper()
	basic, isBasic := expr.(*ast.BasicLit)
	if !isBasic || basic.Kind != token.STRING {
		t.Fatalf("expected a string literal key")
	}
	// Unquote, not a quote trim: a trim silently mis-decodes any escape and
	// would hand back a role key that matches nothing, reading as a missing
	// role rather than as the malformed literal it is.
	unquoted, err := strconv.Unquote(basic.Value)
	if err != nil {
		t.Fatalf("unquoting role key %s: %v", basic.Value, err)
	}
	return unquoted
}

func parseLegacyPolicy(t *testing.T) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), legacyPolicyPath, gitShow(t, legacyPolicyPath), 0)
	if err != nil {
		t.Fatalf("parsing %s:%s: %v", legacyCommit, legacyPolicyPath, err)
	}
	return file
}

// decodeGrant reads a fixture grant strictly, so a malformed one is reported
// rather than silently read as all-false.
func decodeGrant(raw json.RawMessage) (legacyGrant, error) {
	if len(raw) == 0 {
		return legacyGrant{}, errors.New("the object is absent from the document")
	}
	var grant legacyGrant
	if err := json.Unmarshal(raw, &grant); err != nil {
		return legacyGrant{}, err
	}
	return grant, nil
}

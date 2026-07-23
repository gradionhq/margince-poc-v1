// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// buildIncludesFile reports whether the go tool would compile a file of
// this name into the (non-test) package. Mirroring go/build: a name
// beginning with '.' or '_' is ignored, and a _test.go file is test-only.
// The AST derivation must read exactly what compiles — parsing an ignored
// file could bind the manifest to a New() the build never sees.
func buildIncludesFile(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return false
	}
	return !strings.HasSuffix(name, "_test.go")
}

func deriveUnitManifest(u extensionUnit, vocab map[string]string) ([]byte, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, u.Dir, func(fi fs.FileInfo) bool { return buildIncludesFile(fi.Name()) }, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("extensions/%s: %w", u.Name, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("extensions/%s: the unit root must hold exactly one package, found %d", u.Name, len(pkgs))
	}
	r := &unitReader{fset: fset, vocab: vocab}
	newFn, newFile, count := findNew(pkgs)
	if count == 0 {
		return nil, fmt.Errorf("extensions/%s: no New() in the unit root package — the declaration constructor is the ADR-0069 §4 contract", u.Name)
	}
	if count > 1 {
		// Map iteration over pkg.Files is unordered, so picking one of
		// several New() would make the manifest nondeterministic; a
		// build-tag-split constructor cannot be resolved statically either.
		return nil, fmt.Errorf("extensions/%s: multiple New() constructors in the unit root — the static manifest reader needs exactly one (build-tag-split declarations are unsupported)", u.Name)
	}
	m, err := r.readExtension(newFn, newFile)
	if err != nil {
		return nil, err
	}
	if m.Name != u.Name {
		return nil, fmt.Errorf("extensions/%s: New() declares name %q — the directory name IS the unit name", u.Name, m.Name)
	}
	return encodeUnitManifest(m)
}

func findNew(pkgs map[string]*ast.Package) (fn *ast.FuncDecl, file *ast.File, count int) {
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				if d, ok := decl.(*ast.FuncDecl); ok && d.Recv == nil && d.Name.Name == "New" {
					fn, file, count = d, f, count+1
				}
			}
		}
	}
	return fn, file, count
}

func encodeUnitManifest(m unitManifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unitReader walks one unit's declaration AST. Everything it reads is a
// LITERAL: the declaration idiom (ADR-0069 §4/§5) requires New() to
// return a literal so the manifest derives without compiling — a computed
// value is a hard error naming the position, never a silent gap.
type unitReader struct {
	fset  *token.FileSet
	vocab map[string]string
}

func (r *unitReader) readExtension(fn *ast.FuncDecl, file *ast.File) (unitManifest, error) {
	expr, err := r.singleReturn(fn)
	if err != nil {
		return unitManifest{}, err
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok || !isSelector(lit.Type, importAlias(file, extensionPkgPath), "Extension") {
		return unitManifest{}, r.errAt(expr, "New must return an extension.Extension literal")
	}
	m := unitManifest{Schema: 1, AutonomyTiers: []autonomyTierRequest{}}
	for _, elt := range lit.Elts {
		if err := r.readExtensionField(elt, file, &m); err != nil {
			return unitManifest{}, err
		}
	}
	// Validate identity through the published grammar the boot preflight
	// runs, so gen-time acceptance cannot diverge from boot-time: an empty,
	// whitespace-framed, or non-printable Version passes neither. These are
	// SEMANTIC errors — the value is a literal, just an invalid one — so
	// they carry position but not the "declare literal values" prescription.
	if err := extension.Name(m.Name).Validate(); err != nil {
		return unitManifest{}, r.errPos(lit, "%v", err)
	}
	if err := extension.Version(m.Version).Validate(); err != nil {
		return unitManifest{}, r.errPos(lit, "%v", err)
	}
	sort.Slice(m.AutonomyTiers, func(i, j int) bool { return m.AutonomyTiers[i].ID < m.AutonomyTiers[j].ID })
	return m, nil
}

func (r *unitReader) readExtensionField(elt ast.Expr, file *ast.File, m *unitManifest) error {
	kv, ok := elt.(*ast.KeyValueExpr)
	if !ok {
		return r.errAt(elt, "Extension fields must be keyed")
	}
	key, ok := kv.Key.(*ast.Ident)
	if !ok {
		return r.errAt(kv.Key, "Extension fields must be keyed by name")
	}
	var err error
	switch key.Name {
	case "Name":
		m.Name, err = r.stringLit(kv.Value, "Name")
	case "Version":
		m.Version, err = r.stringLit(kv.Value, "Version")
	case "Tools":
		var tiers []autonomyTierRequest
		tiers, err = r.readTools(kv.Value, file)
		if err == nil {
			m.AutonomyTiers = append(m.AutonomyTiers, tiers...)
		}
	case "Jurisdictions":
		// Recognized and deliberately skipped: a jurisdiction pack is
		// passive policy the core consults, never a governed operation an
		// operator resolves (§7), so it contributes no manifest entry.
	default:
		// Fail closed: a field this generator does not recognize could be a
		// future governed capability, and a manifest that silently omitted
		// it would hide a request from the operator.
		err = r.errAt(kv, "Extension field %s is not derivable by this generator — teach the manifest reader before declaring it", key.Name)
	}
	return err
}

func (r *unitReader) readTools(expr ast.Expr, file *ast.File) ([]autonomyTierRequest, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Tools must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	tiers := make([]autonomyTierRequest, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		c, err := r.readTool(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[c.ID] {
			return nil, r.errAt(elt, "governed operation %s declared twice", c.ID)
		}
		seen[c.ID] = true
		tiers = append(tiers, c)
	}
	return tiers, nil
}

func (r *unitReader) readTool(elt ast.Expr, ext string) (autonomyTierRequest, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "Tool")) {
		return autonomyTierRequest{}, r.errAt(elt, "a Tools entry must be an extension.Tool literal")
	}
	var name, version, tier, scope string
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return autonomyTierRequest{}, r.errAt(e, "Tool fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return autonomyTierRequest{}, r.errAt(kv.Key, "Tool fields must be keyed by name")
		}
		var err error
		switch key.Name {
		case "Name":
			name, err = r.stringLit(kv.Value, "Tool.Name")
		case "Version":
			version, err = r.stringLit(kv.Value, "Tool.Version")
		case "Tier":
			tier, err = r.constValue(kv.Value, ext)
		case "RequestedScope":
			scope, err = r.constValue(kv.Value, ext)
		default:
			err = r.errAt(kv, "Tool field %s is not derivable by this generator", key.Name)
		}
		if err != nil {
			return autonomyTierRequest{}, err
		}
	}
	return r.toolRequest(lit, name, version, tier, scope)
}

// toolRequest validates the declared tool through its published grammar
// (the same Validate the boot preflight runs, raised here at the
// declaration's position) and assembles its descriptor. A tool requires
// one scope; the descriptor carries it as its (single-element) scope set,
// the general §5 shape shared across governed kinds. Version is not part
// of the descriptor: §7 binds resolutions to the digest, never a version.
func (r *unitReader) toolRequest(at ast.Node, name, version, tier, scope string) (autonomyTierRequest, error) {
	declared := extension.Tool{Name: name, Version: version, Tier: extension.Tier(tier), RequestedScope: extension.Scope(scope)}
	if err := declared.Validate(); err != nil {
		return autonomyTierRequest{}, r.errPos(at, "%v", err)
	}
	c := autonomyTierRequest{
		ID:        "tool/" + name,
		Operation: opAgentToolInvoke,
		Scopes:    []string{scope},
		Tier:      tier,
	}
	digest, err := descriptorDigest(c)
	if err != nil {
		return autonomyTierRequest{}, err
	}
	c.Digest = digest
	return c, nil
}

// constValue resolves a published constant (extension.X) through the
// source-derived vocabulary, or accepts a plain string literal.
func (r *unitReader) constValue(expr ast.Expr, ext string) (string, error) {
	switch v := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok || pkg.Name != ext {
			return "", r.errAt(expr, "constants must come from the published extension package")
		}
		value, ok := r.vocab[v.Sel.Name]
		if !ok {
			return "", r.errAt(expr, "%s.%s is not a published extension constant", pkg.Name, v.Sel.Name)
		}
		return value, nil
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", r.errAt(expr, "expected a string literal or a published extension constant")
		}
		return strconv.Unquote(v.Value)
	}
	return "", r.errAt(expr, "expected a string literal or a published extension constant")
}

// errAt names the position and restates the rule: the fix is to make the
// declaration a literal, so a SHAPE error (a computed value, a non-literal
// field) carries that prescription.
func (r *unitReader) errAt(n ast.Node, format string, args ...any) error {
	return fmt.Errorf("%s: %s — manifest derivation reads declarations statically; declare literal values (ADR-0069 §5)",
		r.fset.Position(n.Pos()), fmt.Sprintf(format, args...))
}

// errPos names the position only, for a SEMANTIC error (a literal that is
// present but invalid — a bad version, an out-of-vocabulary scope) whose
// fix is not "make it a literal".
func (r *unitReader) errPos(n ast.Node, format string, args ...any) error {
	return fmt.Errorf("%s: %s", r.fset.Position(n.Pos()), fmt.Sprintf(format, args...))
}

// singleReturn enforces the declaration-constructor shape: exactly one
// statement, a return of exactly one expression.
func (r *unitReader) singleReturn(fn *ast.FuncDecl) (ast.Expr, error) {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return nil, r.errAt(fn, "%s must hold exactly one return statement", fn.Name.Name)
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil, r.errAt(fn, "%s must hold exactly one return statement", fn.Name.Name)
	}
	return ret.Results[0], nil
}

func (r *unitReader) stringLit(expr ast.Expr, field string) (string, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", r.errAt(expr, "%s must be a string literal", field)
	}
	return strconv.Unquote(lit.Value)
}

// importAlias resolves the file-local name of an imported package path.
func importAlias(file *ast.File, path string) string {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return p[strings.LastIndex(p, "/")+1:]
	}
	return ""
}

func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && pkg != "" && ident.Name == pkg && sel.Sel.Name == name
}

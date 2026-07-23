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

	"github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

// unitReader walks one unit's declaration AST. Everything it accepts is
// a LITERAL: the declaration idiom (ADR-0069 §4/§5) requires New() and
// the pack methods it references to return literal values, so the
// manifest can be derived without compiling — a computed value is a
// hard error naming the position, never a silent gap.
type unitReader struct {
	unit    string
	fset    *token.FileSet
	vocab   map[string]string
	methods map[string]map[string]methodDecl
}

// methodDecl keeps the declaring file with the method: selector
// resolution (import aliases) is per-file.
type methodDecl struct {
	fn   *ast.FuncDecl
	file *ast.File
}

func deriveUnitManifest(u extensionUnit, vocab map[string]string) ([]byte, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, u.Dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("extensions/%s: %w", u.Name, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("extensions/%s: the unit root must hold exactly one package, found %d", u.Name, len(pkgs))
	}
	r := &unitReader{unit: u.Name, fset: fset, vocab: vocab, methods: map[string]map[string]methodDecl{}}
	var newFn *ast.FuncDecl
	var newFile *ast.File
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fn.Recv == nil {
					if fn.Name.Name == "New" {
						newFn, newFile = fn, file
					}
					continue
				}
				recv, ok := receiverType(fn)
				if !ok {
					continue
				}
				if r.methods[recv] == nil {
					r.methods[recv] = map[string]methodDecl{}
				}
				r.methods[recv][fn.Name.Name] = methodDecl{fn: fn, file: file}
			}
		}
	}
	if newFn == nil {
		return nil, fmt.Errorf("extensions/%s: no New() in the unit root package — the declaration constructor is the ADR-0069 §4 contract", u.Name)
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

func encodeUnitManifest(m unitManifest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func receiverType(fn *ast.FuncDecl) (string, bool) {
	if len(fn.Recv.List) != 1 {
		return "", false
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

// errAt names the position and restates the rule: the fix is always
// "make the declaration a literal", so every message carries it.
func (r *unitReader) errAt(n ast.Node, format string, args ...any) error {
	return fmt.Errorf("%s: %s — manifest derivation reads declarations statically; declare literal values (ADR-0069 §5)",
		r.fset.Position(n.Pos()), fmt.Sprintf(format, args...))
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

const (
	extensionPkgPath    = "github.com/gradionhq/margince/backend/pkg/extension"
	jurisdictionPkgPath = "github.com/gradionhq/margince/backend/pkg/extension/jurisdiction"
)

func (r *unitReader) readExtension(fn *ast.FuncDecl, file *ast.File) (unitManifest, error) {
	expr, err := r.singleReturn(fn)
	if err != nil {
		return unitManifest{}, err
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok || !isSelector(lit.Type, importAlias(file, extensionPkgPath), "Extension") {
		return unitManifest{}, r.errAt(expr, "New must return an extension.Extension literal")
	}
	m := unitManifest{Schema: 1, Capabilities: []unitCapability{}}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return unitManifest{}, r.errAt(elt, "Extension fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return unitManifest{}, r.errAt(kv.Key, "Extension fields must be keyed by name")
		}
		switch key.Name {
		case "Name":
			m.Name, err = r.stringLit(kv.Value, "Name")
		case "Version":
			m.Version, err = r.stringLit(kv.Value, "Version")
		case "Jurisdictions":
			m.Capabilities, err = r.readJurisdictions(kv.Value, file)
		default:
			// Fail closed: a field this generator version cannot derive
			// must never silently vanish from the manifest reviewers and
			// approvals rely on.
			err = r.errAt(kv, "Extension field %s is not derivable by this generator", key.Name)
		}
		if err != nil {
			return unitManifest{}, err
		}
	}
	if m.Name == "" || m.Version == "" {
		return unitManifest{}, r.errAt(lit, "the Extension literal must declare Name and Version")
	}
	sort.Slice(m.Capabilities, func(i, j int) bool { return m.Capabilities[i].ID < m.Capabilities[j].ID })
	return m, nil
}

func (r *unitReader) stringLit(expr ast.Expr, field string) (string, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", r.errAt(expr, "%s must be a string literal", field)
	}
	return strconv.Unquote(lit.Value)
}

func (r *unitReader) readJurisdictions(expr ast.Expr, file *ast.File) ([]unitCapability, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Jurisdictions must be a slice literal")
	}
	caps := make([]unitCapability, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		if unary, ok := elt.(*ast.UnaryExpr); ok && unary.Op == token.AND {
			elt = unary.X
		}
		packLit, ok := elt.(*ast.CompositeLit)
		if !ok {
			return nil, r.errAt(elt, "a Jurisdictions entry must be a pack literal")
		}
		typeName, ok := packLit.Type.(*ast.Ident)
		if !ok {
			return nil, r.errAt(packLit, "a pack must be a type of the unit's own package")
		}
		claim, err := r.readPack(typeName.Name, packLit)
		if err != nil {
			return nil, err
		}
		c := unitCapability{
			ID:           "jurisdiction/" + claim.Code,
			Operation:    opRegisterJurisdictionPack,
			Scopes:       []string{},
			Jurisdiction: claim,
		}
		if seen[c.ID] {
			return nil, r.errAt(packLit, "capability %s declared twice", c.ID)
		}
		seen[c.ID] = true
		c.Digest, err = descriptorDigest(c)
		if err != nil {
			return nil, err
		}
		caps = append(caps, c)
	}
	return caps, nil
}

func (r *unitReader) readPack(typeName string, at ast.Node) (*jurisdictionClaim, error) {
	codeFn, ok := r.methods[typeName]["Code"]
	if !ok {
		return nil, r.errAt(at, "pack type %s declares no Code() method", typeName)
	}
	codeExpr, err := r.singleReturn(codeFn.fn)
	if err != nil {
		return nil, err
	}
	code, err := r.stringLit(codeExpr, "Code()")
	if err != nil {
		return nil, err
	}
	if err := jurisdiction.Code(code).Validate(); err != nil {
		return nil, r.errAt(codeExpr, "%v", err)
	}
	claim := &jurisdictionClaim{Code: code, Retention: []retentionRow{}}

	retFn, ok := r.methods[typeName]["Retention"]
	if !ok {
		return nil, r.errAt(at, "pack type %s declares no Retention() method", typeName)
	}
	retExpr, err := r.singleReturn(retFn.fn)
	if err != nil {
		return nil, err
	}
	if ident, ok := retExpr.(*ast.Ident); ok && ident.Name == "nil" {
		return claim, nil
	}
	retLit, ok := retExpr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(retExpr, "Retention() must return nil or a literal of a local retention type")
	}
	retType, ok := retLit.Type.(*ast.Ident)
	if !ok {
		return nil, r.errAt(retLit, "Retention() must return nil or a literal of a local retention type")
	}
	classesFn, ok := r.methods[retType.Name]["Classes"]
	if !ok {
		return nil, r.errAt(retLit, "retention type %s declares no Classes() method", retType.Name)
	}
	claim.Retention, err = r.readClasses(classesFn)
	return claim, err
}

func (r *unitReader) readClasses(m methodDecl) ([]retentionRow, error) {
	expr, err := r.singleReturn(m.fn)
	if err != nil {
		return nil, err
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Classes() must return a slice literal")
	}
	jur := importAlias(m.file, jurisdictionPkgPath)
	rows := make([]retentionRow, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		classLit, ok := elt.(*ast.CompositeLit)
		if !ok {
			return nil, r.errAt(elt, "a retention class must be a literal")
		}
		row, err := r.readClass(classLit, jur)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (r *unitReader) readClass(lit *ast.CompositeLit, jurAlias string) (retentionRow, error) {
	row := retentionRow{Anchor: string(jurisdiction.AnchorOccurrence)}
	var keep jurisdiction.Period
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return retentionRow{}, r.errAt(elt, "retention class fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return retentionRow{}, r.errAt(kv.Key, "retention class fields must be keyed by name")
		}
		var err error
		switch key.Name {
		case "Name":
			row.Name, err = r.vocabValue(kv.Value, jurAlias)
			if err == nil {
				err = validateAt(r, kv.Value, jurisdiction.RetentionClassName(row.Name).Validate())
			}
		case "Keep":
			keep, err = r.readPeriod(kv.Value, jurAlias)
		case "Anchor":
			row.Anchor, err = r.vocabValue(kv.Value, jurAlias)
			if err == nil {
				err = validateAt(r, kv.Value, jurisdiction.Anchor(row.Anchor).Validate())
			}
		default:
			err = r.errAt(kv, "retention class field %s is not derivable by this generator", key.Name)
		}
		if err != nil {
			return retentionRow{}, err
		}
	}
	if err := validateAt(r, lit, keep.Validate()); err != nil {
		return retentionRow{}, err
	}
	row.Keep = keep.String()
	return row, nil
}

// validateAt lifts a published-surface validation error to the
// declaration's position — the same rule the boot preflight enforces,
// caught at gen time where the author still holds the file.
func validateAt(r *unitReader, at ast.Node, err error) error {
	if err == nil {
		return nil
	}
	return r.errAt(at, "%v", err)
}

// vocabValue resolves a published constant (jurisdiction.X) via the
// source-derived vocabulary, or accepts a plain string literal.
func (r *unitReader) vocabValue(expr ast.Expr, jurAlias string) (string, error) {
	switch v := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok || pkg.Name != jurAlias {
			return "", r.errAt(expr, "constants must come from the published jurisdiction package")
		}
		value, ok := r.vocab[v.Sel.Name]
		if !ok {
			return "", r.errAt(expr, "%s.%s is not a published jurisdiction constant", pkg.Name, v.Sel.Name)
		}
		return value, nil
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", r.errAt(expr, "expected a string literal or a published jurisdiction constant")
		}
		return strconv.Unquote(v.Value)
	}
	return "", r.errAt(expr, "expected a string literal or a published jurisdiction constant")
}

func (r *unitReader) readPeriod(expr ast.Expr, jurAlias string) (jurisdiction.Period, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok || !isSelector(lit.Type, jurAlias, "Period") {
		return jurisdiction.Period{}, r.errAt(expr, "Keep must be a jurisdiction.Period literal")
	}
	var p jurisdiction.Period
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return jurisdiction.Period{}, r.errAt(elt, "Period fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return jurisdiction.Period{}, r.errAt(kv.Key, "Period fields must be keyed by name")
		}
		// A negative component is written as unary minus over the
		// literal; unwrap it so the value reaches Period.Validate and
		// fails with the retention-floor rule, not a shape error.
		value, negative := kv.Value, false
		if unary, ok := value.(*ast.UnaryExpr); ok && unary.Op == token.SUB {
			value, negative = unary.X, true
		}
		basic, ok := value.(*ast.BasicLit)
		if !ok || basic.Kind != token.INT {
			return jurisdiction.Period{}, r.errAt(kv.Value, "Period.%s must be an integer literal", key.Name)
		}
		n, err := strconv.Atoi(basic.Value)
		if err != nil {
			return jurisdiction.Period{}, r.errAt(kv.Value, "Period.%s: %v", key.Name, err)
		}
		if negative {
			n = -n
		}
		switch key.Name {
		case "Years":
			p.Years = n
		case "Months":
			p.Months = n
		case "Days":
			p.Days = n
		default:
			return jurisdiction.Period{}, r.errAt(kv, "Period field %s is not derivable by this generator", key.Name)
		}
	}
	return p, nil
}

func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && pkg != "" && ident.Name == pkg && sel.Sel.Name == name
}

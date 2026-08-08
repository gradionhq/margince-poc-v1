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

// scannableGoFile reports whether to parse this .go file for the
// declaration scan. It excludes only what go/build ignores BY NAME — a
// name beginning with '.' or '_', and _test.go test files. It deliberately
// does NOT apply //go:build constraints or GOOS/GOARCH suffixes: the scan
// is platform-independent ON PURPOSE, so the committed manifest is the
// same on every host. A build-tag/GOOS-split New() is therefore parsed on
// all platforms and rejected by the multiple-New guard rather than
// resolved per-context (which would make the manifest platform-dependent).
func scannableGoFile(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return false
	}
	return !strings.HasSuffix(name, "_test.go")
}

func deriveUnitManifest(u extensionUnit, vocab map[string]string, verbs []declaredVerb, jobDecls []extension.JobDeclaration) ([]byte, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, u.Dir, func(fi fs.FileInfo) bool { return scannableGoFile(fi.Name()) }, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("extensions/%s: %w", u.Name, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("extensions/%s: the unit root must hold exactly one package, found %d", u.Name, len(pkgs))
	}
	if err := rejectLiveInitializers(pkgs, fset); err != nil {
		return nil, fmt.Errorf("extensions/%s: %w", u.Name, err)
	}
	r := &unitReader{fset: fset, vocab: vocab, verbs: verbs, jobs: jobDecls}
	newFn, newFile, count := findNew(pkgs)
	if count == 0 {
		return nil, fmt.Errorf("extensions/%s: no New() in the unit root package — the declaration constructor is required", u.Name)
	}
	if count > 1 {
		// The scan is platform-independent (build tags/GOOS are not
		// applied), so a build-tag or GOOS-split New() appears as several
		// here. That is rejected by design: an extension declaration is
		// platform-independent inert data, and picking one of several
		// (unordered map iteration) would make the committed manifest
		// nondeterministic. Declare exactly one New().
		return nil, fmt.Errorf("extensions/%s: multiple New() constructors in the unit root — declare exactly one; an extension declaration is platform-independent, so a build-tag/GOOS-split New() is unsupported", u.Name)
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
// LITERAL: the declaration idiom requires New() to
// return a literal so the manifest derives without compiling — a computed
// value is a hard error naming the position, never a silent gap.
type unitReader struct {
	fset  *token.FileSet
	vocab map[string]string
	// verbs are the operations this unit's contract fragments declare, read
	// from the MERGED contract before the AST is walked. The reader needs them
	// to join behavior to declaration (joinToolsToContract) and to build the
	// manifest's risk tiers, which are contract-derived, not AST-derived.
	verbs []declaredVerb
	// jobs are the scheduled jobs this unit's jobs.yaml fragment declares,
	// read from the MERGED contract for the same reason verbs are: the join
	// between behavior and declaration, and the manifest's job risk tiers.
	jobs []extension.JobDeclaration
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
	tiers, err := toolRequests(r.verbs)
	if err != nil {
		return unitManifest{}, err
	}
	jobTiers, err := jobRequests(r.jobs)
	if err != nil {
		return unitManifest{}, err
	}
	m := unitManifest{Schema: 1, RiskTiers: append(tiers, jobTiers...)}
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
	sort.Slice(m.RiskTiers, func(i, j int) bool { return m.RiskTiers[i].ID < m.RiskTiers[j].ID })
	sort.Slice(m.Secrets, func(i, j int) bool {
		if m.Secrets[i].Key != m.Secrets[j].Key {
			return m.Secrets[i].Key < m.Secrets[j].Key
		}
		return m.Secrets[i].Scope < m.Secrets[j].Scope
	})
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
		// The manifest's risk tiers are already set, from the merged contract.
		// What the Go slice contributes is the join: behavior for a verb the
		// contract does not declare is a defect, reported at its own line.
		var tools []declaredTool
		tools, err = r.readTools(kv.Value, file)
		if err == nil {
			err = r.joinToolsToContract(tools, r.verbs)
		}
	case "Jobs":
		// Symmetric with Tools: the manifest's job risk tiers are already set,
		// from the merged contract. What the Go slice contributes is the join —
		// behavior for a job the contract does not declare is a defect,
		// reported at its own line.
		var declared []declaredTool
		declared, err = r.readJobs(kv.Value, file)
		if err == nil {
			err = r.joinJobsToContract(declared, r.jobs)
		}
	case "Jurisdictions":
		// Recognized and deliberately skipped: a jurisdiction pack is
		// passive policy the core consults, never a governed operation an
		// operator resolves, so it contributes no manifest entry.
	case "Migrations":
		// Recognized and deliberately skipped for the same reason, and the
		// layer is not unread: collectUnitTables validates the SQL this field
		// embeds, and extmigrategate applies it as the restricted ext_<name>
		// role. What an operator resolves are risk tiers and secret requests;
		// a schema is neither.
	case "Secrets":
		var secrets []secretsRequest
		secrets, err = r.readSecrets(kv.Value, file)
		if err == nil {
			m.Secrets = append(m.Secrets, secrets...)
		}
	default:
		// Fail closed: a field this generator does not recognize could be a
		// future governed capability, and a manifest that silently omitted
		// it would hide a request from the operator.
		err = r.errAt(kv, "Extension field %s is not derivable by this generator — teach the manifest reader before declaring it", key.Name)
	}
	return err
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
	return fmt.Errorf("%s: %s — manifest derivation reads declarations statically; declare literal values",
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
	// A concatenation of literals is still a literal: the value is fixed at the
	// declaration and this reader can compute it without evaluating anything.
	// Prose that will not fit on one line — a tool's description — has no other
	// way to be written, and refusing it would push a unit author into a single
	// unreadable line to satisfy a generator.
	if bin, ok := expr.(*ast.BinaryExpr); ok && bin.Op == token.ADD {
		left, err := r.stringLit(bin.X, field)
		if err != nil {
			return "", err
		}
		right, err := r.stringLit(bin.Y, field)
		if err != nil {
			return "", err
		}
		return left + right, nil
	}
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", r.errAt(expr, "%s must be a string literal (or literals joined by +)", field)
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

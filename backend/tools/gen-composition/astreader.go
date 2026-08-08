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

func deriveUnitManifest(u extensionUnit, vocab map[string]string, verbs []declaredVerb) ([]byte, error) {
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
	r := &unitReader{fset: fset, vocab: vocab, verbs: verbs}
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
	m := unitManifest{Schema: 1, RiskTiers: tiers}
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

// readTools reads the unit's Tools slice. After the narrowing, a Tools entry
// carries only {Name, Handle} — the verb, and the behavior. Nothing here
// reaches the manifest: what an operator resolves is DECLARED in the unit's
// contract fragment and derived from the merged contract (extverbs.go). What
// this read is for is the join between the two halves, and its one refusal:
// behavior for a verb no contract operation declares.
func (r *unitReader) readTools(expr ast.Expr, file *ast.File) ([]declaredTool, error) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, r.errAt(expr, "Tools must be a slice literal")
	}
	ext := importAlias(file, extensionPkgPath)
	tools := make([]declaredTool, 0, len(lit.Elts))
	seen := map[string]bool{}
	for _, elt := range lit.Elts {
		t, err := r.readTool(elt, ext)
		if err != nil {
			return nil, err
		}
		if seen[t.name] {
			return nil, r.errAt(elt, "tool %s declared twice", t.name)
		}
		seen[t.name] = true
		tools = append(tools, t)
	}
	return tools, nil
}

func (r *unitReader) readTool(elt ast.Expr, ext string) (declaredTool, error) {
	lit, ok := elt.(*ast.CompositeLit)
	if !ok || (lit.Type != nil && !isSelector(lit.Type, ext, "Tool")) {
		return declaredTool{}, r.errAt(elt, "a Tools entry must be an extension.Tool literal")
	}
	var d declaredTool
	d.at = lit
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return declaredTool{}, r.errAt(e, "Tool fields must be keyed")
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return declaredTool{}, r.errAt(kv.Key, "Tool fields must be keyed by name")
		}
		var err error
		switch key.Name {
		case "Name":
			d.name, err = r.stringLit(kv.Value, "Tool.Name")
		case "Handle":
			// Behavior is not a static declaration and never reaches the
			// manifest. Whether one is SERVED is read anyway, because that is
			// what separates a live capability from a verb the unit publishes
			// and does not run. A declared `Handle: nil` is inert — it is how
			// the seam spells "declare it, serve nothing", and the runtime
			// adapter skips exactly that — so the field's presence is not the
			// question; its value being non-nil is. See isStaticallyNil for the
			// spellings that count as nil, and readHandle for why a non-nil
			// value must be a bare identifier.
			d.served, err = r.readHandle(kv.Value, ext)
		default:
			// Fail closed, and this arm is what keeps the narrowing HONEST: a
			// unit still declaring Tier, Description or InputSchema in Go is
			// told, at that line, that the field moved to its contract
			// fragment — rather than having it silently ignored while the
			// contract's value governs.
			err = r.errAt(kv, "Tool field %s is not derivable by this generator — a Tool declares {Name, Handle}; tier, scope, version, title, description and the I/O schemas are declared in the unit's %s/<contract>.yaml fragment and read from the merged contract", key.Name, apiLayer)
		}
		if err != nil {
			return declaredTool{}, err
		}
	}
	// Grammar only. Every other rule about a tool is a rule about its
	// DECLARATION, and the declaration is the contract's (extension.Verb).
	if err := (extension.Tool{Name: d.name}).Validate(); err != nil {
		return declaredTool{}, r.errPos(lit, "%v", err)
	}
	return d, nil
}

// readHandle reports whether a Tools entry serves a handler, refusing any
// non-nil spelling that is not a bare identifier.
//
// A declared handler must name a package-level function the runtime adapter
// can call directly. The AST cannot tell an inert `pkg.Fn` (a value from
// some other package, still just a name) apart from `recv.Method` — a
// method value that closes over a receiver and can reopen liveness the
// declaration is supposed to foreclose — without type information the
// generator does not have. Nor can it evaluate `mkHandler()` without
// running code, which is the one thing a static reader must never do.
// Identifier-only is therefore the sole rule that keeps "a declaration is
// inert data" checkable: it accepts the one spelling the reader can judge
// by shape alone, and refuses every other one on the same conservative
// footing as isStaticallyNil below.
func (r *unitReader) readHandle(expr ast.Expr, ext string) (served bool, err error) {
	if isStaticallyNil(expr, ext) {
		return false, nil
	}
	if _, ok := expr.(*ast.Ident); ok {
		return true, nil
	}
	return false, r.errAt(expr, "Tool.Handle must be a plain identifier naming the handler function, or one of the documented inert nil spellings (nil, extension.ToolHandler(nil), (nil))")
}

// isStaticallyNil reports whether an expression is nil at the declaration —
// which is how a Tools entry says "declare it, serve nothing", and what the
// runtime adapter skips on.
//
// Two spellings, because both reach the adapter as the same nil function value:
// the bare `nil`, and a conversion of it through the PUBLISHED extension.
// ToolHandler type (`extension.ToolHandler(nil)`), which a unit author writes
// when the surrounding literal needs the type to be obvious.
//
// The CallExpr arm checks the callee, not just the argument count, and that
// check is load-bearing, not decorative: a syntactic conversion and an
// ordinary one-argument call are indistinguishable by shape alone (`T(x)` and
// `f(x)` parse identically), so accepting any one-argument call whose sole
// argument is nil — without checking what is being called — would read
// `mustDial(nil)` as inert too. That is not the safe failure mode: it does
// not merely admit one more spelling of "serve nothing", it exempts a call
// that already ran, at declaration time, from BOTH gates the tool has —
// readHandle's identifier-only rule for anything else, and readTool's
// served-tool-needs-a-Description refusal, which never even asks the
// question for something this function has already called inert. Requiring
// the callee to be exactly the published extension.ToolHandler conversion
// keeps this arm what its name promises: a real, code-free type conversion
// of the constant nil, not a call to arbitrary unit-authored code. Anything
// else — a function name, a literal, any other call — is refused outright by
// the Ident check in readHandle above; this reader never falls back to
// treating an unrecognized shape as inert.
func isStaticallyNil(expr ast.Expr, ext string) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil"
	case *ast.CallExpr:
		return len(e.Args) == 1 && isSelector(e.Fun, ext, "ToolHandler") && isStaticallyNil(e.Args[0], ext)
	case *ast.ParenExpr:
		return isStaticallyNil(e.X, ext)
	}
	return false
}

// declaredTool is one Tools entry as the source states it: the verb, whether
// it serves a handler, and the position, so the join against the contract can
// report a mismatch at the line that caused it.
type declaredTool struct {
	name   string
	served bool
	at     ast.Node
}

// joinToolsToContract reconciles the unit's Go behavior with the operations its
// contract fragment declares, and refuses the one direction that is a defect.
//
// Behavior for a verb no operation declares is refused: it is a capability with
// no published surface — nothing lists it, nothing documents it, no manifest
// entry asks an operator about it, and yet it would be registered into the same
// registry the core tools ride. The reverse is NOT a defect: a declared verb
// with no Go behavior is a contract-only governed request (fixtures'
// crm-hello), which the manifest records and the boot serves nothing for.
func (r *unitReader) joinToolsToContract(tools []declaredTool, verbs []declaredVerb) error {
	declared := make(map[string]bool, len(verbs))
	for _, d := range verbs {
		declared[d.verb.Tool] = true
	}
	for _, t := range tools {
		if declared[t.name] {
			continue
		}
		return r.errPos(t.at, "tool %q has behavior here but no operation in this unit's %s/ fragments declares it — declare it in the contract (the merged contract is what publishes a verb), or delete the entry", t.name, apiLayer)
	}
	return nil
}

// toolRequests turns the unit's contract-declared operations into its manifest
// risk-tier entries. A tool requires one scope; the descriptor carries it as
// its (single-element) scope set, the general shape shared across governed
// kinds.
func toolRequests(verbs []declaredVerb) ([]riskTierRequest, error) {
	out := make([]riskTierRequest, 0, len(verbs))
	for _, d := range verbs {
		c := riskTierRequest{
			ID:           "tool/" + d.verb.Tool,
			Unit:         string(d.verb.Unit),
			Kind:         kindAgentTool,
			Contract:     d.verb.Contract,
			Operation:    opAgentToolInvoke,
			OperationID:  d.verb.OperationID,
			Route:        d.verb.Route,
			Method:       d.verb.Method,
			Scopes:       []string{string(d.verb.RequestedScope)},
			Tier:         string(d.verb.Tier),
			FragmentHash: d.fragmentHash,
		}
		digest, err := descriptorDigest(c)
		if err != nil {
			return nil, err
		}
		c.Digest = digest
		out = append(out, c)
	}
	return out, nil
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

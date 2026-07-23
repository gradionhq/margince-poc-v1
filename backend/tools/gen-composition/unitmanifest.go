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
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// unitManifestFile is the per-unit generated manifest (ADR-0069 §5): what
// the unit declares that an OPERATOR must resolve, derived from the
// declaration and written next to the unit, so preflight, permission
// review, and the boot inventory read it WITHOUT compiling or executing
// the code. The drift gate (-verify), not a signature, binds it to the
// source for in-repo units.
const unitManifestFile = "manifest.generated.json"

// unitManifest is one extension's manifest.generated.json: identity plus
// the AUTONOMY TIERS it requests — every operation the extension adds
// that runs at a 🟢/🟡 tier or asks for a scope, the things §7 makes an
// operator resolve. Passive policy an extension merely supplies (a
// jurisdiction pack the core consults, never invokes — no operation, no
// tier) requests no autonomy and never appears here; the list stays
// empty until a unit declares an operation that needs approval.
type unitManifest struct {
	Schema        int                   `json:"schema"`
	Name          string                `json:"name"`
	Version       string                `json:"version"`
	AutonomyTiers []autonomyTierRequest `json:"autonomy_tiers"`
}

// autonomyTierRequest is one governed operation and the autonomy tier it
// requests, carrying its ADR-0069 §5 security descriptor: id, operation,
// requested scopes and requested tier are what §7 resolutions bind to,
// through Digest over exactly those four. No tier-bearing kind is
// derivable yet — the governed kinds (agent tools with tiers, /x/
// endpoints with scopes, ext tables) arrive in later slices, each
// teaching the reader to emit and digest its own requests.
type autonomyTierRequest struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	Scopes    []string `json:"scopes"`
	Tier      string   `json:"tier"`
	Digest    string   `json:"digest"`
}

// generateUnitManifests derives and writes every enabled unit's manifest.
// The write is skipped when the content is current, so the lane-frequent
// `make composition` never churns source-tree mtimes.
func generateUnitManifests(units []extensionUnit) error {
	for _, u := range units {
		encoded, err := deriveUnitManifest(u)
		if err != nil {
			return err
		}
		path := filepath.Join(u.Dir, unitManifestFile)
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, encoded) {
			continue
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { // #nosec G306 -- generated manifest, not a secret
			return err
		}
	}
	return nil
}

// verifyUnitManifests re-derives every unit's manifest and requires the
// file next to the unit to be byte-identical — a hand edit, a stale
// derivation, or a foreign encoder fails here even when the semantic
// content agrees (the composition.json input row only pins the digest;
// THIS is the gate that ties the digest back to the declaration).
func verifyUnitManifests(units []extensionUnit) error {
	for _, u := range units {
		encoded, err := deriveUnitManifest(u)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(filepath.Join(u.Dir, unitManifestFile))
		if err != nil {
			return fmt.Errorf("extensions/%s/%s: %w — run 'make gen'", u.Name, unitManifestFile, err)
		}
		if !bytes.Equal(onDisk, encoded) {
			return fmt.Errorf("extensions/%s/%s differs from its derivation — run 'make gen'", u.Name, unitManifestFile)
		}
	}
	return nil
}

const extensionPkgPath = "github.com/gradionhq/margince/backend/pkg/extension"

// deriveUnitManifest reads one unit's declaration statically and emits its
// manifest. It parses the unit's New() constructor from the AST — never
// compiling or running it — so the reader accepts only LITERAL values; a
// computed one is a positioned error, never a silent gap in what review
// sees.
func deriveUnitManifest(u extensionUnit) ([]byte, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, u.Dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("extensions/%s: %w", u.Name, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("extensions/%s: the unit root must hold exactly one package, found %d", u.Name, len(pkgs))
	}
	r := &unitReader{unit: u.Name, fset: fset}
	var newFn *ast.FuncDecl
	var newFile *ast.File
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "New" {
					newFn, newFile = fn, file
				}
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

// unitReader walks one unit's declaration AST. Everything it reads is a
// LITERAL: the declaration idiom (ADR-0069 §4/§5) requires New() to
// return a literal so the manifest derives without compiling — a computed
// value is a hard error naming the position, never a silent gap.
type unitReader struct {
	unit string
	fset *token.FileSet
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
			// Recognized and deliberately skipped: a jurisdiction pack is
			// passive policy the core consults, never a governed capability
			// an operator resolves (§7), so it contributes no manifest entry.
		default:
			// Fail closed: a field this generator does not recognize could
			// be a future governed capability, and a manifest that silently
			// omitted it would hide a request from the operator.
			err = r.errAt(kv, "Extension field %s is not derivable by this generator — teach the manifest reader before declaring it", key.Name)
		}
		if err != nil {
			return unitManifest{}, err
		}
	}
	if m.Name == "" || m.Version == "" {
		return unitManifest{}, r.errAt(lit, "the Extension literal must declare Name and Version")
	}
	return m, nil
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

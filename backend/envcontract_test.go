// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Environment-variable contract fitness functions. Four obligations, all
// derived from the tree rather than a maintained list:
//
//  1. every MARGINCE_* var the Go code reads is named in
//     docs/reference/configuration.md — the table of record for the binaries;
//  2. every MARGINCE_* var .env.example names is still part of the product;
//  3. every MARGINCE_* var configuration.md names is still part of the product;
//  4. every MARGINCE_* var a deploy entrypoint hard-requires is named in
//     .env.example — that file is the annotated template docs/deployment.md
//     hands an operator, so a var the container refuses to boot without must
//     not be discoverable only by reading the entrypoint script.
//
// Unasserted, the three files drift apart: the template names credentials for a
// process role that no longer exists, a secret an operator must provision goes
// undocumented, and the reference doc keeps a row for a variable nothing reads.
// None of those is visible at the point of failure — an operator supplies a
// value and observes no effect, or looks for a variable and does not find it.
//
// What these gates do NOT cover, stated rather than implied:
//
//   - Obligation 1 covers Go readers, in the licensed trees only. A var read
//     solely by deploy shell or a workflow is documented in docs/deployment.md
//     by hand, and cli/craft is a module of its own that no sweep here walks.
//     Obligations 2 and 3 do count those non-Go readers, because there the
//     question is merely whether a name is still real.
//   - MARGINCE_-prefixed names only. The BYOK keys (GEMINI_API_KEY,
//     OPENAI_API_KEY, ANTHROPIC_API_KEY, OPENAI_COMPATIBLE_API_KEY) carry
//     provider-conventional names and are ungated. Widening the pattern to any
//     env-shaped literal would match unrelated constants and trade a sharp
//     gate for a noisy one.
//   - Whole double-quoted literals only. A name assembled at run time
//     ("MARGINCE_" + provider + "_KEY"), or written as a backquoted raw string,
//     is invisible to every obligation here, so adding one has to be a
//     deliberate act rather than a silent gap.
//     internal/platform/config/unknown.go is the one deliberate case: it
//     assembles the names of variables it deliberately does NOT read, so a
//     whole literal there would demand a documentation row for a variable no
//     process consults. Its own comment records the reason. Every backquoted
//     occurrence remains prose in a comment or an error message.
//   - Presence, not truth. A var being named in a document does not make the
//     prose around it accurate, and .env.example now carries denser
//     behavioural claims than the reference doc does. These gates stop a var
//     disappearing; they cannot stop a description going stale.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// envVarName matches one MARGINCE_* name in prose — the reference doc and the
// template, where names appear unquoted. Applied to those files it is why
// .env.example may not write a `MARGINCE_FOO_*` glob: the glob yields the name
// `MARGINCE_FOO_`, which nothing reads.
var envVarName = regexp.MustCompile(`MARGINCE_[A-Z0-9_]+`)

// quotedEnvVarName matches one MARGINCE_* name as a Go string literal. Keying
// on the literal rather than on os.Getenv is what makes the Go sweep complete:
// the vars are also read through four wrapper helpers in cmd/api and cmd/worker
// (envOr, envIntOr, envDuration, envDurationOr). Keying on unquoted text
// instead would harvest globs out of comments and invent vars that do not exist.
var quotedEnvVarName = regexp.MustCompile(`"(MARGINCE_[A-Z0-9_]+)"`)

const (
	configurationDoc = "../docs/reference/configuration.md"
	envExample       = "../.env.example"
)

// deploySurfaceRoots are the non-Go trees that configure a deployment: the
// entrypoints and helper scripts, the compose/CI definitions, and the images.
// A variable read only here is as real as one Go reads, so obligations 2 and 3
// must accept it. MARGINCE_ADMIN_PASSWORD is the current example: the entrypoint
// turns it into the file reference margince.yaml names — OPS-CFG-3 keeps secret
// VALUES out of every config layer the product reads — so a Go-only definition of
// "live" would report it as dead in a file whose purpose is to offer it to an
// operator.
var deploySurfaceRoots = []string{"../scripts", "../infra", "../.github/workflows"}

// walkTextFiles reads every file under root and hands each to visit. The trees
// swept here are small and text-only; a read error fails the test rather than
// narrowing the sweep in silence.
func walkTextFiles(t *testing.T, root string, visit func(path, text string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A unit's frontend layer is a pnpm workspace package, so extensions/
		// now contains an installed dependency tree — thousands of files this
		// sweep has no business reading, and symlinked DIRECTORIES that are not
		// IsDir() and so reach the ReadFile below as "is a directory".
		if d.IsDir() && d.Name() == "node_modules" {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		// Belt and braces for the same reason: a symlink is not a regular file,
		// and this sweep reads text a human wrote.
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path) // #nosec G304 G122 -- path comes from walking a fixed root inside the trusted source tree
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(path), string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

// envVarsReadByGoCode maps each MARGINCE_* name a Go string literal spells to
// the first file spelling it. The trees are the ones the license sweep covers
// (licensedTrees, license_test.go): extensions/ and fixtures/ are separate
// modules a `./...` from here never reaches, and a var a unit reads is as real
// as one the backend reads. Sharing that list means a new tree enrolls in both
// gates at once instead of silently in only one.
func envVarsReadByGoCode(t *testing.T) map[string]string {
	t.Helper()
	sites := map[string]string{}
	for _, tree := range licensedTrees {
		walkTextFiles(t, tree.root, func(path, text string) {
			if !strings.HasSuffix(path, ".go") || isGenerated(path, text) {
				return
			}
			for _, m := range quotedEnvVarName.FindAllStringSubmatch(text, -1) {
				if _, seen := sites[m[1]]; !seen {
					sites[m[1]] = path
				}
			}
		})
	}
	if len(sites) == 0 {
		t.Fatal("no MARGINCE_* env var found in any Go tree — a sweep that scans nothing passes exactly like a clean one")
	}
	return sites
}

// liveEnvVars is every MARGINCE_* name the product still mentions anywhere it
// configures itself: Go literals plus the deploy surface. The set is
// intentionally over-inclusive — a script that sets a variable for a child
// process is counted the same as one that reads it — because it is only ever
// used to permit a name to appear in a document. Obligation 1, which requires
// documentation, stays Go-only for the opposite reason: there the same
// imprecision would demand rows for variables no operator configures.
func liveEnvVars(t *testing.T) map[string]string {
	t.Helper()
	live := envVarsReadByGoCode(t)
	for _, root := range deploySurfaceRoots {
		walkTextFiles(t, root, func(path, text string) {
			for _, name := range envVarName.FindAllString(text, -1) {
				if _, seen := live[name]; !seen {
					live[name] = path
				}
			}
		})
	}
	images, err := filepath.Glob("../Dockerfile*")
	if err != nil {
		t.Fatalf("globbing Dockerfiles: %v", err)
	}
	for _, path := range images {
		b, err := os.ReadFile(path) // #nosec G304 -- path comes from a fixed glob in the trusted source tree
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, name := range envVarName.FindAllString(string(b), -1) {
			if _, seen := live[name]; !seen {
				live[name] = filepath.ToSlash(path)
			}
		}
	}
	return live
}

// namesIn returns the set of MARGINCE_* names a document spells out. Matching
// whole names rather than searching for substrings is what makes the gates
// exact in both directions: a doc mentioning only MARGINCE_AICERT_MODEL must
// not be read as covering the separate MARGINCE_AICERT, and an abbreviation
// like `MARGINCE_GMAIL_CLIENT_ID` / `…_SECRET` covers only the name it actually
// writes out. A variable name a reader cannot grep for is not documented.
func namesIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- fixed path in the trusted source tree
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	names := map[string]bool{}
	for _, name := range envVarName.FindAllString(string(b), -1) {
		names[name] = true
	}
	if len(names) == 0 {
		t.Fatalf("%s names no MARGINCE_* var at all — a file that documents nothing cannot be the table of record", path)
	}
	return names
}

// TestEveryEnvVarIsDocumented: shipping a var the reference doc has never heard
// of leaves an operator guessing, and a secret nobody wrote down cannot be
// provisioned at all.
func TestEveryEnvVarIsDocumented(t *testing.T) {
	documented := namesIn(t, configurationDoc)
	sites := envVarsReadByGoCode(t)

	var undocumented []string
	for _, name := range slices.Sorted(maps.Keys(sites)) {
		if !documented[name] {
			undocumented = append(undocumented, name+" (read in "+sites[name]+")")
		}
	}
	if len(undocumented) > 0 {
		t.Errorf("%d env var(s) read by Go code but not named in %s — add a row there, spelling the name out in full (an abbreviated suffix like `…_SECRET` does not count):\n\t%s",
			len(undocumented), configurationDoc, strings.Join(undocumented, "\n\t"))
	}
}

// TestEnvExampleNamesOnlyLiveVars: an entry for a variable nothing reads is
// worse than no entry at all, because it invites an operator to supply a value
// that cannot take effect.
func TestEnvExampleNamesOnlyLiveVars(t *testing.T) {
	assertNamesOnlyLiveVars(t, envExample)
}

// TestConfigurationDocNamesOnlyLiveVars holds the table of record to the same
// bar as the template it now absorbs. Without it the more authoritative file is
// the one free to keep a row for a var that no longer exists.
func TestConfigurationDocNamesOnlyLiveVars(t *testing.T) {
	assertNamesOnlyLiveVars(t, configurationDoc)
}

// entrypointRequired matches the parameter-expansion forms that abort a script
// on an unset variable: `${FOO:?msg}` and the colonless `${FOO?msg}`, which
// differ only in whether an empty value also aborts. The `:-default` form is
// deliberately not matched — a variable with a fallback is not one an operator
// must supply, and requiring those would admit most of the deploy surface and
// leave the label meaningless.
//
// Only these two forms are detected. A script can also make a variable
// mandatory with an explicit guard (`[ -z "$FOO" ] && exit 1`) or by relying on
// `set -u`, and obligation 4 would not see either. Neither appears in
// scripts/deploy today; the limit is recorded so the gate is not read as
// proving more than it does.
var entrypointRequired = regexp.MustCompile(`\$\{(MARGINCE_[A-Z0-9_]+):?\?`)

// TestEntrypointRequiredVarsAreInTheEnvExample: a var the container refuses to
// boot without belongs in the file an operator is handed, not only in the
// script that rejects them. docs/deployment.md names .env.example as that
// file, which is what makes this an obligation rather than a nicety.
func TestEntrypointRequiredVarsAreInTheEnvExample(t *testing.T) {
	offered := namesIn(t, envExample)

	required := map[string]string{}
	walkTextFiles(t, "../scripts/deploy", func(path, text string) {
		for _, m := range entrypointRequired.FindAllStringSubmatch(text, -1) {
			if _, seen := required[m[1]]; !seen {
				required[m[1]] = path
			}
		}
	})
	if len(required) == 0 {
		t.Fatal("no `${MARGINCE_…:?}` requirement found under ../scripts/deploy — a sweep that scans nothing passes exactly like a clean one")
	}

	var missing []string
	for _, name := range slices.Sorted(maps.Keys(required)) {
		if !offered[name] {
			missing = append(missing, name+" (required by "+required[name]+")")
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d var(s) a deploy entrypoint refuses to boot without, absent from %s — add them, commented, with the value shape an operator must replace:\n\t%s",
			len(missing), envExample, strings.Join(missing, "\n\t"))
	}
}

func assertNamesOnlyLiveVars(t *testing.T, path string) {
	t.Helper()
	live := liveEnvVars(t)

	var dead []string
	for name := range namesIn(t, path) {
		if _, ok := live[name]; !ok {
			dead = append(dead, name)
		}
	}
	slices.Sort(dead)
	if len(dead) > 0 {
		t.Errorf("%d var(s) named in %s that nothing in the product reads — delete them. A `MARGINCE_FOO_*` glob counts as the name `MARGINCE_FOO_`, which nothing reads: spell such vars out instead of globbing them:\n\t%s",
			len(dead), path, strings.Join(dead, "\n\t"))
	}
}

// ---------------------------------------------------------------------------
// Obligation 5: a declared default and a documented default say the same thing.
//
// The four obligations above gate NAMES. Nothing gated VALUES, and the two
// descriptions of a default drift apart in the direction that matters: the code
// applies a fallback, the schema does not declare it, and the reference doc
// documents one anyway. An operator reads the doc, sees a default, and supplies
// nothing — while what the binary actually does is decided somewhere the doc
// never described.
//
// Both sides are already in the tree, which is what makes this derivable rather
// than a list: config.Item carries Default, and configuration.md's tables carry
// a Default column. This asserts they agree, in both directions — a declared
// default the doc does not show, and a documented default nothing declares.

// configItemDefaults is one config.Item declaration's documented name and the
// default it declares. An absent Default is the empty string, which is the same
// thing the doc writes as an em dash.
type configItemDefault struct {
	name    string
	value   string
	pos     string
	present bool
}

// docDefaultRow matches one reference-doc table row: the env name in backticks,
// then the second cell.
var docDefaultRow = regexp.MustCompile(`(?m)^\|\s*` + "`" + `(MARGINCE_[A-Z0-9_]+)` + "`" + `\s*\|\s*([^|]*?)\s*\|`)

// docDefaultTable matches a table header whose SECOND column is the default.
// configuration.md also carries tables whose second column is something else
// entirely — which role reads the var, or which suite needs it — and reading
// those cells as defaults would report prose where no default was ever claimed.
var docDefaultTable = regexp.MustCompile(`(?mi)^\|\s*Env\s*\|\s*Default\s*\|`)

// docDefaultLiteral pulls the value out of a default cell, which is written in
// backticks when there is one.
var docDefaultLiteral = regexp.MustCompile("^`([^`]*)`$")

func TestEveryDeclaredDefaultMatchesTheDocumentedOne(t *testing.T) {
	declared := declaredConfigDefaults(t)
	documented := documentedDefaults(t)
	if len(declared) == 0 {
		t.Fatal("no config.Item declaration found — a sweep that scans nothing passes exactly like a clean one")
	}

	for _, item := range declared {
		docValue, inDoc := documented[item.name]
		if !inDoc {
			continue // obligation 1 already requires the name to be documented
		}
		switch {
		case item.present && docValue != item.value:
			t.Errorf("%s: %s declares Default %q, %s documents %q — an operator supplying nothing gets the first and reads the second",
				item.pos, item.name, item.value, configurationDoc, docValue)
		case !item.present && docValue != "":
			t.Errorf("%s: %s declares no Default, %s documents %q — either the code applies that fallback and the schema must declare it, or it does not and the doc is telling operators about a value nothing will supply",
				item.pos, item.name, configurationDoc, docValue)
		}
	}
}

// declaredConfigDefaults finds every config.Item composite literal in the
// hand-written trees and reads the env name and default out of it.
//
// Name is usually a constant rather than a literal, so package-level MARGINCE_*
// constants are resolved first. A gate that only understood literals would see
// almost nothing here and pass for that reason.
func declaredConfigDefaults(t *testing.T) []configItemDefault {
	t.Helper()
	consts := envNameConstants(t)
	var out []configItemDefault
	fset := token.NewFileSet()
	for _, tree := range licensedTrees {
		walkTextFiles(t, tree.root, func(path, text string) {
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || isGenerated(path, text) {
				return
			}
			file, err := parser.ParseFile(fset, path, text, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, isLit := n.(*ast.CompositeLit)
				if !isLit {
					return true
				}
				// The declarations are elements of a []config.Item slice literal,
				// so their own Type node is NIL — Go elides it. A walk keyed on
				// each element's type finds nothing at all here, which passes for
				// exactly the same reason a clean tree does.
				for _, element := range elidedConfigItems(lit) {
					if item, ok := readConfigItem(element, consts); ok {
						item.pos = fset.Position(element.Pos()).String()
						out = append(out, item)
					}
				}
				return true
			})
		})
	}
	return out
}

// elidedConfigItems answers the config.Item literals a composite literal holds:
// itself when it is written out as config.Item{…}, or its elements when it is
// the []config.Item slice the declarations actually live in.
func elidedConfigItems(lit *ast.CompositeLit) []*ast.CompositeLit {
	if namesConfigItem(lit.Type) {
		return []*ast.CompositeLit{lit}
	}
	array, isArray := lit.Type.(*ast.ArrayType)
	if !isArray || !namesConfigItem(array.Elt) {
		return nil
	}
	var out []*ast.CompositeLit
	for _, element := range lit.Elts {
		if inner, isLit := element.(*ast.CompositeLit); isLit {
			out = append(out, inner)
		}
	}
	return out
}

// namesConfigItem reports whether an expression names config.Item, by either
// spelling — qualified from outside the package, bare from within it.
func namesConfigItem(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name == "Item"
	case *ast.Ident:
		return typed.Name == "Item"
	}
	return false
}

// readConfigItem pulls Name and Default out of one literal. A literal whose Name
// does not resolve to a MARGINCE_* value is not a declaration this gate governs.
func readConfigItem(lit *ast.CompositeLit, consts map[string]string) (configItemDefault, bool) {
	var item configItemDefault
	for _, element := range lit.Elts {
		field, isField := element.(*ast.KeyValueExpr)
		if !isField {
			continue
		}
		key, isIdent := field.Key.(*ast.Ident)
		if !isIdent {
			continue
		}
		switch key.Name {
		case "Name":
			item.name = resolveStringExpr(field.Value, consts)
		case "Default":
			item.value = resolveStringExpr(field.Value, consts)
			item.present = true
		}
	}
	return item, strings.HasPrefix(item.name, "MARGINCE_")
}

// resolveStringExpr answers a string literal's value, or a constant's, or "".
func resolveStringExpr(expr ast.Expr, consts map[string]string) string {
	switch typed := expr.(type) {
	case *ast.BasicLit:
		if typed.Kind != token.STRING {
			return ""
		}
		value, err := strconv.Unquote(typed.Value)
		if err != nil {
			return ""
		}
		return value
	case *ast.Ident:
		return consts[typed.Name]
	case *ast.SelectorExpr:
		// A name borrowed from another package, e.g. runtimeenv.EnvVar. The
		// constant sweep is tree-wide, so the terminal identifier resolves.
		return consts[typed.Sel.Name]
	}
	return ""
}

// envNameConstants maps every package-level identifier bound to a MARGINCE_*
// string to that string, tree-wide.
//
// Keyed on the bare identifier so a qualified reference resolves too. Two
// constants sharing a name and disagreeing about the value would make that
// unsound, so the sweep refuses rather than picking one.
func envNameConstants(t *testing.T) map[string]string {
	t.Helper()
	consts := map[string]string{}
	fset := token.NewFileSet()
	for _, tree := range licensedTrees {
		walkTextFiles(t, tree.root, func(path, text string) {
			if !strings.HasSuffix(path, ".go") || isGenerated(path, text) {
				return
			}
			file, err := parser.ParseFile(fset, path, text, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			for _, decl := range file.Decls {
				gen, isGen := decl.(*ast.GenDecl)
				if !isGen || gen.Tok != token.CONST {
					continue
				}
				collectEnvConstants(t, gen, consts, fset)
			}
		})
	}
	return consts
}

// collectEnvConstants records one const block's MARGINCE_* bindings.
func collectEnvConstants(t *testing.T, gen *ast.GenDecl, consts map[string]string, fset *token.FileSet) {
	t.Helper()
	for _, spec := range gen.Specs {
		value, isValue := spec.(*ast.ValueSpec)
		if !isValue {
			continue
		}
		for i, name := range value.Names {
			if i >= len(value.Values) {
				continue
			}
			resolved := resolveStringExpr(value.Values[i], nil)
			if !strings.HasPrefix(resolved, "MARGINCE_") {
				continue
			}
			if seen, dup := consts[name.Name]; dup && seen != resolved {
				t.Fatalf("%s: two constants named %s bind different env vars (%q and %q); this sweep resolves a name to one value and cannot with both",
					fset.Position(name.Pos()), name.Name, seen, resolved)
			}
			consts[name.Name] = resolved
		}
	}
}

// documentedDefaults reads the Default column out of configuration.md's tables.
// An em dash means the doc is asserting there is no default, which is the same
// claim as an absent Default field.
func documentedDefaults(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(configurationDoc) // #nosec G304 -- fixed path in the trusted source tree
	if err != nil {
		t.Fatalf("reading %s: %v", configurationDoc, err)
	}
	out := map[string]string{}
	for _, section := range defaultBearingTables(string(b)) {
		for _, row := range docDefaultRow.FindAllStringSubmatch(section, -1) {
			cell := strings.TrimSpace(row[2])
			if cell == "—" || cell == "-" || cell == "" {
				out[row[1]] = ""
				continue
			}
			if literal := docDefaultLiteral.FindStringSubmatch(cell); literal != nil {
				out[row[1]] = literal[1]
				continue
			}
			// Prose in a Default column is not a value this gate can compare, and
			// silently treating it as "no default" would make the row invisible.
			t.Errorf("%s: the Default cell for %s reads %q — a default is a literal in backticks or an em dash, so that a gate and an operator read the same thing",
				configurationDoc, row[1], cell)
		}
	}
	return out
}

// defaultBearingTables cuts the document into the table bodies whose second
// column is a default, so rows from the other tables are never read as one.
func defaultBearingTables(doc string) []string {
	var out []string
	for _, loc := range docDefaultTable.FindAllStringIndex(doc, -1) {
		body := doc[loc[1]:]
		if end := strings.Index(body, "\n\n"); end >= 0 {
			body = body[:end]
		}
		out = append(out, body)
	}
	return out
}

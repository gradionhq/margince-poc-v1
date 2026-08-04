// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The forbidigo rule that bans a direct River registration, held to River's
// own API rather than to a remembered list of its spellings.
//
// The closed type set makes an UNDECLARED kind unbuildable, but only along the
// sanctioned path; going straight to River escapes that, escapes jobs.Govern
// (so even a declared kind answers River's option methods for itself again),
// and never records a kind for jobs.MustBeTotal to refuse. The only thing
// holding that door is a regex in .golangci.yml — and a regex naming symbols
// is a hand-maintained list, which is precisely the artefact that goes stale
// on the upgrade nobody reads. It went stale once already, on a pattern
// anchored to AddWorker while AddWorkerArgs and AddWorkerSafely walked past.
//
// So the set is DERIVED, and structurally rather than by name: a function can
// only register a worker if it is handed the bundle to register into, so every
// exported function in package river whose first parameter is *Workers is an
// entry point, whatever it ends up being called. A fourth spelling in a future
// upgrade enrols itself.
//
// River's source is read out of the module cache via `go list -m`, not through
// go/packages: the gate lanes already run under a composed GOWORK workspace
// that `go list` resolves natively, and a parse of the declarations is all
// this needs — no type checking, no build of River itself.

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	riverModulePath   = "github.com/riverqueue/river"
	backendModulePath = "github.com/gradionhq/margince/backend"
)

// moduleDir asks the go command where a module's source lives. Derived rather
// than composed from a relative depth, so the test does not care which
// directory the lane runs it from.
func moduleDir(t *testing.T, modulePath string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", modulePath)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("locating module %s: %v\n%s", modulePath, err, stderr)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatalf("module %s resolved to no directory — it is not in this build's module graph, so nothing below could be derived", modulePath)
	}
	return dir
}

// riverRegistrationEntryPoints returns every exported function in package
// river that takes a *Workers as its first parameter: the complete set of ways
// this build could put a worker into River's registry.
func riverRegistrationEntryPoints(t *testing.T) []string {
	t.Helper()
	dir := moduleDir(t, riverModulePath)
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}

	var found []string
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		if file.Name.Name != "river" {
			continue
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && takesWorkersFirst(fn) {
				found = append(found, fn.Name.Name)
			}
		}
	}
	slices.Sort(found)

	if len(found) == 0 {
		t.Fatalf("found no registration entry points in %s — the module layout changed or the parse matched nothing, and a gate that checks an empty set is worse than no gate", dir)
	}
	// AddWorker is the one entry point this repository provably calls
	// (internal/compose/jobregistry.go), so a walk that misses it is broken
	// however many other names it happened to collect.
	if !slices.Contains(found, "AddWorker") {
		t.Fatalf("derived %v, which does not include AddWorker — the one entry point this build demonstrably uses. The derivation is not seeing what it thinks it is.", found)
	}
	return found
}

// takesWorkersFirst reports whether fn is an exported package-level function
// whose first parameter is a *Workers.
func takesWorkersFirst(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Workers"
}

// forbidRule is one entry of the repo-wide forbidigo blocklist.
type forbidRule struct {
	Pattern string `yaml:"pattern"`
	Pkg     string `yaml:"pkg"`
	Msg     string `yaml:"msg"`
}

// golangciConfig is the sliver of .golangci.yml this gate reads. The keys are
// golangci-lint's own, so they are spelled as it spells them.
type golangciConfig struct {
	Linters struct {
		Settings struct {
			Forbidigo struct {
				//nolint:tagliatelle // golangci-lint's key, not ours to case.
				AnalyzeTypes bool         `yaml:"analyze-types"`
				Forbid       []forbidRule `yaml:"forbid"`
			} `yaml:"forbidigo"`
		} `yaml:"settings"`
	} `yaml:"linters"`
}

// riverForbidRules returns the blocklist entries that govern package river,
// selected by their own pkg expression rather than by position, so reordering
// or adding unrelated rules cannot silently change what is checked.
func riverForbidRules(t *testing.T) []forbidRule {
	t.Helper()
	path := filepath.Join(moduleDir(t, backendModulePath), ".golangci.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var cfg golangciConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	forbidigo := cfg.Linters.Settings.Forbidigo

	// pkg is only consulted when forbidigo type-checks; without this the
	// selection below would be reading a field the linter ignores.
	if !forbidigo.AnalyzeTypes {
		t.Fatalf("%s: forbidigo.analyze-types is off, so its pkg expressions are inert and a rule could match a same-named symbol from any package", path)
	}

	var governing []forbidRule
	for _, rule := range forbidigo.Forbid {
		if rule.Pkg == "" {
			continue
		}
		pkg, err := regexp.Compile(rule.Pkg)
		if err != nil {
			t.Fatalf("%s: forbidigo rule %q has an unparsable pkg expression %q: %v", path, rule.Pattern, rule.Pkg, err)
		}
		if pkg.MatchString(riverModulePath) {
			governing = append(governing, rule)
		}
	}
	if len(governing) == 0 {
		t.Fatalf("%s declares no forbidigo rule whose pkg matches %s — the registration ban is gone, and every check below would pass by having nothing to check", path, riverModulePath)
	}
	return governing
}

// banCovers reports whether any governing rule forbids the given selector.
//
// "river.X" is the string forbidigo itself matches: with analyze-types on it
// resolves the selector to the package's own name, so an import alias is
// normalized away rather than being an escape.
func banCovers(t *testing.T, rules []forbidRule, expr string) bool {
	t.Helper()
	for _, rule := range rules {
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			t.Fatalf("forbidigo rule pattern %q does not compile: %v", rule.Pattern, err)
		}
		if pattern.MatchString(expr) {
			return true
		}
	}
	return false
}

// TestTheRegistrationBanCoversEveryRiverEntryPoint holds the forbidigo rule to
// River's API instead of to a remembered list. A River upgrade that adds a
// fourth way to register fails here, at the line of the upgrade, rather than
// on the first pull request that quietly uses it.
func TestTheRegistrationBanCoversEveryRiverEntryPoint(t *testing.T) {
	entryPoints := riverRegistrationEntryPoints(t)
	rules := riverForbidRules(t)

	for _, name := range entryPoints {
		expr := "river." + name
		if !banCovers(t, rules, expr) {
			t.Errorf("river.%s registers a worker but no forbidigo rule forbids it. A call to it compiles with an unconstrained type parameter, skips jobs.Govern, and records no kind for jobs.MustBeTotal — widen the pattern in backend/.golangci.yml to cover it.", name)
		}
	}
}

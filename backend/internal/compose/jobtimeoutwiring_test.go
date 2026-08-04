// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The timeout a worker actually gets is decided at its registration site, and
// only one of the two inputs is checkable by running code: Govern reads the
// declared Spec, but the value a {operator: …} kind is GIVEN is an argument the
// runner computes and passes. A test over the policy can prove the policy
// honours what it is handed; it cannot notice the runner handing it a zero.
// That is precisely the failure this contract exists to remove — a zero
// resolves to River's silent one-minute default — so the argument itself is
// gated here, read off the source rather than executed, because NewJobRunner
// needs a live pool and this claim is about what is written, not what runs.
//
// The expectation is DERIVED from the contract, never listed: whether a call
// site must compute its timeout or must pass zero follows from that kind's own
// declared TimeoutPolicy.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// governedRegistrationFloor guards against a vacuous pass. This package
// registers 55 kinds through the helper today; the floor sits at 45 so
// retiring a few passes does not drag the gate along, while a walker that
// matched nothing — or a rename of the helper — still trips it.
const governedRegistrationFloor = 45

// kindByGoType inverts the declared table: a call site names the args type, the
// contract is keyed by kind string, and Spec.GoType is the only thing joining
// them.
func kindByGoType() map[string]string {
	byType := map[string]string{}
	for kind, spec := range jobs.Declared() {
		byType[spec.GoType] = kind
	}
	return byType
}

// governedRegistration is one sanctioned registration call site: the args type
// its type argument names, and the expression passed as the supplied timeout —
// nil when the site registered through addDeclaredWorker, which has no third
// argument to pass one through.
type governedRegistration struct {
	goType   string
	supplied ast.Expr
}

// parseComposeSources parses this package's own hand-written PRODUCT files.
// The test binary runs with the package directory as its working directory, so
// the sources under gate are the ones beside this file.
//
// Test sources are excluded along with generated ones, and for the same
// reason: a gate here asks what the runner wires, and a fixture that registers
// a probe kind or schedules a pass to assert on it is not that.
func parseComposeSources(t *testing.T) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing this package's sources: %v", err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		if strings.HasSuffix(path, "_gen.go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files = append(files, file)
	}
	return files
}

// governedRegistrations finds every sanctioned registration call in this
// package: addDeclaredWorker[T](reg, w), which supplies nothing, and
// addDeclaredWorkerWithTimeout[T](reg, w, supplied), which supplies the
// operator's value.
//
// Only those two are matched. addGovernedWorker underneath them is the shared
// body, reached from the generated file and from fixtures that register a kind
// without asserting anything about its wall clock.
func governedRegistrations(t *testing.T) []governedRegistration {
	t.Helper()
	var found []governedRegistration
	for _, file := range parseComposeSources(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// A generic call with one explicit type argument parses as an
			// IndexExpr: addDeclaredWorker[SiteDeepReadArgs](…).
			index, ok := call.Fun.(*ast.IndexExpr)
			if !ok {
				return true
			}
			fn, ok := index.X.(*ast.Ident)
			if !ok {
				return true
			}
			// supplied stays nil for the two-argument form: that is the
			// whole distinction this gate reads.
			var supplied ast.Expr
			switch {
			case fn.Name == "addDeclaredWorker" && len(call.Args) == 2:
			case fn.Name == "addDeclaredWorkerWithTimeout" && len(call.Args) == 3:
				supplied = call.Args[2]
			default:
				return true
			}
			argsType, ok := index.Index.(*ast.Ident)
			if !ok {
				return true
			}
			found = append(found, governedRegistration{goType: argsType.Name, supplied: supplied})
			return true
		})
	}
	return found
}

// TestEveryOperatorSuppliedTimeoutIsActuallySuppliedAtItsRegistration is the
// half a policy test cannot reach. TimeoutPolicy{FromOperator: true} returns
// whatever it is handed, so the declaration is only as good as the argument —
// and registering such a kind through the plain addDeclaredWorker compiles,
// reads as "the ordinary case", and silently puts the kind back on River's
// one-minute default.
//
// The converse is gated in the same walk: a kind whose policy IGNORES the
// supplied value must NOT be registered through the with-timeout form, because
// a computed expression there would read as a budget that governs something
// when Duration never looks at it.
func TestEveryOperatorSuppliedTimeoutIsActuallySuppliedAtItsRegistration(t *testing.T) {
	byType := kindByGoType()
	registrations := governedRegistrations(t)

	operatorSupplied := 0
	for _, r := range registrations {
		kind, declared := byType[r.goType]
		if !declared {
			t.Errorf("%s is registered but api/jobs.yaml declares no kind for it — add it there and run `make gen`", r.goType)
			continue
		}
		spec, ok := jobs.SpecFor(kind)
		if !ok {
			t.Fatalf("%s resolved to kind %q, which has no Spec — the declared table and its GoType index disagree", r.goType, kind)
		}
		switch {
		case spec.Timeout.FromOperator:
			operatorSupplied++
			if r.supplied == nil {
				t.Errorf("%s declares an operator-supplied timeout but registers through addDeclaredWorker, which supplies nothing. TimeoutPolicy.Duration returns what it is handed, so this kind would run at River's one-minute default — register through addDeclaredWorkerWithTimeout, passing the value computed from the operator's config.", kind)
			}
		case r.supplied != nil:
			t.Errorf("%s supplies a timeout its policy never reads. Only a {operator: …} kind takes addDeclaredWorkerWithTimeout; every other one takes its value from api/jobs.yaml, so this expression governs nothing and reads as if it did.", kind)
		}
	}

	if len(registrations) < governedRegistrationFloor {
		t.Fatalf("found only %d governed registrations, expected at least %d — the walker matched almost nothing and this gate would pass vacuously", len(registrations), governedRegistrationFloor)
	}
	if operatorSupplied == 0 {
		t.Fatal("no operator-supplied registration was checked — site_deep_read is the one kind this gate exists for, and it matched nothing")
	}
}

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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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

// governedHelpers is the two sanctioned registration calls and the number of
// arguments each takes. addGovernedWorker underneath them is not one: it is the
// shared body, reached from the generated file and from fixtures that register
// a kind without asserting anything about its wall clock.
var governedHelpers = map[string]int{
	"addDeclaredWorker":            2,
	"addDeclaredWorkerWithTimeout": 3,
}

// parseComposeSources parses this package's own hand-written PRODUCT files.
// The test binary runs with the package directory as its working directory, so
// the sources under gate are the ones beside this file.
//
// Test sources are excluded along with generated ones, and for the same
// reason: a gate here asks what the runner wires, and a fixture that registers
// a probe kind or schedules a pass to assert on it is not that.
func parseComposeSources(t *testing.T) (*token.FileSet, []*ast.File) {
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
	return fset, files
}

// calleeAndTypeArgs reads the function a call names and the explicit type
// arguments it carries: a generic call with one parses as an IndexExpr and with
// several as an IndexListExpr, while one whose type argument is INFERRED parses
// as a plain identifier and carries none. All three forms are answered rather
// than only the one this package writes today, because what the walk must never
// do is pass silently over a registration it did not understand.
func calleeAndTypeArgs(call *ast.CallExpr) (*ast.Ident, []ast.Expr) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun, nil
	case *ast.IndexExpr:
		name, _ := fun.X.(*ast.Ident)
		return name, []ast.Expr{fun.Index}
	case *ast.IndexListExpr:
		name, _ := fun.X.(*ast.Ident)
		return name, fun.Indices
	default:
		return nil, nil
	}
}

// readRegistration reads one call to a governed helper, or says why it could
// not. The error is the point: this walk joins a call site to a declaration
// through the args type NAME, so any spelling that does not hand it a bare name
// is a registration this gate does not cover — and a gate that skips what it
// cannot parse is indistinguishable from one that found nothing wrong.
func readRegistration(call *ast.CallExpr, name string, typeArgs []ast.Expr) (governedRegistration, error) {
	wantArgs := governedHelpers[name]
	if len(call.Args) != wantArgs {
		return governedRegistration{}, fmt.Errorf("%s takes %d arguments, but %d are passed here", name, wantArgs, len(call.Args))
	}
	if len(typeArgs) != 1 {
		return governedRegistration{}, fmt.Errorf(
			"%s is called with %d explicit type arguments; write the args type out, because reading an inferred one would need a type checker and this gate reads source", name, len(typeArgs))
	}
	argsType, bare := typeArgs[0].(*ast.Ident)
	if !bare {
		return governedRegistration{}, fmt.Errorf(
			"%s names its args type as %s rather than a bare identifier; api/jobs.yaml states a plain go_type, so there is nothing to join this to", name, types.ExprString(typeArgs[0]))
	}
	reg := governedRegistration{goType: argsType.Name}
	if wantArgs == 3 {
		reg.supplied = call.Args[2]
	}
	return reg, nil
}

// governedRegistrations finds every sanctioned registration call in this
// package: addDeclaredWorker[T](reg, w), which supplies nothing, and
// addDeclaredWorkerWithTimeout[T](reg, w, supplied), which supplies the
// operator's value.
//
// A call to either that this walk cannot read fails the test where it stands,
// rather than being dropped from the set the caller then checks. A skipped
// registration is a kind whose wall clock nothing gates, and it would leave
// every count below still looking healthy.
func governedRegistrations(t *testing.T) []governedRegistration {
	t.Helper()
	fset, files := parseComposeSources(t)
	var found []governedRegistration
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			callee, typeArgs := calleeAndTypeArgs(call)
			if callee == nil {
				return true
			}
			if _, governed := governedHelpers[callee.Name]; !governed {
				return true
			}
			reg, err := readRegistration(call, callee.Name, typeArgs)
			if err != nil {
				t.Errorf("%s: this registration is written in a form the timeout gate cannot read, so its wall clock is checked by nothing: %v",
					fset.Position(call.Pos()), err)
				return true
			}
			found = append(found, reg)
			return true
		})
	}
	return found
}

// TestEveryOperatorSuppliedTimeoutIsActuallySuppliedAtItsRegistration is the
// half a policy test cannot reach. An operator-supplied TimeoutPolicy returns
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
		case spec.Timeout.FromOperator():
			operatorSupplied++
			if r.supplied == nil {
				t.Errorf("%s declares an operator-supplied timeout but registers through addDeclaredWorker, which supplies nothing. TimeoutPolicy.Duration returns what it is handed, so this kind would run at River's one-minute default — register through addDeclaredWorkerWithTimeout, passing the value computed from the operator's config.", kind)
				continue
			}
			// WHICH dial, not merely that one was passed. The declaration
			// names the JobRunnerConfig field the wall clock is computed
			// from, and a value computed from a different one would satisfy
			// every other check here while making the file say a budget
			// governs this kind that does not.
			if computed := types.ExprString(r.supplied); !strings.Contains(computed, spec.Timeout.OperatorField) {
				t.Errorf("%s declares its timeout comes from JobRunnerConfig.%s, but its registration computes it from %s. The file is what an operator reads to know which dial moves this deadline — change one end or the other so they name the same field.",
					kind, spec.Timeout.OperatorField, computed)
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

// TestARegistrationTheWalkCannotReadIsAFinding is this gate's own
// falsification. Everything above rests on reading a registration's args type
// off the source, and the failure that costs nothing to miss is the one where
// the walk simply does not recognise a call: the kind then disappears from the
// set, every count still looks healthy, and the wall clock it was supposed to
// prove is gated by nobody. So each form the reader cannot join has to come
// back as an error rather than as an absence.
func TestARegistrationTheWalkCannotReadIsAFinding(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{
			name: "a type argument left to inference",
			src:  "addDeclaredWorker(reg, w)",
			want: "explicit type arguments",
		},
		{
			name: "a qualified type argument",
			src:  "addDeclaredWorker[other.SiteDeepReadArgs](reg, w)",
			want: "bare identifier",
		},
		{
			name: "a type argument that is itself generic",
			src:  "addDeclaredWorker[wrapper[SiteDeepReadArgs]](reg, w)",
			want: "bare identifier",
		},
		{
			name: "several type arguments",
			src:  "addDeclaredWorker[SiteDeepReadArgs, VoiceBuildArgs](reg, w)",
			want: "explicit type arguments",
		},
		{
			name: "a with-timeout call that supplies nothing",
			src:  "addDeclaredWorkerWithTimeout[SiteDeepReadArgs](reg, w)",
			want: "takes 3 arguments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, callee, typeArgs := parseGovernedCall(t, tc.src)
			_, err := readRegistration(call, callee.Name, typeArgs)
			if err == nil {
				t.Fatalf("%s was read as a registration this gate covers; it is a form the walk cannot join to a declaration", tc.src)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the finding for %s says %q, want it to name %q so the author knows what to write instead", tc.src, err, tc.want)
			}
		})
	}
}

// The two spellings the package actually uses, read as the gate expects. A
// reader that refused everything would satisfy the test above and gate nothing.
func TestTheSanctionedRegistrationSpellingsAreRead(t *testing.T) {
	plain, callee, typeArgs := parseGovernedCall(t, "addDeclaredWorker[VoiceBuildArgs](reg, w)")
	got, err := readRegistration(plain, callee.Name, typeArgs)
	if err != nil {
		t.Fatalf("the ordinary registration was refused: %v", err)
	}
	if got.goType != "VoiceBuildArgs" || got.supplied != nil {
		t.Errorf("read %+v, want VoiceBuildArgs supplying nothing — a nil supplied is what tells the two forms apart", got)
	}

	withTimeout, callee, typeArgs := parseGovernedCall(t, "addDeclaredWorkerWithTimeout[SiteDeepReadArgs](reg, w, deepReadTimeout(cfg.DeepReadCaps))")
	got, err = readRegistration(withTimeout, callee.Name, typeArgs)
	if err != nil {
		t.Fatalf("the with-timeout registration was refused: %v", err)
	}
	if got.goType != "SiteDeepReadArgs" {
		t.Errorf("read args type %q, want SiteDeepReadArgs", got.goType)
	}
	if computed := types.ExprString(got.supplied); computed != "deepReadTimeout(cfg.DeepReadCaps)" {
		t.Errorf("the supplied expression reads as %q; it is what the declared operator field is compared against", computed)
	}
}

// parseGovernedCall parses one call expression the way the walk sees it.
func parseGovernedCall(t *testing.T, src string) (*ast.CallExpr, *ast.Ident, []ast.Expr) {
	t.Helper()
	expr, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatalf("parsing %s: %v", src, err)
	}
	call, isCall := expr.(*ast.CallExpr)
	if !isCall {
		t.Fatalf("%s parsed as %T, not a call", src, expr)
	}
	callee, typeArgs := calleeAndTypeArgs(call)
	if callee == nil {
		t.Fatalf("%s names no function this walk can read", src)
	}
	return call, callee, typeArgs
}

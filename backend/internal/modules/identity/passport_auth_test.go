// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestBothAgentAuthenticationPathsExecuteTheOneLivenessQuery is a fitness
// function over this package's own source, because the property is structural:
// an agent authenticates either by bearer token or by passport id, and a
// liveness rule that binds on one and not the other is a live credential on
// whichever path was missed. Asserting that the query BUILDER contains the
// constants it concatenates would prove only that concatenation works — it
// would stay green while an entry point quietly grew a SELECT of its own,
// which is the one drift this guard exists to catch. So what is asserted is
// what the two exported entry points reach for, and that the package has
// exactly one statement selecting from passport for authentication.
func TestBothAgentAuthenticationPathsExecuteTheOneLivenessQuery(t *testing.T) {
	const source = "passport.go"
	file, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", source, err)
	}

	// One authentication statement in the file: `FROM passport p` is the
	// aliased shape only the agent-auth query uses (the Settings reads select
	// from passport unaliased). A second occurrence is a second place the
	// liveness rule has to be remembered.
	if got := strings.Count(readSource(t, source), "FROM passport p"); got != 1 {
		t.Errorf("%s contains %d aliased `FROM passport p` statements, want exactly 1: the agent-authentication query is spelled once, or the liveness rule lives in more than one place", source, got)
	}

	for _, entryPoint := range []string{"AuthenticateAgent", "AuthenticateAgentByID"} {
		body := funcBody(t, file, entryPoint)
		if body == nil {
			t.Fatalf("%s has no %s method — the entry point this rule guards is gone or renamed", source, entryPoint)
		}
		if !callsFunc(body, "authenticateAgentWhere") {
			t.Errorf("%s does not go through authenticateAgentWhere, so it does not carry the liveness rule the other path does", entryPoint)
		}
	}
}

// The two predicates differ in NOTHING but the column that names the passport:
// any other difference is a second place the rules can rot apart.
func TestTheTwoAgentPredicatesDifferOnlyInWhichColumnNamesThePassport(t *testing.T) {
	byToken := strings.Replace(agentAuthQuery(agentByHashPredicate), agentByHashPredicate, "<predicate>", 1)
	byID := strings.Replace(agentAuthQuery(agentByIDPredicate), agentByIDPredicate, "<predicate>", 1)
	if byToken != byID {
		t.Errorf("the two authentication paths differ beyond their predicate:\n%s\n---\n%s", byToken, byID)
	}
}

// A locally minted passport answers to no OAuth grant, so the liveness rule
// must be a condition on the joined rows and never a requirement that they
// exist — an inner join, or dropping the IS NULL arm, would take the whole A1
// surface down with it.
func TestTheLivenessRuleExemptsLocallyMintedPassports(t *testing.T) {
	query := agentAuthQuery(agentByHashPredicate)
	if strings.Count(query, "LEFT JOIN") != 2 {
		t.Errorf("the connection joins are not both LEFT JOINs, so a passport with no grant cannot match:\n%s", query)
	}
	if !strings.Contains(query, "p.oauth_grant_id IS NULL") {
		t.Errorf("the liveness predicate has no exemption for a passport that answers to no grant:\n%s", query)
	}
}

// readSource reads one file of this package. The test runs in the package
// directory, so the plain name resolves.
func readSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(raw)
}

// funcBody finds a method or function by name, whatever it is declared on.
func funcBody(t *testing.T, file *ast.File, name string) *ast.BlockStmt {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn.Body
		}
	}
	return nil
}

// callsFunc reports whether body calls name anywhere inside it, including as a
// method on a receiver.
func callsFunc(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			found = found || fn.Name == name
		case *ast.SelectorExpr:
			found = found || fn.Sel.Name == name
		}
		return !found
	})
	return found
}

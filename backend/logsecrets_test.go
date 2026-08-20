// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A credential reaches a log field only on the failure of the channel that was
// supposed to carry it.
//
// A log is not a secret store. It is read by a strictly larger set than the
// process's own filesystem — pods/log RBAC, any viewer role on the log store,
// any CI job scraping container output — and it persists in a searchable index
// long after the credential it names has served its purpose. A secret in a 0600
// file has an owner and a lifetime; the same secret in a log field has neither.
//
// So the rule is not "never". There is one defensible reason to accept a
// credential in a log: the channel that should have held it failed, and
// withholding it would lock an operator out of their own installation. That
// case is narrow and it is self-evidencing — the call sits inside the error
// branch and carries the error that forced it, so a reader sees why the
// credential is there. An UNGUARDED credential log has no such story and is a
// disclosure on every boot.
//
// Fix a violation by removing the value from the call — log the path, the id or
// a fingerprint — or by moving it under the failure it is the fallback for.
// Never by renaming the key: the key set below is what this gate sees, and a
// rename hides the value from the gate without hiding it from a log reader.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// credentialLogKeys are the structured-log attribute keys whose VALUE is a
// credential. The list is hand-maintained, which is the shape that rots, so
// wantCredentialLogKeys pins its size.
//
// Growing it is expected and cheap — a new kind of secret earns an entry and a
// bumped count in the same commit. SHRINKING it is what the pin refuses, because
// a shrink silently un-guards every call site that used the key, and the easiest
// way to make this gate pass is to delete the key it fired on.
//
// Declared as a slice rather than a map[…]string on purpose: gatecensus_test.go's
// isStringValuedMapType enrolls subject-to-reason maps in the fixture-annotation
// census, and this is a key set, not a waiver list.
var credentialLogKeys = []string{
	"access_token",
	"api_key",
	"client_secret",
	"credential",
	"passphrase",
	"password",
	"private_key",
	"refresh_token",
	"secret",
	"setup_token",
	"signing_key",
	"token",
}

// wantCredentialLogKeys pins the size of the set above. Bump it in the commit
// that adds a key; never lower it to quiet a failure.
const wantCredentialLogKeys = 12

// logMethods are the structured-log calls this gate reads: slog's levelled
// methods and their Context variants, the two that take a level argument, and
// With/Group — an attribute attached by With reaches every later line from that
// logger, which is the same disclosure one step removed.
var logMethods = []string{
	"Debug", "DebugContext",
	"Error", "ErrorContext",
	"Group",
	"Info", "InfoContext",
	"Log", "LogAttrs",
	"Warn", "WarnContext",
	"With",
}

// TestACredentialIsLoggedOnlyWhenItsOwnChannelFailed walks every hand-written Go
// file and refuses a structured-log call that carries a credential-shaped
// attribute key without standing in the failure branch of the channel that
// should have carried it.
func TestACredentialIsLoggedOnlyWhenItsOwnChannelFailed(t *testing.T) {
	if len(credentialLogKeys) != wantCredentialLogKeys {
		t.Fatalf("credentialLogKeys holds %d keys, wantCredentialLogKeys is %d — a key was removed; "+
			"restore it, or if the removal is deliberate lower the pin in the same commit and say why",
			len(credentialLogKeys), wantCredentialLogKeys)
	}

	for _, site := range unguardedCredentialLogs(t) {
		t.Errorf("%s: the log attribute %q carries a credential value on every pass through this line — "+
			"log the path, id or fingerprint instead, or, if this is the fallback for a channel that "+
			"failed, put it inside that failure's `if err != nil` and pass the same error to the call "+
			"so a reader can see why the credential is here",
			site.pos, site.key)
	}
}

// credentialLogSite is one structured-log call carrying a credential key.
type credentialLogSite struct {
	pos string
	key string
}

// unguardedCredentialLogs walks the AST rather than matching lines, because the
// shape this gate exists to catch spans lines: a log call whose message sits on
// one line and whose attribute pairs sit on the next is invisible to a
// line-anchored pattern, and a census that cannot see the defect it was written
// for reproduces the hole it was meant to close.
func unguardedCredentialLogs(t *testing.T) []credentialLogSite {
	t.Helper()
	var sites []credentialLogSite
	fset := token.NewFileSet()
	// Every hand-written tree that can hold a log call. cmd and pkg log as
	// freely as internal does, and a first-party extension unit ships the same
	// product, so a walk over internal alone would grade half the tree.
	for _, root := range []string{"internal", "cmd", "pkg", "../extensions"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			walkUnderGuards(file, nil, func(call *ast.CallExpr, guards []string) {
				if !isLogCall(call) {
					return
				}
				for _, key := range credentialKeysIn(call) {
					if reportsOneOf(call, guards) {
						continue
					}
					sites = append(sites, credentialLogSite{pos: fset.Position(call.Pos()).String(), key: key})
				}
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return sites
}

// walkUnderGuards visits every call in the subtree, carrying the identifiers an
// enclosing guard has established as non-nil on that path.
//
// The branching statements are recursed by hand because only SOME of their arms
// inherit the guard: an if's condition runs with nothing proven, its else arm
// proves the negation, and a switch's arms each prove their own case. A blanket
// walk would credit a credential logged in the wrong arm with a guard that does
// not hold there.
func walkUnderGuards(n ast.Node, guards []string, visit func(*ast.CallExpr, []string)) {
	ast.Inspect(n, func(child ast.Node) bool {
		switch node := child.(type) {
		case *ast.IfStmt:
			if child == n {
				return true
			}
			walkIf(node, guards, visit)
			return false
		case *ast.SwitchStmt:
			if child == n || node.Tag != nil {
				return true // a tagged switch compares values; it proves nothing about nil
			}
			walkExprSwitch(node, guards, visit)
			return false
		case *ast.CallExpr:
			visit(node, guards)
		}
		return true
	})
}

// walkIf recurses an if statement, giving the body what the condition proves and
// the else arm what its negation proves.
func walkIf(stmt *ast.IfStmt, guards []string, visit func(*ast.CallExpr, []string)) {
	if stmt.Init != nil {
		walkUnderGuards(stmt.Init, guards, visit)
	}
	walkUnderGuards(stmt.Cond, guards, visit)
	walkUnderGuards(stmt.Body, extend(guards, nonNilWhen(stmt.Cond, true)), visit)
	if stmt.Else != nil {
		walkUnderGuards(stmt.Else, extend(guards, nonNilWhen(stmt.Cond, false)), visit)
	}
}

// walkExprSwitch recurses a tagless switch, whose arms are conditions in their
// own right — `switch { case err != nil: … }` is the same guard as an if, and
// the relay loop spells it that way.
//
// Only a single-expression case proves anything: `case a != nil, b != nil:` is
// an or, so neither identifier is established in the body it opens.
func walkExprSwitch(stmt *ast.SwitchStmt, guards []string, visit func(*ast.CallExpr, []string)) {
	if stmt.Init != nil {
		walkUnderGuards(stmt.Init, guards, visit)
	}
	for _, item := range stmt.Body.List {
		clause, isClause := item.(*ast.CaseClause)
		if !isClause {
			continue
		}
		armGuards := guards
		if len(clause.List) == 1 {
			walkUnderGuards(clause.List[0], guards, visit)
			armGuards = extend(guards, nonNilWhen(clause.List[0], true))
		}
		for _, inner := range clause.Body {
			walkUnderGuards(inner, armGuards, visit)
		}
	}
}

// extend appends to a guard set without writing through the caller's slice —
// two arms of one branch must not inherit each other's proofs.
func extend(guards, more []string) []string {
	if len(more) == 0 {
		return guards
	}
	return append(slices.Clone(guards), more...)
}

// nonNilWhen answers the identifiers a condition proves non-nil when it holds
// (whenTrue) or when it does not, so an else arm and an if body are read by the
// same rule rather than by two hand-written ones.
func nonNilWhen(cond ast.Expr, whenTrue bool) []string {
	switch node := cond.(type) {
	case *ast.ParenExpr:
		return nonNilWhen(node.X, whenTrue)
	case *ast.UnaryExpr:
		if node.Op == token.NOT {
			return nonNilWhen(node.X, !whenTrue)
		}
	case *ast.BinaryExpr:
		return nonNilFromBinary(node, whenTrue)
	}
	return nil
}

// nonNilFromBinary is nonNilWhen's comparison and conjunction half: `e != nil`
// and its negation `e == nil`, and the two connectives, where only the arm that
// forces BOTH operands proves anything.
func nonNilFromBinary(cond *ast.BinaryExpr, whenTrue bool) []string {
	switch cond.Op {
	case token.LAND:
		if !whenTrue {
			return nil
		}
		return append(nonNilWhen(cond.X, true), nonNilWhen(cond.Y, true)...)
	case token.LOR:
		if whenTrue {
			return nil
		}
		return append(nonNilWhen(cond.X, false), nonNilWhen(cond.Y, false)...)
	case token.NEQ, token.EQL:
		subject, isIdent := cond.X.(*ast.Ident)
		nilSide, isNil := cond.Y.(*ast.Ident)
		if !isIdent || !isNil || nilSide.Name != "nil" || (cond.Op == token.NEQ) != whenTrue {
			return nil
		}
		return []string{subject.Name}
	}
	return nil
}

// reportsOneOf reports whether the call passes one of the guarding identifiers,
// which is what makes the fallback self-evidencing: the line that discloses the
// credential also carries the failure that forced the disclosure.
func reportsOneOf(call *ast.CallExpr, guards []string) bool {
	reported := false
	ast.Inspect(call, func(n ast.Node) bool {
		ident, isIdent := n.(*ast.Ident)
		if isIdent && slices.Contains(guards, ident.Name) {
			reported = true
		}
		return !reported
	})
	return reported
}

// isLogCall reports whether the call names one of the structured-log methods.
// Matched on the method name alone: the receiver is a *slog.Logger under a dozen
// different field and variable names across this tree, and pinning the receiver
// would grade only the spellings that exist today.
func isLogCall(call *ast.CallExpr) bool {
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	return isSelector && slices.Contains(logMethods, selector.Sel.Name)
}

// credentialKeysIn answers the credential-shaped string literals anywhere in the
// call's subtree, deduplicated so one call reports each key once. The whole
// subtree, not just the direct arguments: an attribute built with
// slog.String("password", …) nests the key one level down, and a walk over Args
// alone would read that call as clean.
func credentialKeysIn(call *ast.CallExpr) []string {
	var found []string
	ast.Inspect(call, func(n ast.Node) bool {
		lit, isLit := n.(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if slices.Contains(credentialLogKeys, value) && !slices.Contains(found, value) {
			found = append(found, value)
		}
		return true
	})
	return found
}

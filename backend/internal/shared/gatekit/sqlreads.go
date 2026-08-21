// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

// Finding the reads of one table, for the gates that judge them.
//
// Several census gates derive their SITES the same way — a pattern over the SQL
// string literals in the tree, attributed to the declaration holding them — and
// each then judges those sites by its own rule: a held row must be excluded, an
// edge read must be admitted, a served reference must be bounded. The rules
// differ; the walk does not, so the walk lives once.
//
// A census that stops recognising this tree's SQL finds nothing objectionable
// and reads exactly like a clean tree. Nothing fails. That is why the matching
// is here rather than in each gate: one place to be right, one place to test,
// and a gate cannot inherit a blind spot it did not write.
//
// What is deliberately NOT here: the verdicts. Which markers satisfy, which
// waiver sets exist, whether a file or a declaration is the unit of judgement —
// those are each gate's own policy, and a shared "satisfied" would be one
// definition of correctness for several different obligations.

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// TableReadPattern matches a SQL string literal that reads the named table.
//
// Two properties carry it, and a census silently under-reports without either:
//
//   - the match ends on whitespace, a NEWLINE, the end of the text, or a
//     delimiter. Requiring a trailing space misses every statement whose line
//     ends at `FROM person`;
//   - the end-of-text alternate only fires against UNQUOTED text. A literal's
//     token still carries its closing delimiter, so a statement ENDING at the
//     table name is invisible unless the text has been unquoted — which is what
//     LiteralText is for, and what TableReads does.
//
// The table name is quoted into the pattern rather than interpolated raw: a
// caller naming a table with a regex metacharacter would otherwise silently
// widen or break its own census, and a census that matches the wrong thing
// reads exactly like a clean tree.
func TableReadPattern(table string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(FROM|JOIN)\s+` + regexp.QuoteMeta(table) + `(\s|$|[,;)])`)
}

// LiteralText is a string literal's content without its quoting.
//
// A literal Go accepts and strconv does not is not something a census should
// decide about, so the raw form is returned rather than dropped: it still
// matches the table name, and dropping it would mean a read nothing judged.
func LiteralText(node ast.Expr) (string, bool) {
	lit, isLit := node.(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return lit.Value, true
	}
	return value, true
}

// TableRead is one read of the table: the function whose body holds the
// literal, and the SQL's first line for the failure message.
//
// Function is empty for a package-level SQL fragment. A fragment has no
// function to belong to, and a gate that dropped it would exempt exactly the
// shared constants several statements are assembled from.
type TableRead struct {
	Function string
	SQL      string
}

// TableReads collects every read of the table in the file, attributed to the
// enclosing top-level declaration.
//
// Attribution is by DECLARATION SUBTREE rather than by looking a name up in an
// index, and that is not an implementation detail. A module routinely spells the
// same method on two receivers — people has both a Store.RemoveProjectStakeholder
// and a Handlers.RemoveProjectStakeholder — so a by-name index lets one vouch
// for the other, and which one wins is map iteration order. A gate built on that
// is nondeterministic about the thing it exists to be certain of.
func TableReads(file *ast.File, pattern *regexp.Regexp) []TableRead {
	var reads []TableRead
	for _, decl := range file.Decls {
		reads = append(reads, DeclReads(decl, pattern)...)
	}
	return reads
}

// DeclReads is TableReads over ONE top-level declaration, for a gate whose unit
// of judgement is the function rather than the file.
//
// Exported because both of this module's per-function censuses want it, and
// without it they reached TableReads by wrapping a declaration in a synthetic
// ast.File — a fixture standing in for the thing the function documents.
func DeclReads(decl ast.Decl, pattern *regexp.Regexp) []TableRead {
	name := ""
	if fn, isFunc := decl.(*ast.FuncDecl); isFunc {
		name = fn.Name.Name
	}
	var reads []TableRead
	for _, sql := range matchingLiterals(decl, pattern) {
		reads = append(reads, TableRead{Function: name, SQL: sql})
	}
	return reads
}

// FileReadsTable reports whether the file holds any read of the table. It is
// the shape a Scope.Subject wants, and it skips generated files: a census over
// code nobody wrote judges the generator, not the tree.
func FileReadsTable(path string, file *ast.File, pattern *regexp.Regexp) bool {
	if strings.HasSuffix(path, "_gen.go") {
		return false
	}
	return len(matchingLiterals(file, pattern)) > 0
}

// CallsAny reports whether the subtree mentions any of the identifiers.
//
// Identifiers rather than calls, because this tree reaches a gate through a
// package-local wrapper as often as directly, and a matcher that insisted on a
// selector expression would report the wrapper's callers as ungated.
func CallsAny(node ast.Node, names []string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if ident, isIdent := n.(*ast.Ident); isIdent {
			for _, name := range names {
				if strings.Contains(ident.Name, name) {
					found = true
					return false
				}
			}
		}
		return !found
	})
	return found
}

// FirstLineOf names an offending statement in a failure without printing a
// forty-line query into the test log.
func FirstLineOf(sql string) string {
	trimmed := strings.TrimSpace(strings.Trim(sql, "`\""))
	if idx := strings.IndexByte(trimmed, '\n'); idx >= 0 {
		return trimmed[:idx] + " …"
	}
	return trimmed
}

// matchingLiterals returns the unquoted text of every string literal in the
// subtree that the pattern matches.
func matchingLiterals(node ast.Node, pattern *regexp.Regexp) []string {
	var out []string
	ast.Inspect(node, func(n ast.Node) bool {
		lit, isLit := n.(*ast.BasicLit)
		if !isLit {
			return true
		}
		text, isString := LiteralText(lit)
		if isString && pattern.MatchString(text) {
			out = append(out, text)
		}
		return true
	})
	return out
}

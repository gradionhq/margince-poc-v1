// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The pattern's boundary test, and it is the reason this code is shared.
//
// Every miss the censuses built on it have had was a boundary the regex could
// not see, and each one read exactly like a clean tree: nothing failed, a read
// was simply never judged. The cases below are that history — the same table
// of them used to sit inside one census while an identical pattern in another
// carried the identical flaw untested.
func TestTableReadPatternSeesEveryBoundary(t *testing.T) {
	pattern := TableReadPattern("relationship")
	for literal, want := range map[string]bool{
		"`SELECT r.id FROM relationship r WHERE r.kind = 'employment'`": true,
		// Ends AT the table name, so the closing delimiter is the next
		// character. This is the one that slipped: matched against the quoted
		// token rather than the unquoted text, `$` never fires and the read is
		// invisible. It survived a mutation drill because the probe ended the
		// LINE rather than the LITERAL.
		"`SELECT r.person_id FROM relationship`": true,
		// Ends the line, which is the flaw's sibling: a pattern demanding a
		// trailing space misses this one.
		"`SELECT r.person_id\n\tFROM relationship\n\tWHERE r.kind = 'x'`": true,
		"`SELECT r.id FROM relationship, person p`":                       true,
		"`SELECT r.id FROM relationship)`":                                true,
		"`... JOIN relationship theirs ON theirs.person_id = p.id`":       true,
		"`SELECT 1 FROM RELATIONSHIP r`":                                  true,
		// Not reads of this table: a longer name that merely starts with it, a
		// write, and the table's name used as a column.
		"`SELECT 1 FROM relationship_history r`":        false,
		"`INSERT INTO relationship (kind) VALUES ($1)`": false,
		"`SELECT relationship FROM person`":             false,
	} {
		text, isString := LiteralText(&ast.BasicLit{Kind: token.STRING, Value: literal})
		if !isString {
			t.Fatalf("%s did not read back as a string literal", literal)
		}
		if got := pattern.MatchString(text); got != want {
			t.Errorf("the pattern %s %s\n  want it %s — a boundary it cannot see reads exactly "+
				"like a clean tree",
				matched(got), literal, matched(want))
		}
	}
}

// A table name carrying a regex metacharacter must be matched literally, or a
// census silently judges something other than its subject — and reports the
// clean tree it was looking at instead of the one it meant to.
func TestTableReadPatternQuotesTheTableName(t *testing.T) {
	pattern := TableReadPattern("ext_zalo.message")
	text, _ := LiteralText(&ast.BasicLit{
		Kind: token.STRING, Value: "`SELECT 1 FROM ext_zaloxmessage`",
	})
	if pattern.MatchString(text) {
		t.Error("the dot matched any character — a census built on this would judge a table nobody named")
	}
}

// Attribution is by declaration subtree, so a package-level fragment reports as
// having no function rather than being dropped. A census that dropped it would
// exempt exactly the shared constants several statements are assembled from.
func TestTableReadsAttributesEachReadToItsDeclaration(t *testing.T) {
	const src = `package p

const listSQL = "SELECT 1 FROM relationship"

func read() string { return "SELECT r.id FROM relationship r" }

func unrelated() string { return "SELECT 1 FROM person" }
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	reads := TableReads(file, TableReadPattern("relationship"))
	if len(reads) != 2 {
		t.Fatalf("found %d reads, want 2: %+v", len(reads), reads)
	}
	if reads[0].Function != "" {
		t.Errorf("the package-level fragment is attributed to %q, want no function", reads[0].Function)
	}
	if reads[1].Function != "read" {
		t.Errorf("the function read is attributed to %q", reads[1].Function)
	}
}

// LiteralText answers about STRINGS only. A numeric literal reaching a SQL
// matcher as text would be a matcher asking a question about the wrong node.
func TestLiteralTextRefusesANonString(t *testing.T) {
	if _, isString := LiteralText(&ast.BasicLit{Kind: token.INT, Value: "42"}); isString {
		t.Error("an integer literal read back as a string")
	}
}

func matched(yes bool) string {
	if yes {
		return "matched"
	}
	return "missed"
}

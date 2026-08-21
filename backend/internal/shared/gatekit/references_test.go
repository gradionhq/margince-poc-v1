// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

import (
	"go/parser"
	"go/token"
	"testing"
)

// References is what every path-scoped gate here uses to name the site it
// judges, so each way a file can reach a symbol is a way a gate can be walked
// past silently. Each is asserted, in the spelling that would actually be
// written rather than one contrived to be easy to match.
func TestReferencesFindsEverySpellingOfReachingASymbol(t *testing.T) {
	const importPath = "example.com/x/database"
	for _, tc := range []struct {
		name   string
		source string
		want   bool
	}{
		{
			name:   "the canonical qualified call",
			source: "package s\nimport \"example.com/x/database\"\nfunc f() { database.NewPool() }",
			want:   true,
		},
		{
			name:   "an aliased import",
			source: "package s\nimport db \"example.com/x/database\"\nfunc f() { db.NewPool() }",
			want:   true,
		},
		{
			name:   "the symbol taken as a value and applied later",
			source: "package s\nimport \"example.com/x/database\"\nfunc f() { open := database.NewPool; open() }",
			want:   true,
		},
		{
			name:   "a dot-import leaving the symbol bare",
			source: "package s\nimport . \"example.com/x/database\"\nfunc f() { NewPool() }",
			want:   true,
		},
		{
			name:   "a file inside the package the symbol is declared in",
			source: "package database\nfunc f() { NewPool() }",
			want:   true,
		},
		{
			name:   "a blank import, which cannot be called through",
			source: "package s\nimport _ \"example.com/x/database\"\nfunc f() {}",
			want:   false,
		},
		{
			// The file imports the target too, so the traversal runs and the
			// qualifier comparison is what rejects this — not the cheap import
			// check, which would exit before reading a single node.
			name:   "another package's symbol of the same name",
			source: "package s\nimport (\n\"example.com/x/database\"\n\"example.com/x/other\"\n)\nfunc f() { other.NewPool(); database.BindTo() }",
			want:   false,
		},
		{
			name:   "a different symbol from the same package",
			source: "package s\nimport \"example.com/x/database\"\nfunc f() { database.BindTo() }",
			want:   false,
		},
		{
			name:   "the symbol named only in prose and a string",
			source: "package s\nimport \"example.com/x/database\"\n// database.NewPool must not be called here.\nfunc f() { _ = \"database.NewPool\"; _ = database.BindTo }",
			want:   false,
		},
		{
			name:   "a file that neither imports the package nor is declared in it",
			source: "package s\nfunc f() { database := struct{ NewPool int }{}; _ = database.NewPool }",
			want:   false,
		},
		{
			// Shadowing the qualifier does NOT hide the symbol, and that is the
			// bar this gate is set at: it matches names, because following a
			// value to its type needs analysis a fitness test does not have. A
			// file that holds the import is judged on the import, so the cost of
			// naming is a false positive a waiver can answer for — while the
			// cost of the alternative is a real call the gate walks past.
			name:   "a local variable shadowing the qualifier, in a file that imports it",
			source: "package s\nimport \"example.com/x/database\"\nfunc g() { database.BindTo() }\nfunc f() { database := struct{ NewPool int }{}; _ = database.NewPool }",
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "s.go", tc.source, parser.ParseComments)
			if err != nil {
				t.Fatalf("parsing the probe source: %v", err)
			}
			if got := References(file, importPath, "NewPool"); got != tc.want {
				t.Errorf("References = %v, want %v", got, tc.want)
			}
		})
	}
}

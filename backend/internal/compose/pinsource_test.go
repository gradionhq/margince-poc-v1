// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reading a sibling package's pin from its SOURCE, so a gate here can defer to
// a fixture there without keeping a copy of it. A copy is what lets the two
// agree with each other while the pin covers something else.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
)

// quotedMapKeys answers the string keys of the map literal returned by the
// named function in the given file — the shape a test fixture keyed by tool
// verb takes.
func quotedMapKeys(relPath, funcPrefix string) (map[string]bool, error) {
	path := filepath.Clean(relPath)
	src, err := os.ReadFile(path) // #nosec G304 -- a fixed test-source path inside this package's tree
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	prefix := funcPrefix
	if i := len(prefix) - 1; i >= 0 && prefix[i] == '(' {
		prefix = prefix[:i]
	}
	name := prefix[len("func "):]

	keys := map[string]bool{}
	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fn.Name.Name != name {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			kv, isPair := n.(*ast.KeyValueExpr)
			if !isPair {
				return true
			}
			lit, isLit := kv.Key.(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				return true
			}
			if key, uErr := strconv.Unquote(lit.Value); uErr == nil {
				keys[key] = true
			}
			return true
		})
	}
	return keys, nil
}

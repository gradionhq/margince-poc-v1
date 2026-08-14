// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// parseDirByPackage parses every scannable Go file in dir and groups the results
// by package name.
//
// This is go/parser.ParseDir minus its return type. ParseDir is deprecated in
// favour of golang.org/x/tools/go/packages, and that loader is the wrong tool
// here for a reason that will not change: it resolves and type-checks a build,
// while gen-composition runs BEFORE a resolvable build exists — it is what
// materializes the workspace the build then uses. It also reads a unit's source
// on purpose without honouring build tags, because the declaration it validates
// must be the one in the file whatever tags a unit carries. Reimplementing the
// twenty lines keeps that behaviour and drops the deprecated ast.Package, whose
// only field any caller here ever touched was Files.
//
// Files are parsed in os.ReadDir's filename order, which is a documented sort
// and is relied on rather than re-imposed: the manifests derived from these
// files are committed and verified byte-for-byte by check-composition, so the
// walk order is part of the output contract, not an implementation detail.
func parseDirByPackage(fset *token.FileSet, dir string, mode parser.Mode) (map[string][]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	pkgs := map[string][]*ast.File{}
	for _, e := range entries {
		// The .go check belongs here, not in scannableGoFile: ParseDir applied its
		// own ".go" filter BEFORE consulting the caller's, so scannableGoFile only
		// ever had to answer the leftover questions (dotfiles, _-prefixed,
		// _test.go). Absorbing ParseDir means absorbing the check it did first.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || !scannableGoFile(e.Name()) {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, mode)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}
		pkgs[file.Name.Name] = append(pkgs[file.Name.Name], file)
	}
	return pkgs, nil
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ParsedFile is one swept source file: its slash-normalised path relative to
// the sweep universe, and its syntax. The path is what a gate reports and what
// an Exempt entry is keyed by, so it reads the same on every machine.
type ParsedFile struct {
	Path string
	File *ast.File
}

// Scope turns a path-scoped gate's walk root from an assertion into a proof.
//
// A gate that walks a subtree and judges what it finds says two things: what is
// wrong inside the subtree, and — silently — that the subtree is where the
// obligated code lives. Only the first is checked. A root that names the wrong
// tree, or that a later split left behind, finds nothing objectionable and
// reads green, which is indistinguishable from a clean tree.
//
// Scope checks the second claim by sweeping the negative space: it looks at the
// code the gate does NOT look at, and reports any subject it finds there. A
// subject outside every root, unratified, means the roots are wrong.
type Scope struct {
	// Roots are the subtrees the gate claims cover its obligation, relative to
	// Tree.
	Roots []string
	// Subject reports whether a parsed file holds a site this gate must judge.
	// It is the same predicate inside the roots and outside them — the sweep is
	// only meaningful because both sides are asked the same question.
	Subject func(path string, file *ast.File) bool
	// Exempt ratifies files that hold a Subject OUTSIDE every Root and are
	// nonetheless not this gate's business, keyed by path. Each entry carries
	// the reason it is not, and AssertAllMatched reports one that has gone
	// stale.
	Exempt *Waivers[string]
	// Tree bounds the sweep: the universe the Roots are proven against. Empty
	// means the module root, found by walking up for go.mod — every gate in the
	// module then proves its roots against the same tree, whichever package it
	// runs from.
	Tree string
}

// Files parses the sweep universe once and returns the subjects under Roots,
// having first proven that those roots are where the subjects are: every root
// must hold at least one, and no unratified subject may lie outside all of
// them. Files are returned in the walk's lexical order.
//
// Sources are parsed with comments, so a Subject may read a doc comment.
func (s Scope) Files(t testing.TB) []ParsedFile {
	t.Helper()
	if s.Subject == nil {
		t.Errorf("a Scope with no Subject predicate sweeps for nothing: name the site this gate must judge")
		return nil
	}
	roots, usable := s.normalizedRoots(t)
	if !usable {
		return nil
	}
	if len(roots) == 0 {
		t.Errorf("a Scope with no Roots claims no coverage: name the subtrees this gate's obligation lives in")
		return nil
	}
	tree := s.universe(t)
	if tree == "" {
		return nil
	}

	inside, outside, subjectsPerRoot, err := s.sweep(tree, roots)
	if err != nil {
		t.Errorf("sweeping %s: %v", tree, err)
		return nil
	}

	for i, root := range roots {
		if subjectsPerRoot[i] == 0 {
			t.Errorf("%s holds no site this gate judges — a root that finds nothing certifies nothing, so "+
				"either the root is stale or the obligation has moved; correct it rather than leaving the "+
				"gate reading green over an empty tree", root)
		}
	}
	sort.Strings(outside)
	for _, path := range outside {
		if s.Exempt.Waived(t, path) {
			continue
		}
		t.Errorf("%s holds a site this gate must judge but lies outside every root (%s), so the gate never "+
			"sees it: widen the roots if this tier is in scope, or ratify the file in the scope's Exempt set "+
			"with the reason it is not this gate's business", path, strings.Join(roots, ", "))
	}
	return inside
}

// sweep parses the swept sources under tree once and partitions the subjects it
// finds into those a root covers and those no root does, alongside the number of
// subjects each root holds. A subject under several roots counts for each of
// them, because every root owes its own evidence of not being vacuous.
func (s Scope) sweep(tree string, roots []string) (inside []ParsedFile, outside []string, perRoot []int, err error) {
	perRoot = make([]int, len(roots))
	fset := token.NewFileSet()
	err = filepath.WalkDir(tree, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(tree, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !isSweptSource(rel) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		if !s.Subject(rel, file) {
			return nil
		}
		covered := false
		for i, root := range roots {
			if under(rel, root) {
				perRoot[i]++
				covered = true
			}
		}
		if !covered {
			outside = append(outside, rel)
			return nil
		}
		inside = append(inside, ParsedFile{Path: rel, File: file})
		return nil
	})
	return inside, outside, perRoot, err
}

// universe resolves the tree the roots are proven against.
func (s Scope) universe(t testing.TB) string {
	t.Helper()
	if s.Tree != "" {
		return s.Tree
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Errorf("resolving the working directory to find the module root: %v", err)
		return ""
	}
	for {
		// A stat that does not succeed is the answer "no go.mod here", which the
		// loop handles by moving up; reaching the filesystem root is the failure.
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Errorf("no go.mod above the working directory: a Scope with no Tree is bounded by the module "+
				"root, and there is none (searched up from %s)", dir)
			return ""
		}
		dir = parent
	}
}

// normalizedRoots returns the roots in the one spelling paths are compared in,
// deduplicated so a root named twice is reported once, and reports whether every
// declared root names a subtree at all. A root that does not is refused here
// rather than swept, because sweeping it produces a confident finding about the
// obligation when the defect is in the root's spelling.
func (s Scope) normalizedRoots(t testing.TB) (roots []string, usable bool) {
	t.Helper()
	seen := make(map[string]bool, len(s.Roots))
	roots = make([]string, 0, len(s.Roots))
	for _, declared := range s.Roots {
		root := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(declared)), "/")
		switch {
		case root == ".":
			t.Errorf("a Scope root of %q covers the whole sweep universe, so no code lies outside the roots "+
				"and the sweep has nothing left to judge: name the subtrees this gate's obligation lives in",
				declared)
			return nil, false
		case root == "":
			t.Errorf("a Scope root of %q is an absolute path, and a root is relative to the sweep universe, so "+
				"it matches no file at all: name the subtrees this gate's obligation lives in, relative to Tree",
				declared)
			return nil, false
		case seen[root]:
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots, true
}

// under reports whether path lies in root, matching whole segments so that
// "internal/modules" does not swallow "internal/modulesomething".
func under(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// isSweptSource reports whether the file is hand-written product source. Tests
// are excluded because a gate judges what ships; generated files because a
// finding there is fixed in the generator, and this repo spells generated
// output "_gen.go" everywhere.
func isSweptSource(path string) bool {
	return strings.HasSuffix(path, ".go") &&
		!strings.HasSuffix(path, "_test.go") &&
		!strings.HasSuffix(path, "_gen.go")
}

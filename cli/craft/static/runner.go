// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package static

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config tunes the size thresholds and the domain-path rule. Zero values are
// filled with the defaults in withDefaults, so callers can set only what they
// care about.
type Config struct {
	MaxFileLines int    // largeFile threshold (0 → default 500)
	MaxFuncLines int    // longFunc threshold (0 → default 80)
	DomainMarker string // path substring marking a domain module (default "internal/modules/")
	Strict       bool   // when true, MAJOR findings also block the merge
}

func (c Config) withDefaults() Config {
	if c.MaxFileLines == 0 {
		c.MaxFileLines = 500
	}
	if c.MaxFuncLines == 0 {
		c.MaxFuncLines = 80
	}
	if c.DomainMarker == "" {
		c.DomainMarker = "internal/modules/"
	}
	return c
}

// fileContext is the per-file view every check reads. It is built once and
// handed to each check so the AST is parsed a single time.
type fileContext struct {
	path     string
	fset     *token.FileSet
	file     *ast.File
	lineN    int
	isTest   bool
	inDomain bool
	waivers  map[string][]int // check name → lines carrying //craft:ignore
}

// Run walks the given paths (files or directories), applies every check to
// each buildable Go file, drops waived findings, and returns a sorted Report.
// A parse error on one file is skipped, not fatal — this is a linter, not a
// compiler.
func Run(paths []string, cfg Config) (Report, error) {
	cfg = cfg.withDefaults()
	files, err := lintableFiles(paths)
	if err != nil {
		return Report{}, err
	}
	var findings []Finding
	for _, path := range files {
		fs, err := lintFile(path, cfg)
		if err != nil {
			return Report{}, err
		}
		findings = append(findings, fs...)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Severity > findings[j].Severity
	})
	return Report{Tool: "craft static", Verdict: verdict(findings, cfg.Strict), Findings: findings}, nil
}

// lintableFiles expands the given files/directories into the deduped list of
// lintable Go files, in encounter order.
func lintableFiles(paths []string) ([]string, error) {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		if isLintable(p) && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(root)
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			add(p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// lintFile runs every check over one file, dropping waived findings.
func lintFile(path string, cfg Config) ([]Finding, error) {
	src, err := os.ReadFile(path) //nolint:gosec // a linter reads the files it is pointed at
	if err != nil {
		return nil, err
	}
	fc, ok := newFileContext(path, src, cfg.DomainMarker)
	if !ok {
		return nil, nil // unparseable or generated — skip
	}
	var out []Finding
	for _, chk := range checks {
		for _, f := range chk.run(fc, cfg) {
			if fc.waived(f.Check, f.Line) {
				continue
			}
			out = append(out, f)
		}
	}
	return append(out, fc.waiverHygiene()...), nil
}

func verdict(findings []Finding, strict bool) string {
	for _, f := range findings {
		if f.Severity == Blocker || (strict && f.Severity == Major) {
			return "BLOCK"
		}
	}
	return "PASS"
}

// newFileContext parses the source and precomputes the flags checks need. It
// returns ok=false for generated files and parse failures, which are skipped.
func newFileContext(path string, src []byte, domainMarker string) (*fileContext, bool) {
	if isGenerated(src) {
		return nil, false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, false
	}
	fc := &fileContext{
		path:     path,
		fset:     fset,
		file:     file,
		lineN:    countLines(src),
		isTest:   strings.HasSuffix(path, "_test.go"),
		inDomain: strings.Contains(filepath.ToSlash(path), domainMarker),
		waivers:  map[string][]int{},
	}
	fc.parseWaivers()
	return fc, true
}

// parseWaivers records every `//craft:ignore <check> <reason>` directive
// by line, so a check's finding on that line (or the line below it) is
// suppressed. A directive missing its reason is surfaced by waiverHygiene — a
// waiver with no reason is a finding, not a pass (the writeshape philosophy).
func (fc *fileContext) parseWaivers() {
	const prefix = "craft:ignore"
	for _, group := range fc.file.Comments {
		for _, c := range group.List {
			text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), "/*"))
			if !strings.HasPrefix(text, prefix) {
				continue
			}
			fields := strings.Fields(strings.TrimSpace(text[len(prefix):]))
			line := fc.fset.Position(c.Slash).Line
			name := ""
			if len(fields) > 0 {
				name = fields[0]
			}
			fc.waivers[name] = append(fc.waivers[name], line)
		}
	}
}

// waived reports whether a finding for check at line is suppressed by a
// directive on the same line or the line immediately above it.
func (fc *fileContext) waived(check string, line int) bool {
	for _, w := range fc.waivers[check] {
		if w == line || w == line-1 {
			return true
		}
	}
	return false
}

// waiverHygiene flags directives that name a check but give no reason, and
// directives that name no check at all — a silent escape hatch is exactly
// what the gate exists to prevent.
func (fc *fileContext) waiverHygiene() []Finding {
	var out []Finding
	for _, group := range fc.file.Comments {
		for _, c := range group.List {
			text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if !strings.HasPrefix(text, "craft:ignore") {
				continue
			}
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, "craft:ignore")))
			line := fc.fset.Position(c.Slash).Line
			switch {
			case len(fields) == 0:
				out = append(out, newFinding("waiver-hygiene", Major, fc.path, line,
					"craft:ignore names no check — say which check and why"))
			case len(fields) == 1:
				out = append(out, newFinding("waiver-hygiene", Major, fc.path, line,
					"craft:ignore %s has no reason — a waiver must say why", fields[0]))
			}
		}
	}
	return out
}

func countLines(src []byte) int {
	n := 1
	for _, b := range src {
		if b == '\n' {
			n++
		}
	}
	return n
}

func isGenerated(src []byte) bool {
	// The stdlib convention: a `// Code generated ... DO NOT EDIT.` line in
	// the first chunk of the file marks it machine-owned and out of scope.
	head := src
	if len(head) > 2048 {
		head = head[:2048]
	}
	return strings.Contains(string(head), "Code generated") && strings.Contains(string(head), "DO NOT EDIT")
}

func isLintable(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_gen.go")
}

func skipDir(name string) bool {
	switch name {
	case "vendor", "testdata", "node_modules", ".git":
		return true
	}
	return false
}

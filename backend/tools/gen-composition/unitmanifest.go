// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// unitManifestFile is the per-unit generated manifest (ADR-0069 §5): what
// the unit declares that an OPERATOR must resolve, derived from the
// declaration and written next to the unit, so preflight, permission
// review, and the boot inventory read it WITHOUT compiling or executing
// the code. The drift gate (-verify), not a signature, binds it to the
// source for in-repo units.
const unitManifestFile = "manifest.generated.json"

// opAgentToolInvoke is the operation a governed agent tool performs; the
// security descriptor names it so a later capability kind reusing the id
// grammar can never impersonate a tool invocation.
const opAgentToolInvoke = "agent.tool.invoke"

const extensionPkgPath = "github.com/gradionhq/margince/backend/pkg/extension"

// unitManifest is one extension's manifest.generated.json: identity plus
// the RISK TIERS it requests — every operation the extension adds
// that runs at a 🟢/🟡 tier or asks for a scope, the things §7 makes an
// operator resolve. Passive policy an extension merely supplies (a
// jurisdiction pack the core consults, never invokes — no operation, no
// tier) requests no risk tier and never appears here.
type unitManifest struct {
	Schema    int               `json:"schema"`
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	RiskTiers []riskTierRequest `json:"risk_tiers"`
}

// riskTierRequest is one governed operation and the risk tier it
// requests, carrying its ADR-0069 §5 security descriptor: id, operation,
// requested scopes and requested tier are what §7 resolutions bind to,
// through Digest over exactly those four. The scopes are sorted so the
// digest does not depend on declaration order.
type riskTierRequest struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	Scopes    []string `json:"scopes"`
	Tier      string   `json:"tier"`
	Digest    string   `json:"digest"`
}

// descriptor is the canonical form the capability digest covers — id,
// operation, scopes, tier (ADR-0069 §5), nothing else: the kind-specific
// context around it may change and carry forward, but a change to any of
// these four re-opens operator resolution.
func descriptorDigest(c riskTierRequest) (string, error) {
	canonical, err := json.Marshal(struct {
		ID        string   `json:"id"`
		Operation string   `json:"operation"`
		Scopes    []string `json:"scopes"`
		Tier      string   `json:"tier"`
	}{c.ID, c.Operation, c.Scopes, c.Tier})
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// generateUnitManifests derives and writes every enabled unit's manifest.
// The write is skipped when the content is current, so the lane-frequent
// `make composition` never churns source-tree mtimes.
func generateUnitManifests(root string, units []extensionUnit) error {
	vocab, err := publishedVocabulary(root)
	if err != nil {
		return err
	}
	for _, u := range units {
		encoded, err := deriveUnitManifest(u, vocab)
		if err != nil {
			return err
		}
		path := filepath.Join(u.Dir, unitManifestFile)
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, encoded) {
			continue
		}
		if err := writeFileAtomic(u.Dir, path, encoded); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes content to a temp file in dir and renames it
// over path. Rename replaces the destination NAME — it never follows a
// symlink sitting there — so a unit cannot redirect its manifest write at
// a repository file, and there is no check-then-write TOCTOU window (the
// earlier Lstat guard was fail-open on a stat error and racy).
func writeFileAtomic(dir, path string, content []byte) error {
	tmp, err := os.CreateTemp(dir, "manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename consumes it
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Carry the existing manifest's mode forward (a committed 0644 stays
	// 0644) so a consumer running as another UID can still read it — the
	// mode is read from the file, never a permissive literal, so it does
	// not loosen anything the tree did not already have. A genuinely absent
	// path (a brand-new manifest) keeps CreateTemp's owner-only 0600 (git
	// records only the exec bit, and the next checkout normalizes it); any
	// OTHER stat error is fatal rather than a silent drop to 0600.
	switch fi, err := os.Stat(path); {
	case err == nil:
		if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
			return err
		}
	case !os.IsNotExist(err):
		return err
	}
	return os.Rename(tmpName, path)
}

// verifyUnitManifests re-derives every unit's manifest and requires the
// file next to the unit to be byte-identical — a hand edit, a stale
// derivation, or a foreign encoder fails here even when the semantic
// content agrees (the composition.json input row only pins the digest;
// THIS is the gate that ties the digest back to the declaration).
func verifyUnitManifests(root string, units []extensionUnit) error {
	vocab, err := publishedVocabulary(root)
	if err != nil {
		return err
	}
	for _, u := range units {
		encoded, err := deriveUnitManifest(u, vocab)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(filepath.Join(u.Dir, unitManifestFile))
		if err != nil {
			return fmt.Errorf("extensions/%s/%s: %w — run 'make gen'", u.Name, unitManifestFile, err)
		}
		if !bytes.Equal(onDisk, encoded) {
			return fmt.Errorf("extensions/%s/%s differs from its derivation — run 'make gen'", u.Name, unitManifestFile)
		}
	}
	return nil
}

// publishedVocabulary maps the published extension package's string
// constants (the Tier and Scope values) to their literals by parsing the
// seam's own source — the reader's vocabulary derives from the tree and
// can never drift from what extensions compile against.
func publishedVocabulary(root string) (map[string]string, error) {
	dir := filepath.Join(root, "backend", "pkg", "extension")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool { return scannableGoFile(fi.Name()) }, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing the published extension surface: %w", err)
	}
	vocab := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			collectStringConsts(file, vocab)
		}
	}
	return vocab, nil
}

func collectStringConsts(file *ast.File, vocab map[string]string) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		// Go repeats the previous expression list when a grouped const
		// omits its own (the `const ( A = "x"; B )` form makes B == "x").
		// Carry it forward so such a string constant is not silently
		// dropped from the vocabulary; a non-string repeat (iota) simply
		// yields no string literal below.
		var last []ast.Expr
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if len(vs.Values) > 0 {
				last = vs.Values
			}
			addStringConsts(vs.Names, last, vocab)
		}
	}
}

// addStringConsts records the string-literal constants of one spec into
// vocab; non-string or computed values are skipped — only literal string
// constants form the vocabulary.
func addStringConsts(names []*ast.Ident, values []ast.Expr, vocab map[string]string) {
	if len(names) != len(values) {
		return
	}
	for i, name := range names {
		lit, ok := values[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if value, err := strconv.Unquote(lit.Value); err == nil {
			vocab[name.Name] = value
		}
	}
}

// deriveUnitManifest reads one unit's declaration statically and emits its
// manifest. It parses the unit's New() constructor from the AST — never
// compiling or running it — so the reader accepts only LITERAL values; a
// computed one is a positioned error, never a silent gap in what review
// sees.

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

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// unitManifestFile is the per-unit generated manifest: what
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

// kindAgentTool is the CAPABILITY KIND the descriptor records. It is not a
// restatement of Operation: the operation says what running the capability
// does, the kind says which seam registers it — and a second kind arrives with
// the scheduled-job seam, whose operation will also be an invocation. Keeping
// them separate in the digest means an operator resolution recorded for a tool
// cannot carry to a job that happens to share every other field.
const kindAgentTool = "agent_tool"

const extensionPkgPath = "github.com/gradionhq/margince/backend/pkg/extension"

// unitManifest is one extension's manifest.generated.json: identity plus
// the RISK TIERS it requests — every operation the extension adds
// that runs at a 🟢/🟡 tier or asks for a scope — the things an
// operator must resolve. Passive policy an extension merely supplies (a
// jurisdiction pack the core consults, never invokes — no operation, no
// tier) requests no risk tier and never appears here.
type unitManifest struct {
	Schema    int               `json:"schema"`
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	RiskTiers []riskTierRequest `json:"risk_tiers"`

	// Secrets are the secret keys the unit declares (see
	// extension.SecretsRequest) — inert data an operator resolves, never a
	// live capability. omitempty rather than mirroring RiskTiers' bare "[]":
	// nothing declares Secrets yet, and every manifest already committed to
	// the tree predates this field, so an unconditional key would rewrite
	// every one of them for a field they do not use.
	Secrets []secretsRequest `json:"secrets,omitempty"`
}

// secretsRequest is one declared secret key and scope (see
// extension.SecretsRequest), sorted by key then scope so the encoding does
// not depend on declaration order.
type secretsRequest struct {
	Key   string `json:"key"`
	Scope string `json:"scope"`
}

// riskTierRequest is one governed operation and the risk tier it requests,
// carrying its security descriptor. Every field but Digest is IN the digest,
// and the set is wider than it was when a capability was an AST literal in a
// unit's Go file, because a contract-declared capability has more that can
// change without changing its name:
//
//   - Unit — the declaring extension. Two units may legitimately serve
//     different verbs; a resolution for one must never read as a resolution
//     for the other's.
//   - Kind — which seam registers it (see kindAgentTool).
//   - Contract — the source identity: the base contract the fragment extended.
//     The same operation id under a different contract is a different
//     published thing.
//   - Operation — what invoking it does.
//   - OperationID, Route, Method — the published HTTP surface. A verb that
//     keeps its name while moving to another route, or gaining a method, is a
//     new promise to every client.
//   - Scopes, Tier — the authority requested. These are the two an operator is
//     actually deciding about.
//   - FragmentHash — everything else in the declaration: the request and
//     response schemas, and the prose a model selects the tool by. None of it
//     grants authority, and all of it changes what a model will do with the
//     authority granted, so a resolution recorded against the old text should
//     not carry silently to new text.
//
// The scopes are sorted (one element today) so the digest does not depend on
// declaration order.
type riskTierRequest struct {
	ID           string   `json:"id"`
	Unit         string   `json:"unit"`
	Kind         string   `json:"kind"`
	Contract     string   `json:"contract"`
	Operation    string   `json:"operation"`
	OperationID  string   `json:"operation_id"`
	Route        string   `json:"route"`
	Method       string   `json:"method"`
	Scopes       []string `json:"scopes"`
	Tier         string   `json:"tier"`
	FragmentHash string   `json:"fragment_hash"`
	Digest       string   `json:"digest"`
}

// descriptorDigest hashes the canonical form of everything the descriptor
// records. It re-encodes through an explicit anonymous struct rather than
// marshalling riskTierRequest itself, so that adding a field to the JSON shape
// is a deliberate decision about whether it belongs in the digest — a
// `json:"-"` on Digest would have made every future field digest-covered by
// default, which is the wrong default for a presentational one.
func descriptorDigest(c riskTierRequest) (string, error) {
	canonical, err := json.Marshal(struct {
		ID           string   `json:"id"`
		Unit         string   `json:"unit"`
		Kind         string   `json:"kind"`
		Contract     string   `json:"contract"`
		Operation    string   `json:"operation"`
		OperationID  string   `json:"operation_id"`
		Route        string   `json:"route"`
		Method       string   `json:"method"`
		Scopes       []string `json:"scopes"`
		Tier         string   `json:"tier"`
		FragmentHash string   `json:"fragment_hash"`
	}{c.ID, c.Unit, c.Kind, c.Contract, c.Operation, c.OperationID, c.Route, c.Method, c.Scopes, c.Tier, c.FragmentHash})
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// generateUnitManifests derives and writes every enabled unit's manifest.
// The write is skipped when the content is current, so the lane-frequent
// `make composition` never churns source-tree mtimes.
func generateUnitManifests(root string, units []extensionUnit, verbs []declaredVerb, jobDecls []extension.JobDeclaration) error {
	vocab, err := publishedVocabulary(root)
	if err != nil {
		return err
	}
	byUnit := verbsByUnit(verbs)
	jobsByUnit := jobDeclarationsByUnit(jobDecls)
	for _, u := range units {
		encoded, err := deriveUnitManifest(u, vocab, byUnit[u.Name], jobsByUnit[u.Name])
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

// jobDeclarationsByUnit groups the composed job declarations by declaring
// unit, so each manifest derivation sees its own and no other's.
func jobDeclarationsByUnit(decls []extension.JobDeclaration) map[string][]extension.JobDeclaration {
	byUnit := map[string][]extension.JobDeclaration{}
	for _, d := range decls {
		byUnit[string(d.Unit)] = append(byUnit[string(d.Unit)], d)
	}
	return byUnit
}

// verifyUnitManifests re-derives every unit's manifest and requires the
// file next to the unit to be byte-identical — a hand edit, a stale
// derivation, or a foreign encoder fails here even when the semantic
// content agrees (the composition.json input row only pins the digest;
// THIS is the gate that ties the digest back to the declaration).
func verifyUnitManifests(root string, units []extensionUnit, verbs []declaredVerb, jobDecls []extension.JobDeclaration) error {
	vocab, err := publishedVocabulary(root)
	if err != nil {
		return err
	}
	byUnit := verbsByUnit(verbs)
	jobsByUnit := jobDeclarationsByUnit(jobDecls)
	for _, u := range units {
		encoded, err := deriveUnitManifest(u, vocab, byUnit[u.Name], jobsByUnit[u.Name])
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

// verbsByUnit groups the composed verb set by declaring unit, preserving the
// order extensionVerbs produced — which is what keeps a manifest's risk-tier
// list stable across regenerations.
func verbsByUnit(verbs []declaredVerb) map[string][]declaredVerb {
	byUnit := make(map[string][]declaredVerb)
	for _, d := range verbs {
		unit := string(d.verb.Unit)
		byUnit[unit] = append(byUnit[unit], d)
	}
	return byUnit
}

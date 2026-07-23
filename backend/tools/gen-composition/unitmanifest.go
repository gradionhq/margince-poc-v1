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
	"strings"
)

// unitManifestFile is the per-unit generated manifest (ADR-0069 §5):
// what the unit claims, derived from its declaration and written next to
// it, so preflight, permission review, and the inventory read what an
// extension declares WITHOUT compiling or executing its code. The drift
// gate (-verify), not a signature, binds it to the source for in-repo
// units.
const unitManifestFile = "manifest.generated.json"

// opRegisterJurisdictionPack is the one operation the jurisdiction
// capability kind performs; the security descriptor names it so a later
// kind reusing the id grammar can never impersonate a pack registration.
const opRegisterJurisdictionPack = "jurisdiction.register-pack"

// unitManifest is one extension's manifest.generated.json. Fields are
// ordered for the human reviewer: identity first, then the claims.
type unitManifest struct {
	Schema       int              `json:"schema"`
	Name         string           `json:"name"`
	Version      string           `json:"version"`
	Capabilities []unitCapability `json:"capabilities"`
}

// unitCapability is one declared capability with its ADR-0069 §5
// security descriptor: the id, operation, requested scopes and requested
// tier are what §7 resolutions bind to (Digest covers exactly those
// four); the kind-specific payload after them is review context, part of
// the manifest but deliberately NOT part of the descriptor digest — a
// payload change carries forward, a request change re-resolves.
type unitCapability struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	Scopes    []string `json:"scopes"`
	// Tier is the requested execution tier. Empty for passive policy
	// capabilities (a jurisdiction pack is consulted, never invoked);
	// tool-bearing kinds request 🟢/🟡 here when their slices land.
	Tier   string `json:"tier"`
	Digest string `json:"digest"`

	Jurisdiction *jurisdictionClaim `json:"jurisdiction,omitempty"`
}

// jurisdictionClaim is the jurisdiction kind's payload: the pack's code
// and every statutory retention floor it declares, in the same rendering
// the engine uses (ISO 8601 periods, named anchors) so a reviewer reads
// exactly what the boot registers.
type jurisdictionClaim struct {
	Code      string         `json:"code"`
	Retention []retentionRow `json:"retention"`
}

type retentionRow struct {
	Name   string `json:"name"`
	Keep   string `json:"keep"`
	Anchor string `json:"anchor"`
}

// descriptor is the canonical form the capability digest covers — id,
// operation, scopes, tier (ADR-0069 §5), nothing else.
type descriptor struct {
	ID        string   `json:"id"`
	Operation string   `json:"operation"`
	Scopes    []string `json:"scopes"`
	Tier      string   `json:"tier"`
}

func descriptorDigest(c unitCapability) (string, error) {
	canonical, err := json.Marshal(descriptor{ID: c.ID, Operation: c.Operation, Scopes: c.Scopes, Tier: c.Tier})
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
		if err := os.WriteFile(path, encoded, 0o644); err != nil { // #nosec G306 -- generated manifest, not a secret
			return err
		}
	}
	return nil
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

// publishedVocabulary maps the published jurisdiction package's string
// constants (retention class names, anchors) to their values by parsing
// the seam's own source — the reader's vocabulary derives from the tree
// and can never drift from what extensions compile against.
func publishedVocabulary(root string) (map[string]string, error) {
	dir := filepath.Join(root, "backend", "pkg", "extension", "jurisdiction")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing the published jurisdiction surface: %w", err)
	}
	vocab := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != len(vs.Values) {
						continue
					}
					for i, name := range vs.Names {
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						value, err := strconv.Unquote(lit.Value)
						if err != nil {
							return nil, err
						}
						vocab[name.Name] = value
					}
				}
			}
		}
	}
	return vocab, nil
}

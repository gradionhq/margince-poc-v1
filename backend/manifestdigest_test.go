// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Manifest-hash encoding fitness function (ADR-0069 §7): every hash a generated
// unit manifest publishes says which algorithm produced it.
//
// A manifest is the record an operator reads to resolve what a unit requests,
// and a field whose encoding depends on which kind of entry carries it is one
// every reader has to special-case. A reader that did not would compare two
// spellings of the same digest and see a change that never happened.
//
// It lives here rather than beside the generator because it reads files OUTSIDE
// its own module, which Go's test cache cannot key: in the tools module a
// manifest edited to the old spelling replays the previous pass and reports ok.
// The root fitness lane runs `-count=1` for exactly this reason.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const unitManifestFile = "manifest.generated.json"

// manifestDigestEncoding is the ONE spelling every hash in a manifest carries.
var manifestDigestEncoding = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// isManifestHashField reports whether a field NAME is one that carries a hash.
// Derived from the name rather than from a maintained list, so a field added
// later is covered the moment it is named like the ones beside it — which is
// the point, because the encoding is a property of the FILE and not of the
// entries a reader happens to know about. The generator already computes a tree
// digest and a manifest digest it does not yet publish; either would arrive
// under a name this predicate already admits.
func isManifestHashField(name string) bool {
	return strings.HasSuffix(name, "hash") || strings.HasSuffix(name, "digest")
}

// manifestTrees are the two roots a unit manifest can live under, and whether
// one is guaranteed to hold a unit that requests authority. extensions/ may
// legitimately hold none — presence under it IS enablement, so the vanilla lane
// composes an empty tree on purpose, and `de` is a jurisdiction pack that
// requests no risk tier at all.
var manifestTrees = []struct {
	root           string
	mustHoldTiered bool
}{
	{root: "../extensions"},
	{root: "../fixtures", mustHoldTiered: true},
}

func TestEveryGeneratedManifestHashNamesItsAlgorithm(t *testing.T) {
	for _, tree := range manifestTrees {
		var tiered int
		for _, path := range committedManifests(t, tree.root) {
			tiered += assertManifestHashes(t, path)
		}
		// A root that reads nothing passes exactly like a clean one, and an
		// aggregate count across both roots cannot catch it: extensions/ alone
		// would carry the total forever while a fixture tree the walk never
		// reached escaped the rule.
		if tree.mustHoldTiered && tiered == 0 {
			t.Errorf("%s yielded no risk-tier entry — the walk is reading no manifest that owes a hash", tree.root)
		}
	}
}

// assertManifestHashes holds one committed manifest to the encoding and returns
// how many risk-tier entries it carried.
//
// It DECODES rather than scanning the text, and that is the load-bearing part.
// A key written `fragment_hash`, a value that is null or a number, and a
// compact `":"` separator all name the same field to every real reader of this
// file; a text scan matches none of them and skips each one in silence.
func assertManifestHashes(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- path comes from walking the trusted source tree
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var document json.RawMessage
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("%s is not valid JSON, so nothing below read what an operator reads: %v", path, err)
	}
	assertHashFieldsIn(t, path, document, "")
	return assertEveryEntryIsHashed(t, path, content)
}

// assertHashFieldsIn walks a decoded manifest and holds every hash-named field
// to the encoding, wherever in the document it sits.
func assertHashFieldsIn(t *testing.T, path string, raw json.RawMessage, at string) {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil {
		for name, value := range object {
			where := at + "." + name
			if isManifestHashField(name) {
				assertDigestEncoding(t, path, where, value)
			}
			assertHashFieldsIn(t, path, value, where)
		}
		return
	}
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err == nil {
		for i, item := range array {
			assertHashFieldsIn(t, path, item, fmt.Sprintf("%s[%d]", at, i))
		}
	}
	// Anything that reads as neither container is a scalar and has no children.
	// The document was decoded whole before this walk began, so neither failure
	// above can be a parse error going unreported here.
}

func assertDigestEncoding(t *testing.T, path, where string, value json.RawMessage) {
	t.Helper()
	var encoded string
	if err := json.Unmarshal(value, &encoded); err != nil {
		t.Errorf("%s: %s is %s — a hash that is not a string is not one a reader can compare", path, where, value)
		return
	}
	if !manifestDigestEncoding.MatchString(encoded) {
		t.Errorf("%s: %s = %q, want an algorithm-prefixed sha256 digest", path, where, encoded)
	}
}

// assertEveryEntryIsHashed is what stops the walk above from passing a file it
// found nothing in. Both hashes are structural — a risk-tier entry is the
// authority request an operator resolves, `fragment_hash` covers the
// declaration the descriptor's own fields do not, and `digest` covers the whole
// descriptor — so an entry missing either is a request that cannot be resolved,
// not merely one this gate did not inspect.
func assertEveryEntryIsHashed(t *testing.T, path string, content []byte) int {
	t.Helper()
	var manifest struct {
		RiskTiers []struct {
			ID           string  `json:"id"`
			FragmentHash *string `json:"fragment_hash"`
			Digest       *string `json:"digest"`
		} `json:"risk_tiers"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("%s does not carry the manifest shape: %v", path, err)
	}
	for _, entry := range manifest.RiskTiers {
		if entry.FragmentHash == nil || entry.Digest == nil {
			t.Errorf("%s: risk tier %q carries no fragment_hash and/or no digest", path, entry.ID)
		}
	}
	return len(manifest.RiskTiers)
}

// committedManifests is every generated unit manifest under one root. Derived
// by walking rather than listed, so a unit added later is covered without
// anyone remembering to add it here.
func committedManifests(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A unit's frontend layer is a workspace package, so extensions/ holds
		// installed dependency trees. Nothing generated by this repo lives in
		// one, and a dependency that happened to ship a file under this name
		// would be read and judged as a unit manifest.
		if d.IsDir() && d.Name() == "node_modules" {
			return fs.SkipDir
		}
		// A symlink would be judged on its target's bytes while its provenance
		// points elsewhere — the same refusal digestTree makes one tree over.
		if !d.IsDir() && d.Name() == unitManifestFile && d.Type().IsRegular() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// License-notice fitness function (business/12-license.md §5 "honest
// labeling", §8 "don't strip notices"): every hand-written Go file must
// carry the BUSL-1.1 SPDX header, and the obligation is derived from the
// tree rather than a checklist — a new file is enrolled the moment it
// exists. Generated files are exempt: their headers are owned by the
// generator (and the drift gate), not by hand.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The locked header, in order, at the very top of the file (see STATUS.md).
const spdxHeader = "// SPDX-License-Identifier: BUSL-1.1\n// SPDX-FileCopyrightText: 2026 Gradion\n"

// The canonical machine-readable "generated file" marker (`go help
// generate`): a comment line matching this, sitting before the package
// clause, means the file is generated and exempt from the hand-written
// notice rule.
var generatedMarker = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

func isGenerated(path, text string) bool {
	if strings.HasSuffix(path, "_gen.go") {
		return true
	}
	// The backend's contracts package is owned by the contract pipeline and
	// frozen by the drift gate (`git diff --exit-code -- internal/contracts/`);
	// even its hand-written doc.go/gen.go must stay byte-identical, so no
	// notice is stamped there. Anchored at the backend tree's own root, not
	// matched anywhere in the path: the sweep also walks sibling modules, and
	// a unit of its own naming a directory `internal/contracts/` would
	// otherwise inherit an exemption that belongs to one frozen package.
	if strings.HasPrefix(filepath.ToSlash(path), "internal/contracts/") {
		return true
	}
	head := text
	if i := strings.Index(text, "\npackage "); i >= 0 {
		head = text[:i]
	}
	return generatedMarker.MatchString(head)
}

// TestTheContractsExemptionIsAnchoredToTheBackendTree: the exemption belongs
// to ONE frozen package, so it must not travel to a sibling module that
// happens to name a directory the same way — an inherited exemption is a file
// shipping with no notice and a gate reporting it clean.
func TestTheContractsExemptionIsAnchoredToTheBackendTree(t *testing.T) {
	const unlicensed = "package contracts\n"
	if !isGenerated("internal/contracts/doc.go", unlicensed) {
		t.Error("the backend's frozen contracts package lost its exemption")
	}
	if isGenerated("../extensions/u/internal/contracts/tool.go", unlicensed) {
		t.Error("a unit's own internal/contracts/ inherited the backend contract pipeline's exemption")
	}
}

// licensedTree is one hand-written Go tree the notice rule covers, and
// whether that tree is guaranteed to contain a file. The backend module is one
// tree, not all of them: extensions/ and fixtures/ are separate modules a
// `./...` from here never reaches, and a first-party unit ships under the same
// license as the code it composes into.
//
// mustHaveFiles is what stops a root from passing by scanning nothing — an
// aggregate count cannot, because backend/ alone would carry it forever while
// a unit tree silently escaped the rule. extensions/ is the one root that may
// legitimately be empty: presence under it IS enablement, so the vanilla lane
// composes an empty tree on purpose. A root that does not exist at all is
// still caught, by the walk error below.
type licensedTree struct {
	root          string
	mustHaveFiles bool
}

var licensedTrees = []licensedTree{
	{root: ".", mustHaveFiles: true},
	{root: "../extensions"},
	{root: "../fixtures", mustHaveFiles: true},
}

func TestEveryHandWrittenGoFileCarriesTheLicenseHeader(t *testing.T) {
	var missing []string
	walk := func(root string) (int, error) {
		checked := 0
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// extensions/ now holds installed dependency trees (a unit's
			// frontend layer is a workspace package). Nothing in one is
			// hand-written, and walking it is thousands of files of pure cost.
			if d.IsDir() && d.Name() == "node_modules" {
				return fs.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.go file from walking the trusted source tree
			if err != nil {
				return err
			}
			text := string(b)
			if isGenerated(path, text) {
				return nil
			}
			checked++
			if !strings.HasPrefix(text, spdxHeader) {
				missing = append(missing, filepath.ToSlash(path))
			}
			return nil
		})
		return checked, err
	}
	for _, tree := range licensedTrees {
		checked, err := walk(tree.root)
		if err != nil {
			t.Fatalf("walking %s: %v", tree.root, err)
		}
		if tree.mustHaveFiles && checked == 0 {
			t.Fatalf("%s yielded no hand-written Go file — a root that scans nothing passes exactly like a clean one", tree.root)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d Go file(s) missing the BUSL-1.1 SPDX header (add it above the package clause, then a blank line):\n\t%s",
			len(missing), strings.Join(missing, "\n\t"))
	}
}

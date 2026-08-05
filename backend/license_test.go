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
	// The whole contracts package is owned by the contract pipeline and
	// frozen by the drift gate (`git diff --exit-code -- internal/contracts/`);
	// even its hand-written doc.go/gen.go must stay byte-identical, so no
	// notice is stamped there.
	if strings.Contains(filepath.ToSlash(path), "internal/contracts/") {
		return true
	}
	head := text
	if i := strings.Index(text, "\npackage "); i >= 0 {
		head = text[:i]
	}
	return generatedMarker.MatchString(head)
}

// licensedTrees are the hand-written Go trees the notice rule covers. The
// backend module is one of them, not all of them: extensions/ and fixtures/
// are separate modules a `./...` from here never reaches, and a first-party
// unit ships under the same license as the code it composes into.
var licensedTrees = []string{".", "../extensions", "../fixtures"}

func TestEveryHandWrittenGoFileCarriesTheLicenseHeader(t *testing.T) {
	var missing []string
	var checked int
	walk := func(root string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
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
	}
	for _, root := range licensedTrees {
		if err := walk(root); err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	// A tree that yields no hand-written file passes silently, and silence
	// here reads exactly like a clean tree — the failure mode a fitness
	// function must never have.
	if checked == 0 {
		t.Fatal("no hand-written Go files found — the notice rule checked nothing")
	}
	if len(missing) > 0 {
		t.Errorf("%d Go file(s) missing the BUSL-1.1 SPDX header (add it above the package clause, then a blank line):\n\t%s",
			len(missing), strings.Join(missing, "\n\t"))
	}
}

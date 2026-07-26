// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Prompt-boundary fitness function: no prompt may declare a data boundary
// the writer of that data can spell.
//
// The control this replaced was a fixed <untrusted> marker, and it failed for
// a reason no review pass catches reliably — a marker built out of characters
// is a marker a sender can write, in another script, with an invisible rune
// mid-word, or assembled across two separately wrapped fields. The boundary is
// now minted per call in shared/kernel/promptfence and named in that call's own
// system prompt, so the obligation is derived from the tree rather than
// remembered: a new prompt that hard-codes the old marker fails here, whoever
// writes it and however plausible it looks.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixed marker no prompt may build a boundary from again.
const fixedBoundaryMarker = "<untrusted>"

// promptfence itself writes the marker: its rule sentence tells the model that
// a literal <untrusted> inside the data carries no authority, which is the one
// place naming it is the point.
const promptfencePackage = "internal/shared/kernel/promptfence/"

func TestNoPromptDeclaresAFixedDataBoundary(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(path)
		if d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasPrefix(slashed, promptfencePackage) {
			return nil
		}
		b, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.go file from walking the trusted source tree
		if err != nil {
			return err
		}
		// Tests are where a forged marker BELONGS: the attacks that defeated
		// the fixed fence are fixtures now, and a test that could not write
		// one could not prove the boundary holds.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(string(b), fixedBoundaryMarker) {
			offenders = append(offenders, slashed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the source tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("these files build a prompt boundary out of the fixed marker %s, which the writer of the fenced data can spell — mint one per call with promptfence.New() and name it in that call's system prompt with Fence.Rule:\n  %s",
			fixedBoundaryMarker, strings.Join(offenders, "\n  "))
	}
}

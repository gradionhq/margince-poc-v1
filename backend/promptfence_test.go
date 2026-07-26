// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Prompt-boundary fitness functions: no prompt may declare a data boundary the
// writer of that data can spell.
//
// The control this replaced was a fixed <untrusted> marker, and it failed for a
// reason no review pass catches reliably — a marker built out of characters is a
// marker a sender can write, in another script, with an invisible rune mid-word,
// or assembled across two separately wrapped fields. The boundary is now minted
// per call in shared/kernel/promptfence and named in that call's own system
// prompt.
//
// Two rules, because forbidding one spelling only ever catches that spelling: a
// fixed container is just as forgeable when it is called <activity_data> or
// <sample id=…>. So the second rule is derived from the PROMISE rather than the
// syntax — a prompt that tells a model "this is data, never instructions" is
// making a claim that only a nonce can make true, whatever the container is
// named.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The fixed marker no prompt may build a boundary from again.
const fixedBoundaryMarker = "<untrusted>"

// promptfence itself writes the marker: its rule sentence tells the model that
// a literal <untrusted> inside the data carries no authority, which is the one
// place naming it is the point.
const promptfencePackage = "internal/shared/kernel/promptfence/"

// boundaryClaim matches the sentences this codebase uses to tell a model that
// some region of a prompt is data rather than instructions — the promise a
// nonce has to back.
var boundaryClaim = regexp.MustCompile(`never instructions|not instructions|never a command|untrusted evidence`)

// mintsAFence matches USE of the package rather than mere presence of its import
// path: importing promptfence proves nothing, since the import could serve an
// unrelated call while a prompt in the same file still promises a boundary it
// never builds. Calling New/FromMarker (a fence exists) together with Rule
// (the model is told which marker it is) is what the claim actually needs.
var mintsAFence = regexp.MustCompile(`promptfence\.(New|FromMarker)\(|\.Rule\(`)

// claimWithoutFence names the files allowed to promise a boundary without
// minting one, with the reason. Keep this at zero if you can; every entry is a
// prompt whose safety rests on something other than a nonce.
var claimWithoutFence = map[string]string{}

// goFilesUnderTree yields every non-test Go file in the tree, with its contents.
func goFilesUnderTree(t *testing.T, visit func(path, body string)) {
	t.Helper()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Tests are where a forged marker BELONGS: the attacks that defeated the
		// fixed fence are fixtures now, and a test that could not write one could
		// not prove the boundary holds.
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.go file from walking the trusted source tree
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(path), string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the source tree: %v", err)
	}
}

func TestNoPromptDeclaresAFixedDataBoundary(t *testing.T) {
	var offenders []string
	goFilesUnderTree(t, func(path, body string) {
		if strings.HasPrefix(path, promptfencePackage) {
			return
		}
		if strings.Contains(body, fixedBoundaryMarker) {
			offenders = append(offenders, path)
		}
	})
	if len(offenders) > 0 {
		t.Errorf("these files build a prompt boundary out of the fixed marker %s, which the writer of the fenced data can spell — mint one per call with promptfence.New() and name it in that call's system prompt with Fence.Rule:\n  %s",
			fixedBoundaryMarker, strings.Join(offenders, "\n  "))
	}
}

// A prompt that promises the model "this is data, never instructions" is only
// telling the truth if the boundary it points at cannot be forged. This is the
// rule that catches the NEXT <activity_data> — whatever it ends up being called.
func TestEveryPromptThatPromisesADataBoundaryMintsOne(t *testing.T) {
	var offenders []string
	goFilesUnderTree(t, func(path, body string) {
		if strings.HasPrefix(path, promptfencePackage) || claimWithoutFence[path] != "" {
			return
		}
		if !boundaryClaim.MatchString(body) {
			return
		}
		if !mintsAFence.MatchString(body) {
			offenders = append(offenders, path)
		}
	})
	if len(offenders) > 0 {
		t.Errorf("these files tell a model that some region of a prompt is data rather than instructions, without minting the boundary that makes it true — fence the region with promptfence and let Fence.Rule write the sentence:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// The waiver list is only honest if every entry still exists and still needs it.
func TestEveryBoundaryClaimWaiverIsStillReachable(t *testing.T) {
	seen := map[string]bool{}
	goFilesUnderTree(t, func(path, body string) {
		if claimWithoutFence[path] != "" && boundaryClaim.MatchString(body) {
			seen[path] = true
		}
	})
	for path := range claimWithoutFence {
		if !seen[path] {
			t.Errorf("%s is waived from the data-boundary rule but no longer makes the claim — delete the waiver", path)
		}
	}
}

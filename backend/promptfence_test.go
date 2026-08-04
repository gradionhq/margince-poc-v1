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
	"bytes"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
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

// buildsAFence matches a call that produces a boundary, qualified by the package
// so only this package's constructors count. An earlier spelling also accepted a
// bare ".Rule(", which any unrelated method of that name satisfies — the matcher
// has to prove a fence exists, not that some identifier looked familiar.
var buildsAFence = regexp.MustCompile(`promptfence\.(New|FromMarker)\(`)

// claimWithoutFence names the files allowed to promise a boundary without
// minting one, with the reason. Keep this at zero if you can; every entry is a
// prompt whose safety rests on something other than a nonce.
var claimWithoutFence = gatekit.Waive(map[string]string{})

// sourceWithoutComments renders one file's CODE, comments discarded.
//
// Both rules below scan source, and a comment is neither a prompt nor a fence:
// prose describing the old marker would trip the first rule, and prose
// mentioning a constructor would satisfy the second. Dropping comments makes the
// claim rule fire only on text that actually reaches a model, and makes the
// fence rule provable rather than suggestive.
func sourceWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// A Go file the tree cannot parse is a real defect, not a file to skip.
		t.Fatalf("parsing %s: %v", path, err)
	}
	var out bytes.Buffer
	if err := printer.Fprint(&out, fset, file); err != nil {
		t.Fatalf("printing %s: %v", path, err)
	}
	return out.String()
}

// goFilesUnderTree yields every non-test Go file in the tree, with its code.
func goFilesUnderTree(t *testing.T, visit func(path, code string)) {
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
		visit(filepath.ToSlash(path), sourceWithoutComments(t, path))
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
// telling the truth if the boundary it points at cannot be forged. This is what
// catches the NEXT <activity_data>, whatever it ends up being called.
//
// Scope, stated plainly because the name would otherwise promise more: the check
// is per FILE, not per prompt. A file that makes the claim and builds a fence
// somewhere passes, so a second builder in that same file could still promise a
// boundary it never mints. Closing that needs the claim and the fence located in
// the same prompt via the AST, which is worth doing and is recorded in STATUS.md
// alongside the corpus pin gate. What this does catch is a whole file — a whole
// lane — making the promise with no nonce behind it anywhere, which is the shape
// every instance found so far has taken.
func TestAFileThatPromisesADataBoundaryBuildsOne(t *testing.T) {
	var offenders []string
	goFilesUnderTree(t, func(path, body string) {
		if strings.HasPrefix(path, promptfencePackage) {
			return
		}
		if !boundaryClaim.MatchString(body) {
			return
		}
		if buildsAFence.MatchString(body) {
			return
		}
		if claimWithoutFence.Waived(t, path) {
			return
		}
		offenders = append(offenders, path)
	})
	if len(offenders) > 0 {
		t.Errorf("these files tell a model that some region of a prompt is data rather than instructions, without minting the boundary that makes it true — fence the region with promptfence and let Fence.Rule write the sentence:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// An entry naming a file that no longer claims a data boundary reads stale
// here: this is the sweep that reaches every file in the tree unconditionally,
// so it is the one that owns AssertAllMatched. It holds that each entry still
// names a claim, not that the claim still needs waiving — a file that now
// mints its fence as well still matches, and stays matched.
func TestEveryBoundaryClaimWaiverIsStillReachable(t *testing.T) {
	defer claimWithoutFence.AssertAllMatched(t)
	goFilesUnderTree(t, func(path, body string) {
		if boundaryClaim.MatchString(body) {
			claimWithoutFence.Waived(t, path)
		}
	})
}

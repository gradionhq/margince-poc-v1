// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// One Go version, pinned in several places, and they have to agree.
//
// The pins exist for different readers: go.mod is what the compiler and CI
// resolve (every workflow job uses `go-version-file: backend/go.mod`),
// .tool-versions is what a developer's asdf/mise shell installs, and the
// extension how-to is what somebody copies when they start a new unit. Nothing
// made them move together, so a security bump updated the modules and left the
// developer pin a patch release behind — which is how a machine keeps building
// with the vulnerable toolchain the bump was for.
//
// This derives the answer from backend/go.mod rather than holding a list: the
// product module is the pin CI actually reads, so it is the one that decides.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// goDirective matches the `go 1.26.6` line a module or workspace file carries.
var goDirective = regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`)

func TestEveryGoVersionPinMatchesTheProductModule(t *testing.T) {
	want := goVersionOf(t, "go.mod")
	if strings.Count(want, ".") != 2 {
		t.Fatalf("backend/go.mod pins %q; a patch version is what the other pins have to match", want)
	}

	// Each of these is read by somebody the others are not: the workspace by
	// every bare go command, .tool-versions by a developer's shell, the
	// how-to by whoever writes the next extension.
	t.Run("the workspace file", func(t *testing.T) {
		if got := goVersionOf(t, "../go.work"); got != want {
			t.Errorf("go.work pins go %s, backend/go.mod pins %s", got, want)
		}
	})

	// Derived from the tree, not listed. The list this replaced named five
	// modules of the fifteen that exist, and the two it happened not to name had
	// already drifted a patch release behind — which is the same failure the
	// whole test exists to catch, reintroduced by the shape of the check. A list
	// also cannot cover a module written after it.
	for _, module := range everyModuleFile(t) {
		t.Run(module, func(t *testing.T) {
			if got := goVersionOf(t, module); got != want {
				t.Errorf("%s pins go %s, backend/go.mod pins %s", module, got, want)
			}
		})
	}

	t.Run("the developer toolchain pin", func(t *testing.T) {
		pinned := readFile(t, "../.tool-versions")
		if !strings.Contains(pinned, "golang "+want+"\n") {
			t.Errorf("`.tool-versions` does not pin golang %s:\n%s\n\n"+
				"A developer's shell installs what this file says, so a stale pin here "+
				"keeps building with the toolchain the bump replaced.", want, pinned)
		}
	})

	// The how-to is a template somebody copies verbatim into a new module, so a
	// stale version there is a stale pin in every extension written from it.
	t.Run("the extension how-to", func(t *testing.T) {
		doc := readFile(t, "../docs/how-to/add-an-extension.md")
		if strings.Contains(doc, "go 1.") && !strings.Contains(doc, "go "+want) {
			t.Errorf("docs/how-to/add-an-extension.md does not show go %s; "+
				"the template is copied into new modules verbatim", want)
		}
	})
}

// moduleRoots are the trees a hand-written go.mod lives under. They are named
// the way license_test.go names its roots, and for the same reason: a walk from
// the repository root would also read the GENERATED module under build/, and any
// unrelated checkout nested inside the working tree, neither of whose pins this
// test has any claim on.
var moduleRoots = []string{".", "../cli", "../composition", "../desktop", "../extensions", "../fixtures"}

// everyModuleFile collects every go.mod under those roots, so a module added
// tomorrow is held to the product module's pin on the day it lands.
func everyModuleFile(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, root := range moduleRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// An extension unit may carry a frontend, and walking its installed
			// dependencies is thousands of directories of pure cost.
			if entry.IsDir() && entry.Name() == "node_modules" {
				return fs.SkipDir
			}
			if !entry.IsDir() && entry.Name() == "go.mod" {
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s for go.mod files: %v", root, err)
		}
	}
	// The product module is the pin every other one is compared against, so its
	// own file must be in what the walk found. Without this a mistyped root
	// reports success for having read nothing, which is the one way a derived
	// check fails worse than the list it replaced.
	if !slices.Contains(found, "go.mod") {
		t.Fatalf("the walk over %v did not find backend/go.mod, so it is not reading this tree; found %v",
			moduleRoots, found)
	}
	return found
}

func goVersionOf(t *testing.T, path string) string {
	t.Helper()
	match := goDirective.FindStringSubmatch(readFile(t, path))
	if match == nil {
		t.Fatalf("%s carries no `go <version>` directive", path)
	}
	return match[1]
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

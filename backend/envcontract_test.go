// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Environment-variable contract fitness functions. Three obligations, all
// derived from the tree rather than a maintained list:
//
//  1. every MARGINCE_* var the Go code reads is named in
//     docs/reference/configuration.md — the table of record for the binaries;
//  2. every MARGINCE_* var .env.template names is still part of the product;
//  3. every MARGINCE_* var configuration.md names is still part of the product.
//
// Unstated, all three files drift apart: the template advertises credentials
// for a process role that no longer exists, a secret an operator must
// provision goes documented nowhere, and the table of record keeps a row for a
// var nothing reads. Each failure is invisible until someone fills in a blank
// and waits for an effect that can never arrive.
//
// What these gates do NOT cover, stated rather than implied:
//
//   - Obligation 1 covers Go readers, in the licensed trees only. A var read
//     solely by deploy shell or a workflow is documented in docs/deployment.md
//     by hand, and cli/craft is a module of its own that no sweep here walks.
//     Obligations 2 and 3 do count those non-Go readers, because there the
//     question is merely whether a name is still real.
//   - MARGINCE_-prefixed names only. The BYOK keys (GEMINI_API_KEY,
//     OPENAI_API_KEY, ANTHROPIC_API_KEY, OPENAI_COMPATIBLE_API_KEY) carry
//     provider-conventional names and are ungated. Widening the pattern to any
//     env-shaped literal would match unrelated constants and trade a sharp
//     gate for a noisy one.
//   - Whole literals only. A name assembled at run time ("MARGINCE_" + provider
//     + "_KEY") is invisible to every obligation here. None exists today, which
//     is when the caveat is cheap to write down.
//   - Presence, not truth. A var being NAMED in a document does not make the
//     prose around it accurate, and .env.template now carries denser
//     behavioural claims than the reference doc does. These gates stop a var
//     disappearing; they cannot stop a description going stale.

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// envVarName matches one MARGINCE_* name in prose — the reference doc and the
// template, where names appear unquoted. Applied to those files it is why
// .env.template may not write a `MARGINCE_FOO_*` glob: the glob yields the name
// `MARGINCE_FOO_`, which nothing reads.
var envVarName = regexp.MustCompile(`MARGINCE_[A-Z0-9_]+`)

// quotedEnvVarName matches one MARGINCE_* name as a Go string literal. Keying
// on the literal rather than on os.Getenv is what makes the Go sweep complete:
// the vars are also read through four wrapper helpers in cmd/api and cmd/worker
// (envOr, envIntOr, envDuration, envDurationOr). Keying on unquoted text
// instead would harvest globs out of comments and invent vars that do not exist.
var quotedEnvVarName = regexp.MustCompile(`"(MARGINCE_[A-Z0-9_]+)"`)

const (
	configurationDoc = "../docs/reference/configuration.md"
	envTemplate      = "../.env.template"
)

// deploySurfaceRoots are the non-Go trees that configure a deployment: the
// entrypoints and helper scripts, the compose/CI definitions, and the images.
// A var read only here (MARGINCE_OWNER_DSN, hard-required by
// scripts/deploy/api-entrypoint.sh, is the live example) is as real as one Go
// reads, so obligations 2 and 3 must accept it — otherwise this file would
// order a developer to delete the very var a container refuses to boot without.
var deploySurfaceRoots = []string{"../scripts", "../infra", "../.github/workflows"}

// walkTextFiles reads every file under root and hands each to visit. The trees
// swept here are small and text-only; a read error fails the test rather than
// narrowing the sweep in silence.
func walkTextFiles(t *testing.T, root string, visit func(path, text string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path) // #nosec G304 G122 -- path comes from walking a fixed root inside the trusted source tree
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(path), string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

// envVarsReadByGoCode maps each MARGINCE_* name a Go string literal spells to
// the first file spelling it. The trees are the ones the license sweep covers
// (licensedTrees, license_test.go): extensions/ and fixtures/ are separate
// modules a `./...` from here never reaches, and a var a unit reads is as real
// as one the backend reads. Sharing that list means a new tree enrolls in both
// gates at once instead of silently in only one.
func envVarsReadByGoCode(t *testing.T) map[string]string {
	t.Helper()
	sites := map[string]string{}
	for _, tree := range licensedTrees {
		walkTextFiles(t, tree.root, func(path, text string) {
			if !strings.HasSuffix(path, ".go") || isGenerated(path, text) {
				return
			}
			for _, m := range quotedEnvVarName.FindAllStringSubmatch(text, -1) {
				if _, seen := sites[m[1]]; !seen {
					sites[m[1]] = path
				}
			}
		})
	}
	if len(sites) == 0 {
		t.Fatal("no MARGINCE_* env var found in any Go tree — a sweep that scans nothing passes exactly like a clean one")
	}
	return sites
}

// liveEnvVars is every MARGINCE_* name the product still mentions anywhere it
// configures itself: Go literals plus the deploy surface. Deliberately
// over-inclusive — a script that SETS a var for a child process counts the same
// as one that reads it. That costs nothing here, because this set only ever
// permits a name to appear in a document; requiring documentation is obligation
// 1's job, and that one stays Go-only precisely because it cannot afford the
// same fuzziness.
func liveEnvVars(t *testing.T) map[string]string {
	t.Helper()
	live := envVarsReadByGoCode(t)
	for _, root := range deploySurfaceRoots {
		walkTextFiles(t, root, func(path, text string) {
			for _, name := range envVarName.FindAllString(text, -1) {
				if _, seen := live[name]; !seen {
					live[name] = path
				}
			}
		})
	}
	images, err := filepath.Glob("../Dockerfile*")
	if err != nil {
		t.Fatalf("globbing Dockerfiles: %v", err)
	}
	for _, path := range images {
		b, err := os.ReadFile(path) // #nosec G304 -- path comes from a fixed glob in the trusted source tree
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, name := range envVarName.FindAllString(string(b), -1) {
			if _, seen := live[name]; !seen {
				live[name] = filepath.ToSlash(path)
			}
		}
	}
	return live
}

// namesIn returns the set of MARGINCE_* names a document spells out. Matching
// whole names rather than searching for substrings is what makes the gates
// exact in both directions: a doc mentioning only MARGINCE_AICERT_MODEL must
// not be read as covering the separate MARGINCE_AICERT, and an abbreviation
// like `MARGINCE_GMAIL_CLIENT_ID` / `…_SECRET` covers only the name it actually
// writes out. A variable name a reader cannot grep for is not documented.
func namesIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- fixed path in the trusted source tree
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	names := map[string]bool{}
	for _, name := range envVarName.FindAllString(string(b), -1) {
		names[name] = true
	}
	if len(names) == 0 {
		t.Fatalf("%s names no MARGINCE_* var at all — a file that documents nothing cannot be the table of record", path)
	}
	return names
}

// TestEveryEnvVarIsDocumented: shipping a var the reference doc has never heard
// of leaves an operator guessing, and a secret nobody wrote down cannot be
// provisioned at all.
func TestEveryEnvVarIsDocumented(t *testing.T) {
	documented := namesIn(t, configurationDoc)
	sites := envVarsReadByGoCode(t)

	var undocumented []string
	for _, name := range slices.Sorted(maps.Keys(sites)) {
		if !documented[name] {
			undocumented = append(undocumented, name+" (read in "+sites[name]+")")
		}
	}
	if len(undocumented) > 0 {
		t.Errorf("%d env var(s) read by Go code but not named in %s — add a row there, spelling the name out in full (an abbreviated suffix like `…_SECRET` does not count):\n\t%s",
			len(undocumented), configurationDoc, strings.Join(undocumented, "\n\t"))
	}
}

// TestEnvTemplateNamesOnlyLiveVars: a template entry for a var nothing reads is
// worse than absent — someone fills it in and waits for an effect that can
// never arrive.
func TestEnvTemplateNamesOnlyLiveVars(t *testing.T) {
	assertNamesOnlyLiveVars(t, envTemplate)
}

// TestConfigurationDocNamesOnlyLiveVars holds the table of record to the same
// bar as the template it now absorbs. Without it the more authoritative file is
// the one free to keep a row for a var that no longer exists.
func TestConfigurationDocNamesOnlyLiveVars(t *testing.T) {
	assertNamesOnlyLiveVars(t, configurationDoc)
}

func assertNamesOnlyLiveVars(t *testing.T, path string) {
	t.Helper()
	live := liveEnvVars(t)

	var dead []string
	for name := range namesIn(t, path) {
		if _, ok := live[name]; !ok {
			dead = append(dead, name)
		}
	}
	slices.Sort(dead)
	if len(dead) > 0 {
		t.Errorf("%d var(s) named in %s that nothing in the product reads — delete them. A `MARGINCE_FOO_*` glob counts as the name `MARGINCE_FOO_`, which nothing reads: spell such vars out instead of globbing them:\n\t%s",
			len(dead), path, strings.Join(dead, "\n\t"))
	}
}

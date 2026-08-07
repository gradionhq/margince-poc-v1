// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Environment-variable contract fitness functions. Two obligations, both
// derived from the tree rather than a maintained list:
//
//  1. every MARGINCE_* var the Go code reads is named in
//     docs/reference/configuration.md — the table of record;
//  2. every MARGINCE_* var .env.template names is still read by Go code.
//
// Without these, both files drift freely and did: the template accumulated
// vars for a retired process role while a dozen live vars — including two
// secrets an operator must provision — were documented nowhere.
//
// What these gates do NOT cover, stated rather than implied:
//
//   - Go readers only. MARGINCE_OWNER_DSN and MARGINCE_ADMIN_PASSWORD[_FILE]
//     are read by deploy shell and compose, so nothing here constrains them.
//   - MARGINCE_-prefixed names only. The BYOK keys (GEMINI_API_KEY,
//     OPENAI_API_KEY, ANTHROPIC_API_KEY, OPENAI_COMPATIBLE_API_KEY) carry
//     provider-conventional names and are ungated. Widening the pattern to any
//     env-shaped literal would match unrelated constants and trade a sharp
//     gate for a noisy one.
//   - Presence, not truth. A var being NAMED in configuration.md does not make
//     the prose around it accurate. These gates stop a var disappearing from
//     the docs, not a description going stale; no test can gate prose.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// envVarName matches one MARGINCE_* name. Applied to Go sources it is
// deliberately anchored on the surrounding double quotes: the vars are read
// through four wrapper helpers as well as os.Getenv (envOr, envIntOr,
// envDuration, envDurationOr in cmd/api and cmd/worker), so keying on the call
// would miss them — while keying on unquoted text would harvest `MARGINCE_X_*`
// globs out of comments and invent vars that do not exist.
var envVarName = regexp.MustCompile(`MARGINCE_[A-Z0-9_]+`)

var quotedEnvVarName = regexp.MustCompile(`"(MARGINCE_[A-Z0-9_]+)"`)

const (
	configurationDoc = "../docs/reference/configuration.md"
	envTemplate      = "../.env.template"
)

// envVarsReadByGoCode collects every MARGINCE_* name appearing as a string
// literal in hand-written Go across the same trees the license sweep covers
// (licensedTrees, license_test.go): extensions/ and fixtures/ are separate
// modules a `./...` from here never reaches, and a var a unit reads is as real
// as one the backend reads. Sharing that list means a new tree enrolls in both
// gates at once instead of silently in only one.
func envVarsReadByGoCode(t *testing.T) map[string][]string {
	t.Helper()
	sites := map[string][]string{}
	for _, tree := range licensedTrees {
		err := filepath.WalkDir(tree.root, func(path string, d fs.DirEntry, err error) error {
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
			for _, m := range quotedEnvVarName.FindAllStringSubmatch(text, -1) {
				sites[m[1]] = append(sites[m[1]], filepath.ToSlash(path))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", tree.root, err)
		}
	}
	if len(sites) == 0 {
		t.Fatal("no MARGINCE_* env var found in any Go tree — a sweep that scans nothing passes exactly like a clean one")
	}
	return sites
}

// namesIn reads a documentation or template file and returns the set of
// MARGINCE_* names it spells out. Matching whole names rather than searching
// for substrings is what makes the gate exact in both directions: a doc that
// mentions only MARGINCE_AICERT_MODEL must not be read as covering the
// separate MARGINCE_AICERT, and an abbreviation like `MARGINCE_GMAIL_CLIENT_ID`
// / `…_SECRET` covers only the name it actually writes out. A variable name a
// reader cannot grep for is a variable name that is not documented.
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

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryEnvVarIsDocumented: shipping a var the reference doc has never
// heard of leaves an operator guessing, and a secret nobody wrote down cannot
// be provisioned at all.
func TestEveryEnvVarIsDocumented(t *testing.T) {
	documented := namesIn(t, configurationDoc)
	sites := envVarsReadByGoCode(t)

	var undocumented []string
	for _, name := range sortedKeys(sites) {
		if !documented[name] {
			undocumented = append(undocumented, name+" (read in "+sites[name][0]+")")
		}
	}
	if len(undocumented) > 0 {
		t.Errorf("%d env var(s) read by Go code but not named in %s — add a row there (spell the name out in full; an abbreviated suffix does not count):\n\t%s",
			len(undocumented), configurationDoc, strings.Join(undocumented, "\n\t"))
	}
}

// TestEnvTemplateNamesOnlyLiveVars: a template entry for a var no code reads
// is worse than absent — someone fills it in and waits for an effect that can
// never arrive. This is how the template kept advertising a retired process
// role's credentials.
//
// It also means .env.template must not write `MARGINCE_FOO_*` globs: the glob
// yields the name `MARGINCE_FOO_`, which no code reads. Spell the vars out —
// the template is meant to be copied, and a glob cannot be.
func TestEnvTemplateNamesOnlyLiveVars(t *testing.T) {
	sites := envVarsReadByGoCode(t)

	var dead []string
	for name := range namesIn(t, envTemplate) {
		if sites[name] == nil {
			dead = append(dead, name)
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("%d var(s) named in %s that no Go code reads — delete them (or spell out a glob):\n\t%s",
			len(dead), envTemplate, strings.Join(dead, "\n\t"))
	}
}

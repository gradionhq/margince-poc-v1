// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Contract $ref pre-flight as a fitness function. It resolves every local
// $ref in api/crm.yaml and
// fails on a dangling pointer with a precise message. This catches the
// dangling-ref class of bug — a typo'd component name like `ProblemDetail`
// where the schema is `Problem` — with a readable error, instead of the
// cryptic codegen abort the same typo produces downstream in `gen`/`drift`.
//
// Resolution is textual (not a YAML lib) so the root fitness-test package
// stays free of parser deps (the arch-lint boundary). Every ref in this
// contract targets #/components/{parameters,responses,schemas}/<Name>; a ref
// of any other shape is reported rather than silently skipped.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var (
	refPointer   = regexp.MustCompile(`\$ref:\s*["']?(#/[^"'\s}]+)`)
	compSection  = regexp.MustCompile(`^  (parameters|responses|schemas):[ \t]*$`)
	compName     = regexp.MustCompile(`^    ([A-Za-z0-9_.-]+):`)
	twoSpaceKey  = regexp.MustCompile(`^  [A-Za-z0-9_]`)
	knownSection = map[string]bool{"parameters": true, "responses": true, "schemas": true}
)

func TestContractRefsResolve(t *testing.T) {
	const path = "api/crm.yaml"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	lines := strings.Split(string(raw), "\n")

	// defined[section] = the component names declared under components.<section>.
	// Names sit at 4-space indent; a 2-space key or an un-indented line closes
	// the current section.
	defined := map[string]map[string]bool{
		"parameters": {}, "responses": {}, "schemas": {},
	}
	section := ""
	for _, line := range lines {
		switch {
		case compSection.MatchString(line):
			section = compSection.FindStringSubmatch(line)[1]
		case twoSpaceKey.MatchString(line): // a different 2-space subsection
			section = ""
		case len(line) > 0 && line[0] != ' ': // left components entirely
			section = ""
		case section != "":
			if m := compName.FindStringSubmatch(line); m != nil {
				defined[section][m[1]] = true
			}
		}
	}

	// Every #/... pointer must resolve to a declared component.
	dangling := 0
	total := 0
	for i, line := range lines {
		for _, m := range refPointer.FindAllStringSubmatch(line, -1) {
			total++
			ptr := m[1]
			parts := strings.Split(strings.TrimPrefix(ptr, "#/"), "/")
			ok := len(parts) == 3 && parts[0] == "components" &&
				knownSection[parts[1]] && defined[parts[1]][parts[2]]
			if !ok {
				dangling++
				t.Errorf("%s:%d: dangling or unrecognized $ref %q (no such component — check for a typo'd name)", path, i+1, ptr)
			}
		}
	}
	if dangling == 0 {
		t.Logf("%d $ref(s) resolve cleanly in %s", total, path)
	}
}

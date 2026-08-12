// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The frontend gate is spelled once as `make frontend-check` and run by CI as
// three parallel jobs. That split is a standing invitation to drift: a leg added
// to the local gate that no CI job invokes runs on developer machines and never
// on a pull request, which is a gate that silently checks nothing — and the
// direction with teeth, because the local run is the one somebody skips.
//
// So the obligation is derived rather than listed: whatever `frontend-check`
// reaches, the targets ci.yml actually names must reach too. Adding a leg to
// either side is free; adding one to the local gate alone fails here.

var (
	// A target line: `name: prereq prereq`, excluding `::=` style assignments.
	makeTargetLine = regexp.MustCompile(`^([a-z][a-z0-9-]*(?: [a-z][a-z0-9-]*)*):(?:[ \t]+(.*))?$`)
	// A recipe line delegating to another target: `\t$(MAKE) name`.
	makeDelegation = regexp.MustCompile(`^\t[ \t]*(?:@)?\$\(MAKE\)[ \t]+([a-z][a-z0-9-]*)`)
	// What ci.yml runs: `run: make <target>` — the first word after `make`.
	ciMakeInvocation = regexp.MustCompile(`run:[ \t]+make[ \t]+([a-z][a-z0-9-]*)`)
)

// parseMakefile maps each target to the targets it pulls in, by prerequisite or
// by an explicit sub-make in its recipe.
func parseMakefile(t *testing.T, path string) map[string][]string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	edges := map[string][]string{}
	var current []string
	for _, line := range strings.Split(string(body), "\n") {
		if leg := makeDelegation.FindStringSubmatch(line); leg != nil {
			for _, target := range current {
				edges[target] = append(edges[target], leg[1])
			}
			continue
		}
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue
		}
		match := makeTargetLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		current = strings.Fields(match[1])
		for _, target := range current {
			edges[target] = append(edges[target], strings.Fields(match[2])...)
		}
	}
	if len(edges) == 0 {
		t.Fatalf("parsed no targets from %s — the Makefile changed shape and this gate was about to compare nothing", path)
	}
	return edges
}

// reachable walks the edges from roots, returning every target reached.
func reachable(edges map[string][]string, roots ...string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(target string) {
		if seen[target] {
			return
		}
		seen[target] = true
		for _, next := range edges[target] {
			walk(next)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return seen
}

// ciMakeTargets is every target ci.yml invokes whose name starts with the
// frontend prefix — derived from the workflow so a renamed job cannot strand it.
func ciMakeTargets(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var targets []string
	for _, match := range ciMakeInvocation.FindAllStringSubmatch(string(body), -1) {
		if strings.HasPrefix(match[1], "fe-") {
			targets = append(targets, match[1])
		}
	}
	if len(targets) == 0 {
		t.Fatalf("found no `run: make fe-*` invocation in %s — the frontend jobs were renamed or removed, so this gate compares nothing", path)
	}
	return targets
}

func TestEveryLocalFrontendGateLegRunsInCI(t *testing.T) {
	edges := parseMakefile(t, "../Makefile")
	local := reachable(edges, "frontend-check")
	inCI := reachable(edges, ciMakeTargets(t, "../.github/workflows/ci.yml")...)

	for leg := range local {
		// Only leaves matter: an aggregate that exists solely to group other
		// targets is covered when everything under it is.
		if len(edges[leg]) > 0 || inCI[leg] {
			continue
		}
		t.Errorf("`make frontend-check` runs %q but no ci.yml job reaches it — the leg would run locally and never on a pull request; add it to fe-quality, fe-unit or fe-bundle", leg)
	}
}

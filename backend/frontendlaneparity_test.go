// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The frontend gate is spelled once as `make check-fe` and run by CI as three
// parallel jobs. That split is a standing invitation to drift: a leg added to
// the local gate that no CI job invokes runs on developer machines and never on
// a pull request, which is a gate that silently checks nothing — and the
// direction with teeth, because the local run is the one somebody skips.
//
// So the obligation is derived from both artifacts rather than listed. A LEG is
// any target whose recipe does work of its own, as opposed to a target that only
// groups others; every leg the local gate reaches must also be reached by a job
// the required `frontend` check depends on. Both halves matter, and both are
// mistakes this gate has already had to be taught:
//
//   - Work is found by RECIPE, not by target name. Until this lane was split,
//     `frontend-check` was a list of raw script lines, so "add my new check to
//     frontend-check" is the next edit a reasonable engineer makes — and a model
//     that only followed `$(MAKE)` edges could not see it at all.
//   - CI coverage is read from the JOB GRAPH, not from the file's text. A
//     `run: make …` line inside a commented-out step, or inside a job the
//     `frontend` fan-in does not depend on, is not a gate that runs.

const (
	// The local gate, and the root of the obligation. `check-fe` rather than
	// `frontend-check` on purpose: it is what `make check` runs, and it adds the
	// composed typecheck and the unit screens' suites, which are gate legs too.
	localGateRoot = "check-fe"
	// The fan-in whose name is the required status check. Only jobs it depends
	// on can block a merge, so only those count as CI coverage.
	requiredFanInJob = "frontend"
)

// makeTarget is one Makefile rule: what it pulls in, and whether it does
// anything itself.
type makeTarget struct {
	pulls []string // prerequisites plus explicit sub-make delegations
	works bool     // has a recipe line that is not purely a local delegation
}

// A rule line: `name: prereq prereq`. Assignments (`X ?= y`, `X := y`) do not
// match, because ?/: are excluded from the name.
var makeRuleLine = regexp.MustCompile(`^([a-z][a-z0-9-]*(?: [a-z][a-z0-9-]*)*):(?:[ \t]+(.*))?$`)

// parseRecipe reads one recipe line, returning the local targets it delegates
// to and whether the line counts as work of its own.
func parseRecipe(t *testing.T, line string) (targets []string, works bool) {
	t.Helper()
	body := strings.TrimLeft(strings.TrimPrefix(line, "\t"), " \t@+-")
	rest, isSubMake := strings.CutPrefix(body, "$(MAKE)")
	if !isSubMake {
		return nil, true // an ordinary command: this target does work
	}
	for _, token := range strings.Fields(rest) {
		switch {
		case token == "-C":
			// A sub-make into another directory (backend/). It does real work
			// and adds no edge inside THIS Makefile's graph.
			return nil, true
		case strings.HasPrefix(token, "-"):
			continue // a flag such as --no-print-directory
		case strings.ContainsAny(token, "$()"):
			t.Fatalf("unrecognised sub-make %q — this gate reads $(MAKE) lines to find the legs, so a spelling it cannot parse would silently drop one", strings.TrimSpace(line))
		default:
			targets = append(targets, token)
		}
	}
	return targets, len(targets) == 0
}

// parseMakefile maps every target to what it pulls in and whether it works.
func parseMakefile(t *testing.T, path string) map[string]*makeTarget {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	targets := map[string]*makeTarget{}
	var current []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "\t") {
			delegated, works := parseRecipe(t, line)
			for _, name := range current {
				targets[name].pulls = append(targets[name].pulls, delegated...)
				targets[name].works = targets[name].works || works
			}
			continue
		}
		match := makeRuleLine.FindStringSubmatch(line)
		if strings.HasPrefix(line, "#") || match == nil {
			continue
		}
		current = strings.Fields(match[1])
		for _, name := range current {
			if targets[name] == nil {
				targets[name] = &makeTarget{}
			}
			targets[name].pulls = append(targets[name].pulls, strings.Fields(match[2])...)
		}
	}
	if targets[localGateRoot] == nil {
		t.Fatalf("%s declares no %q target — the local gate was renamed, so this gate was about to compare nothing", path, localGateRoot)
	}
	return targets
}

// reachable walks from roots over prerequisites and delegations alike.
func reachable(targets map[string]*makeTarget, roots ...string) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		if target := targets[name]; target != nil {
			for _, next := range target.pulls {
				walk(next)
			}
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return seen
}

// stringList accepts both YAML spellings of `needs:` — one job, or a list.
type stringList []string

func (s *stringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var one string
		if err := node.Decode(&one); err != nil {
			return err
		}
		*s = stringList{one}
		return nil
	}
	var many []string
	if err := node.Decode(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

type workflow struct {
	Jobs map[string]struct {
		Needs stringList `yaml:"needs"`
		Steps []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// ciGateTargets is every make target invoked by a job the required fan-in
// depends on. Parsed as YAML rather than grepped, so a commented-out step or a
// job outside the fan-in's `needs` cannot pass for coverage.
// gateWorkflowDeclaring finds the merge-gate workflow that declares `job`.
//
// The fan-in used to live in ci.yml; it now lives in the frontend lane that
// ci.yml calls. Searching the workflow directory for whichever file declares the
// job means this gate follows the next move too, instead of failing with
// "the lane's work moved out from behind the required check" when the work is
// exactly where it should be — one file over.
func gateWorkflowDeclaring(t *testing.T, job string) string {
	t.Helper()
	paths, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil {
		t.Fatalf("listing workflows: %v", err)
	}
	var found []string
	for _, path := range paths {
		body, err := os.ReadFile(path) // #nosec G304 -- a repo-relative path from the glob above
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var parsed workflow
		if err := yaml.Unmarshal(body, &parsed); err != nil {
			// A workflow this gate cannot parse is not necessarily the one it
			// wants; the assertion below fails if none of them is.
			continue
		}
		// The fan-in is the job that DOES the asserting, so it has steps. The
		// caller carries a job of the same name that only delegates (`uses:`),
		// with no steps of its own — matching that one would walk the caller's
		// `needs` (the classifier) instead of the lane's three legs.
		declared, ok := parsed.Jobs[job]
		if ok && len(declared.Needs) > 0 && len(declared.Steps) > 0 {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no workflow under ../.github/workflows declares a %q job with needs — the required check was renamed or retired, so nothing here gates the lane", job)
	default:
		t.Fatalf("%d workflows declare a %q fan-in (%s) — two definitions of one required check drift, and this gate cannot tell which one runs", len(found), job, strings.Join(found, ", "))
	}
	return ""
}

func ciGateTargets(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path) // #nosec G304 -- a repo-relative workflow path
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var parsed workflow
	if err := yaml.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	fanIn, ok := parsed.Jobs[requiredFanInJob]
	if !ok {
		t.Fatalf("%s declares no %q job — the required check was renamed, so nothing here gates the lane", path, requiredFanInJob)
	}
	invoked := regexp.MustCompile(`(?m)^\s*make\s+([a-z][a-z0-9-]*)`)
	var targets []string
	for _, dependency := range fanIn.Needs {
		for _, step := range parsed.Jobs[dependency].Steps {
			for _, match := range invoked.FindAllStringSubmatch(step.Run, -1) {
				targets = append(targets, match[1])
			}
		}
	}
	if len(targets) == 0 {
		t.Fatalf("no job the %q fan-in needs runs a make target in %s — the lane's work moved out from behind the required check", requiredFanInJob, path)
	}
	return targets
}

func TestEveryLocalFrontendGateLegRunsInCI(t *testing.T) {
	targets := parseMakefile(t, "../Makefile")
	local := reachable(targets, localGateRoot)
	if len(local) < 2 {
		t.Fatalf("%q reaches no other target — the local gate lost its legs, or this gate stopped being able to see them", localGateRoot)
	}
	ciRoots := ciGateTargets(t, gateWorkflowDeclaring(t, requiredFanInJob))
	inCI := reachable(targets, ciRoots...)

	for leg := range local {
		// The root is the gate, not a leg of it: CI runs the jobs below it.
		if leg == localGateRoot || !targets[leg].works || inCI[leg] {
			continue
		}
		t.Errorf("`make %s` runs %q but no job behind the %q check reaches it — the leg would run locally and never on a pull request; add it to one of: %s",
			localGateRoot, leg, requiredFanInJob, strings.Join(ciRoots, ", "))
	}
}

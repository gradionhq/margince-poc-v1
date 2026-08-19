// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A contract change owes three regenerations, and the one that strands the
// FRONTEND types is enforced in two different places for two different readers.
//
// Locally it is scripts/check-contract-frontend-drift.sh, wired into
// check-backend, because a backend-only author has no reason to run the lane
// that would otherwise catch it — that is #1639.
//
// On a pull request it is CI's fe-quality job, which runs `make fe-drift`. That
// only holds while the change classifier still ROUTES a contract change to the
// frontend lane: `backend/api/**` sits in the `frontend` filter for exactly this
// reason, and a PR touching only the contract is classified frontend-affecting
// because of that one line. Delete it and the frontend jobs skip, fe-drift never
// runs, and a stranded schema reaches main — which is #1573, the outage this
// whole family is about.
//
// Nothing else notices that deletion. The classifier is a data file, the jobs it
// gates report "skipped" rather than "failed", and a skipped job reads green on
// the pull request. So the routing is asserted here, from the workflow itself.
//
// This is the CI half of the same obligation, and it is deliberately NOT spelled
// as "the backend lane must have pnpm". It must not: deterministic-gates installs
// Go and nothing else, and the local leg skips there — loudly — precisely because
// this test covers the pull-request path instead.

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	// ciWorkflowForContractLane is the merge gate's workflow, where the change
	// classifier and the jobs it gates both live.
	ciWorkflowForContractLane = "../.github/workflows/ci.yml"

	// contractPathspec is the classifier entry that routes a contract change to
	// the frontend lane. Spelled as it appears in the filter, because it is the
	// string dorny/paths-filter matches with.
	contractPathspec = "backend/api/**"
)

// theFrontendFilter is the classifier key whose result gates the frontend jobs.
const theFrontendFilter = "frontend"

// TestTheContractReachesTheFrontendLane fails when a contract change would no
// longer run the CI job that regenerates and diffs the frontend schema.
func TestTheContractReachesTheFrontendLane(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflowForContractLane)
	if err != nil {
		t.Fatalf("reading %s: %v", ciWorkflowForContractLane, err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parsing %s: %v", ciWorkflowForContractLane, err)
	}

	filters := ""
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if f, ok := step.With["filters"]; ok && f != "" {
				filters = f
			}
		}
	}
	if filters == "" {
		t.Fatalf("%s declares no paths-filter `filters:` block — this test reads the routing out of it, and cannot tell a missing block from a correct one", ciWorkflowForContractLane)
	}

	// Decoded as yaml.Node rather than into a []string, because the classifier
	// composes filters out of YAML anchors — `backend` is `[*backend_db, …]`, a
	// sequence whose first element is itself a sequence. A flat decode fails on
	// that, and failing to read the file this test takes its obligation from is
	// indistinguishable from the obligation being met.
	var parsed map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(filters), &parsed); err != nil {
		t.Fatalf("parsing the classifier's filters block: %v", err)
	}
	declared, isDeclared := parsed[theFrontendFilter]
	entries, ok := flattenPathspecs(&declared)
	if !isDeclared {
		ok = false
	}
	if !ok {
		t.Fatalf("the classifier has no %q filter; the frontend jobs are gated on it, so this test cannot confirm a contract change reaches them", theFrontendFilter)
	}
	if len(entries) == 0 {
		t.Fatalf("the classifier's %q filter is empty — every frontend job would skip on every pull request, and a skipped job reads green", theFrontendFilter)
	}
	for _, entry := range entries {
		if entry == contractPathspec {
			return
		}
	}
	t.Fatalf(`the classifier's %q filter no longer lists %q, so a pull request that changes only the contract skips every frontend job.

fe-quality is where CI runs `+"`make fe-drift`"+`, the leg that regenerates
frontend/src/api/schema.d.ts from the contract and fails when the committed copy
drifted. With the routing gone that leg never runs, the jobs report SKIPPED, and
skipped reads green — which is how a stranded schema reached main in #1573.

Filter as declared: %v`, theFrontendFilter, contractPathspec, entries)
}

// flattenPathspecs reads a filter's entries, following the nested sequences a
// YAML anchor produces. Reports false when the node is not a sequence of
// scalars (however nested), so a restructured classifier fails loudly instead of
// matching nothing quietly.
func flattenPathspecs(node *yaml.Node) ([]string, bool) {
	if node.Kind != yaml.SequenceNode {
		return nil, false
	}
	var out []string
	for _, child := range node.Content {
		switch child.Kind {
		case yaml.ScalarNode:
			out = append(out, child.Value)
		case yaml.SequenceNode:
			nested, ok := flattenPathspecs(child)
			if !ok {
				return nil, false
			}
			out = append(out, nested...)
		default:
			return nil, false
		}
	}
	return out, true
}

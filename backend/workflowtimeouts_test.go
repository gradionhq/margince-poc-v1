// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every workflow job carries a wall-clock ceiling.
//
// A job with no `timeout-minutes` inherits GitHub's default of SIX HOURS. That
// is not a bound, it is an outage: a required check that hangs holds the merge
// for a working day, and while it hangs it is indistinguishable from a queue
// backlog — so it is not even read as a failure while it does the damage.
//
// The case that motivated this: a stalled dependency download in the `uat` job
// sat in_progress for 2h20m against a lane that normally finishes in five
// minutes, and had to be cancelled by hand (#1836). Nothing in the product was
// wrong; nothing reported anything.
//
// Derived from the workflow tree rather than a list of job names, so a job
// added later is covered the day it is committed — which is the whole reason
// this is a fitness test and not a one-time edit. It lives beside the other
// gates that read ci.yml (laneconnbudget, frontendlaneparity,
// contractfrontendlane) rather than as a script in a fourth language.

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// workflowDir holds every workflow this repository runs. The list of FILES is
// read from disk for the same reason the job list is: a workflow added later is
// covered without anyone remembering to name it here.
const workflowDir = "../.github/workflows"

// workflowJobs is the shape this gate needs and nothing more — decoding the
// whole GitHub schema would couple the gate to fields it does not judge.
type workflowJobs struct {
	Jobs map[string]struct {
		//nolint:tagliatelle // GitHub names this key, not us.
		TimeoutMinutes int    `yaml:"timeout-minutes"`
		Uses           string `yaml:"uses"`
		Steps          []any  `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestEveryWorkflowJobCarriesATimeoutCeiling(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(workflowDir, "*.yml"))
	if err != nil {
		t.Fatalf("listing workflows: %v", err)
	}
	// A gate that scanned nothing would report exactly like a clean tree, which
	// is the failure mode every derived check here has to close explicitly.
	if len(files) == 0 {
		t.Fatalf("no workflows found under %s; this gate would pass vacuously", workflowDir)
	}

	for _, path := range files {
		raw, err := os.ReadFile(path) // #nosec G304 -- a repo-relative workflow path from the glob above
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var wf workflowJobs
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if len(wf.Jobs) == 0 {
			t.Errorf("%s declares no jobs; either it is not a workflow or this gate cannot see its jobs", filepath.Base(path))
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(wf.Jobs)) {
			job := wf.Jobs[name]
			// A job that only CALLS a reusable workflow cannot carry a timeout
			// of its own; the called workflow's jobs own one, and they are
			// checked on their own pass through this loop.
			if job.Uses != "" && len(job.Steps) == 0 {
				continue
			}
			if job.TimeoutMinutes == 0 {
				t.Errorf("%s: job %q has no timeout-minutes, so it inherits GitHub's six-hour default — "+
					"a hang there holds a required check for a working day while reading as a queue backlog",
					filepath.Base(path), name)
			}
		}
	}
}

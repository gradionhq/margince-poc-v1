// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every job the `ci` aggregate depends on can actually RUN on the merge queue.
//
// `ci` is the single required status check, and scripts/ci-verdict.sh refuses any
// result other than `success` on `merge_group` — because GitHub counts a skipped
// required check as a passing one, which is how a lane that ran nothing reported
// green. The cost of that strictness is a coupling that is easy to break from the
// other side: a job whose `if:` requires `github.event_name == 'pull_request'` is
// SKIPPED on the queue, the aggregate refuses the skip, and every queue entry
// fails. The symptom is not a red test — it is merging stopping repository-wide,
// with the cause several files away from the edit that caused it.
//
// This is a fitness test rather than a fixed list because the mistake has already
// been made twice in one change: `dco` and `license-gate` were both
// pull_request-only, and finding the second by hand after fixing the first is
// exactly the "fixed the case under review, missed the sibling copy" pattern the
// engineering rules name. Deriving the obligation from the workflow means the
// third one fails here instead of in production.
//
// WHAT THIS CATCHES: a job in the aggregate's `needs:` whose condition cannot be
// satisfied on `merge_group`.
//
// WHAT THIS DOES NOT CATCH, deliberately: whether a reachable job is CORRECTLY
// scoped there. Judging that means evaluating GitHub's expression language
// against a synthetic event payload, which is a second implementation of the
// thing under test. The failure this guards is total (nothing merges); the one it
// does not is a design question a reviewer can see.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// aggregateWorkflow is the merge gate, and aggregateJob is the one job in it
// whose `needs:` list defines the required check.
const (
	aggregateWorkflow = workflowDir + "/ci.yml"
	aggregateJob      = "ci"
)

// prOnlyGuard is the conjunct that makes a job unreachable on the queue.
// mergeGroupEscape is what makes such a condition reachable again — either an
// explicit `merge_group` alternative, or the negated spelling
// (`event_name != 'pull_request'`) that every correctly-written lane here uses.
const (
	prOnlyGuard      = "github.event_name == 'pull_request'"
	mergeGroupEscape = "merge_group"
	negatedGuard     = "github.event_name != 'pull_request'"
)

type aggregateJobs struct {
	Jobs map[string]struct {
		Needs []string `yaml:"needs"`
		If    string   `yaml:"if"`
	} `yaml:"jobs"`
}

func TestEveryAggregatedJobCanRunOnTheMergeQueue(t *testing.T) {
	raw, err := os.ReadFile(aggregateWorkflow) // #nosec G304 -- a fixed repo-relative path
	if err != nil {
		t.Fatalf("reading %s: %v", aggregateWorkflow, err)
	}
	var wf aggregateJobs
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("%s: %v", aggregateWorkflow, err)
	}

	agg, ok := wf.Jobs[aggregateJob]
	if !ok {
		t.Fatalf("%s declares no %q job — either the single required check was renamed, in which case "+
			"branch protection needs the new name, or it was deleted and nothing aggregates the lane",
			filepath.Base(aggregateWorkflow), aggregateJob)
	}
	// A gate that judged zero jobs would pass exactly like a clean tree.
	if len(agg.Needs) == 0 {
		t.Fatalf("job %q lists no needs; this gate would pass vacuously and the required check "+
			"would stand for nothing", aggregateJob)
	}

	for _, name := range slices.Sorted(slices.Values(agg.Needs)) {
		job, ok := wf.Jobs[name]
		if !ok {
			t.Errorf("job %q needs %q, which does not exist in %s — the aggregate would never run",
				aggregateJob, name, filepath.Base(aggregateWorkflow))
			continue
		}
		cond := normalizeCondition(job.If)
		if !strings.Contains(cond, prOnlyGuard) {
			continue
		}
		if strings.Contains(cond, mergeGroupEscape) || strings.Contains(cond, negatedGuard) {
			continue
		}
		t.Errorf("job %q requires %s with no merge_group alternative, so it is SKIPPED on the queue — "+
			"and %q refuses a skip there, which stops every merge. Either admit merge_group "+
			"(`github.event_name != 'pull_request' || ...`, as the other lanes do, taking any PR-only "+
			"payload field from the merge_group event instead), or drop the job from %q's needs.",
			name, prOnlyGuard, aggregateJob, aggregateJob)
	}

	// The aggregate itself must not be conditioned away: a skipped aggregate is a
	// GREEN required check, which is the failure the whole design exists to end.
	if got := normalizeCondition(agg.If); got != "always()" {
		t.Errorf("job %q has if: %q, want %q — an aggregate that is skipped alongside a failed "+
			"upstream job reports a green required check", aggregateJob, got, "always()")
	}

	// Verify the gate is reading real conditions, not empty strings from a schema
	// change. Every lane in this pipeline is conditioned; none being so means the
	// decode silently stopped working.
	if !slices.ContainsFunc(agg.Needs, func(n string) bool { return wf.Jobs[n].If != "" }) {
		t.Errorf("no job in %q's needs carries an `if:` — the decode has stopped seeing conditions, "+
			"so this gate was about to pass without judging anything", aggregateJob)
	}
}

// normalizeCondition collapses the whitespace a YAML folded block (`if: >-`)
// introduces, so a condition means the same thing to this gate however it is
// wrapped in the workflow.
func normalizeCondition(cond string) string {
	return strings.Join(strings.Fields(cond), " ")
}

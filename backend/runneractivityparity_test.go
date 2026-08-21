// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The runner's own status vocabulary must be TOTAL over the column it reads.
//
// A status the runner cannot map emits an empty state, which the projection's
// CHECK then refuses — and a refused write on the consumer's path is not a
// missing line, it is a wedged consumer group that stops the whole rail
// updating for everybody. So the map is held to the CHECK rather than to care.
//
// It lives at the root because a module may not import a sibling to assert this
// about itself, and because the CHECK is in the migrations, which is a third
// place again.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
)

func TestEveryAgentRunStatusHasAProjectionState(t *testing.T) {
	statuses := checkValues(t, "agent_run_status_check")
	if len(statuses) == 0 {
		t.Fatal("derived no agent_run statuses; this gate would pass vacuously")
	}
	for _, status := range statuses {
		state, ok := runner.ProjectionStateFor(status)
		if !ok {
			t.Errorf("agent_run.status %q has no projection state — the runner would announce an empty "+
				"state, the projection's own CHECK would refuse it, and the consumer group would wedge "+
				"rather than drop one line", status)
			continue
		}
		if !slices.Contains(projectionStates(t), state) {
			t.Errorf("agent_run.status %q maps to %q, which ai_task_run.state does not admit", status, state)
		}
	}
}

// projectionStates is the projection's own CHECK set, so the two halves of the
// mapping are both derived and neither is restated here.
func projectionStates(t *testing.T) []string {
	t.Helper()
	return aiTaskRunCheckValues(t, "state")
}

// checkValues pulls one named CHECK constraint's IN list out of the migrations,
// wherever it lives — the baseline squashed core's history into one file, so the
// search is over the whole namespace rather than a filename anyone can guess.
func checkValues(t *testing.T, constraint string) []string {
	t.Helper()
	matches, err := filepath.Glob("migrations/core/*.up.sql")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no core migrations found (err %v)", err)
	}
	re := regexp.MustCompile(regexp.QuoteMeta(constraint) + `[^(]*\(\s*status IN \(([^)]*)\)`)
	for _, path := range matches {
		raw, err := os.ReadFile(path) // #nosec G304 -- the path comes from this test's own glob
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		m := re.FindStringSubmatch(string(raw))
		if m == nil {
			continue
		}
		var out []string
		for _, v := range strings.Split(m[1], ",") {
			out = append(out, strings.Trim(strings.TrimSpace(v), "'"))
		}
		slices.Sort(out)
		return out
	}
	t.Fatalf("no CHECK named %s in migrations/core", constraint)
	return nil
}

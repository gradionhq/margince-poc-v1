// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Every job worker that reaches a store must bind an ACTOR, not just a
// workspace.
//
// A queue is not a request: it inherits no principal, so a worker that binds
// only its tenant reaches the first RBAC gate with nothing to check and the
// write is refused. The provider-run workers shipped that way — the poll job
// failed with "no actor bound to context" on every pass, so a paid enrichment
// run sat in_progress forever and the values it bought reached nobody. The
// money was spent and there was nothing to show for it.
//
// Nothing could have caught that but this shape of test. The failure is in the
// wiring rather than in any store, it needs a real queue to reproduce, and it
// looks exactly like a slow provider from the outside.
//
// So this reads the wiring itself: every Work method that builds a store must
// also bind an actor. Derived from the tree rather than a hand-kept list,
// because the next job added is the one nobody remembers to check.
//
// Three older workers are waived below rather than fixed. They have the same
// shape, but whether each one actually reaches a gated write is a question
// about three other subsystems, and answering it wrongly would either break
// working jobs or paper over a live defect. Issue #1127 tracks that audit; the
// waiver is a ratchet — a waived worker that starts binding an actor fails
// this test until its entry is removed.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// jobActorUnbound: workers that predate this gate and bind no actor. Each
// needs its own subsystem checked before it is changed — see #1127.
var jobActorUnbound = gatekit.Waive(map[string]string{
	"privacyRetentionWorkspaceWorker": "retention sweep; whether its writes are gated is #1127's question",
	"timeScanWorkspaceWorker":         "automation time-scan; same audit",
	"webhookRetryWorkspaceWorker":     "webhook delivery retry; same audit",
})

// storeBuilders are the per-workspace store constructors. A Work method that
// calls one is about to reach an RBAC-gated entry point.
var storeBuilders = regexp.MustCompile(`(providerRunStore|workspaceJobDB)\(`)

// actorBinders are the ways a worker legitimately names its principal: the
// helper in this package, or principal.WithActor directly.
var actorBinders = regexp.MustCompile(`(providerJobActor|principal\.WithActor)\(`)

// workMethod matches a River worker's entry point and captures its receiver,
// which is the worker's name in the failure message.
var workMethod = regexp.MustCompile(`func \(\w+ \*(\w+)\) Work\(`)

func TestEveryJobWorkerThatReachesAStoreBindsAnActor(t *testing.T) {
	defer jobActorUnbound.AssertAllMatched(t)
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the compose package: %v", err)
	}

	offenders := map[string]string{}
	var checked int
	for _, f := range files {
		name := f.Name()
		if !strings.HasPrefix(name, "jobs_") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, body := range workMethodBodies(string(src)) {
			checked++
			if !storeBuilders.MatchString(body.text) {
				continue
			}
			if actorBinders.MatchString(body.text) {
				continue
			}
			if jobActorUnbound.Waived(t, body.worker) {
				continue
			}
			offenders[body.worker] = name
		}
	}

	if checked == 0 {
		t.Fatal("no job Work methods found — this gate would pass vacuously; check the jobs_*.go naming")
	}

	names := make([]string, 0, len(offenders))
	for w := range offenders {
		names = append(names, w)
	}
	sort.Strings(names)
	for _, w := range names {
		t.Errorf("%s.Work (%s) builds a store but binds no actor — its first RBAC-gated write will be refused with \"no actor bound to context\", and a queue carries no principal to inherit. Bind one the way providerJobActor does",
			w, offenders[w])
	}
}

type workBody struct {
	worker string
	text   string
}

// workMethodBodies splits a file into its Work methods. A method runs to the
// next top-level `func ` or to end of file, which is enough structure here: the
// question is only which calls appear inside one.
func workMethodBodies(src string) []workBody {
	locs := workMethod.FindAllStringSubmatchIndex(src, -1)
	out := make([]workBody, 0, len(locs))
	for i, loc := range locs {
		end := len(src)
		if next := regexp.MustCompile(`(?m)^func `).FindStringIndex(src[loc[1]:]); next != nil {
			end = loc[1] + next[0]
		}
		if i+1 < len(locs) && locs[i+1][0] < end {
			end = locs[i+1][0]
		}
		out = append(out, workBody{worker: src[loc[2]:loc[3]], text: src[loc[0]:end]})
	}
	return out
}

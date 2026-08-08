// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
	"time"
)

// jobsContract is a merged jobs.yaml holding the core queues block a fragment
// may ride and whatever kinds a case declares.
func jobsContract(kinds string) map[string][]byte {
	return map[string][]byte{jobsContractBase: []byte(`
queues:
  default: {max_workers: 5}
  ai_capture: {max_workers: 2, reason: long}
kinds:
  close_date_sweep:
    role: dispatcher
    go_type: CloseDateSweepArgs
    queue: default
    timeout: 2m
    opts_owner: caller
    cadence: 24h
` + kinds)}
}

func demoUnits() []extensionUnit { return []extensionUnit{{Name: "demo"}} }

const wellFormedPair = `
  ext_demo_refresh:
    job: refresh
    role: dispatcher
    queue: default
    timeout: 1m
    cadence: 6h
    tier: auto_execute
    scope: read
  ext_demo_refresh_ws:
    job: refresh
    role: workspace
    queue: default
    timeout: 5m
    max_attempts: 3
`

// TestExtensionJobsReadsAPairOutOfTheMergedContract is the happy path: the two
// kinds a scheduled job compiles to are read back out of the document a client
// is handed, not out of the fragment a merge could still have refused.
func TestExtensionJobsReadsAPairOutOfTheMergedContract(t *testing.T) {
	decls, err := extensionJobs(demoUnits(), jobsContract(wellFormedPair))
	if err != nil {
		t.Fatalf("extensionJobs: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("got %d declarations, want 1", len(decls))
	}
	d := decls[0]
	if d.Unit != "demo" || d.Job != "refresh" || d.Queue != "default" {
		t.Fatalf("identity came back as %+v", d)
	}
	if d.Cadence != 6*time.Hour || d.DispatcherTimeout != time.Minute || d.Timeout != 5*time.Minute || d.MaxAttempts != 3 {
		t.Fatalf("mechanics came back as %+v", d)
	}
	if d.DispatcherKind() != "ext_demo_refresh" || d.ChildKind() != "ext_demo_refresh_ws" {
		t.Fatalf("kinds came back as %s / %s", d.DispatcherKind(), d.ChildKind())
	}
}

// TestExtensionJobsIgnoresTheCoreKinds: a vanilla contract composes no jobs,
// which is what keeps the composed lane a superset of the vanilla one.
func TestExtensionJobsIgnoresTheCoreKinds(t *testing.T) {
	decls, err := extensionJobs(nil, jobsContract(""))
	if err != nil {
		t.Fatalf("extensionJobs: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("a contract with no extension kinds composed %d job(s)", len(decls))
	}
}

// TestExtensionJobsRefusesTheShapesAFragmentMustNotPublish. Each case is one
// way a fragment could publish a job the running system could not honour, and
// each has to fail at generation — where the author is looking — rather than
// at a boot the author never runs.
func TestExtensionJobsRefusesTheShapesAFragmentMustNotPublish(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kinds string
		want  string
	}{
		{
			name:  "a queue the installation does not declare",
			kinds: strings.ReplaceAll(wellFormedPair, "queue: default", "queue: ext_demo_pool"),
			want:  "which the contract does not declare",
		},
		{
			name:  "a dispatcher with no child",
			kinds: strings.Split(wellFormedPair, "  ext_demo_refresh_ws:")[0],
			want:  "nothing declares ext_demo_refresh_ws",
		},
		{
			name: "a child no dispatcher fans out to",
			kinds: `
  ext_demo_orphan_ws:
    job: orphan
    role: workspace
    queue: default
    timeout: 5m
    max_attempts: 3
`,
			want: "no dispatcher fans out to",
		},
		{
			name:  "a cadence on the workspace kind",
			kinds: wellFormedPair + "    cadence: 1h\n",
			want:  "declares a cadence",
		},
		{
			name:  "an unrecognised key",
			kinds: wellFormedPair + "    registration: {when: [GmailRegistry]}\n",
			want:  "field registration not found",
		},
		{
			name:  "a kind whose namespace no enabled unit owns",
			kinds: strings.ReplaceAll(wellFormedPair, "ext_demo_", "ext_absent_"),
			want:  "no enabled unit owns it",
		},
		{
			name:  "a job name the kind does not spell",
			kinds: strings.ReplaceAll(wellFormedPair, "job: refresh", "job: rebuild"),
			want:  "whose kind is ext_demo_rebuild",
		},
		{
			name:  "an attempt cap on the dispatcher",
			kinds: strings.Replace(wellFormedPair, "    cadence: 6h", "    cadence: 6h\n    max_attempts: 9", 1),
			want:  "the attempt cap is the CHILD's",
		},
		{
			name:  "a split pool",
			kinds: strings.Replace(wellFormedPair, "    queue: default\n    timeout: 5m", "    queue: ai_capture\n    timeout: 5m", 1),
			want:  "share one pool",
		},
		{
			name:  "a tier no extension may request",
			kinds: strings.Replace(wellFormedPair, "tier: auto_execute", "tier: whenever", 1),
			want:  "is not one an extension may request",
		},
		{
			name:  "no cadence",
			kinds: strings.Replace(wellFormedPair, "    cadence: 6h\n", "", 1),
			want:  "declares no cadence",
		},
		{
			name:  "an unparseable duration",
			kinds: strings.Replace(wellFormedPair, "cadence: 6h", "cadence: soon", 1),
			want:  "invalid duration",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := extensionJobs(demoUnits(), jobsContract(tc.kinds))
			if err == nil {
				t.Fatalf("extensionJobs accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extensionJobs(%s) = %v, want a message mentioning %q", tc.name, err, tc.want)
			}
		})
	}
}

// TestExtensionJobsAttributesAKindToTheLongestOwnedNamespace: a unit name may
// carry a hyphen, which becomes an underscore in the namespace, so `ext_a_b`
// and `ext_a_b_c` are both plausible prefixes of one kind string. Longest wins,
// or one unit would silently own the other's jobs.
func TestExtensionJobsAttributesAKindToTheLongestOwnedNamespace(t *testing.T) {
	units := []extensionUnit{{Name: "a"}, {Name: "a-b"}}
	decls, err := extensionJobs(units, jobsContract(`
  ext_a_b_refresh:
    job: refresh
    role: dispatcher
    queue: default
    timeout: 1m
    cadence: 6h
    tier: auto_execute
    scope: read
  ext_a_b_refresh_ws:
    job: refresh
    role: workspace
    queue: default
    timeout: 5m
    max_attempts: 3
`))
	if err != nil {
		t.Fatalf("extensionJobs: %v", err)
	}
	if len(decls) != 1 || decls[0].Unit != "a-b" {
		t.Fatalf("got %+v, want the kind attributed to a-b", decls)
	}
}

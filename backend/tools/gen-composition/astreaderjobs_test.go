// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// deriveWithJobs lays a one-file unit under a temp root and derives its
// manifest against a contract-declared job set.
func deriveWithJobs(t *testing.T, name, source string, decls ...extension.JobDeclaration) ([]byte, error) {
	t.Helper()
	root := t.TempDir()
	writeUnit(t, root, name, map[string]string{
		"go.mod": "module example.test/ext/" + name + "\n\ngo 1.26.5\n",
		"x.go":   source,
	})
	unit, err := scanUnit(name, filepath.Join(root, "extensions", name))
	if err != nil {
		t.Fatal(err)
	}
	return deriveUnitManifest(unit, realVocabulary(t), nil, decls)
}

func syntheticJob(unit, job string) extension.JobDeclaration {
	return extension.JobDeclaration{
		Unit: extension.Name(unit), Job: job, Queue: "default",
		Cadence: 6 * time.Hour, DispatcherTimeout: time.Minute, Timeout: 5 * time.Minute,
		MaxAttempts: 3, Tier: extension.TierAutoExecute, RequestedScope: extension.ScopeRead,
	}
}

const jobUnitSource = `package j

import "github.com/gradionhq/margince/backend/pkg/extension"

func New() extension.Extension {
	return extension.Extension{
		Name:    "j",
		Version: "1.0.0",
		Jobs:    []extension.Job{{Name: "refresh", Handle: tick}},
	}
}

func tick(ctx context.Context, rt extension.Runtime) error { return nil }
`

// TestTheManifestReaderKnowsTheJobsField: the reader is fail-closed on a field
// it does not recognise, so adding Jobs to the published struct is only half
// the change — the manifest has to be able to derive it, or every unit that
// declares one fails generation with "teach the manifest reader".
func TestTheManifestReaderKnowsTheJobsField(t *testing.T) {
	derived, err := deriveWithJobs(t, "j", jobUnitSource, syntheticJob("j", "refresh"))
	if err != nil {
		t.Fatalf("deriving a unit that declares Jobs: %v", err)
	}
	for _, want := range []string{
		`"id": "job/refresh"`,
		`"kind": "extension_job"`,
		`"operation": "extension.job.tick"`,
		`"operation_id": "ext_j_refresh"`,
		`"contract": "jobs.yaml"`,
		`"tier": "auto_execute"`,
		`"read"`,
	} {
		if !strings.Contains(string(derived), want) {
			t.Errorf("the manifest misses %s:\n%s", want, derived)
		}
	}
}

// TestAJobsEntryMayOnlyDeclareNameAndHandle keeps the split honest: a unit
// spelling its cadence in Go must be told at that line that the field belongs
// to its fragment, rather than having it silently ignored while the contract's
// value governs.
func TestAJobsEntryMayOnlyDeclareNameAndHandle(t *testing.T) {
	src := strings.Replace(jobUnitSource, `{Name: "refresh", Handle: tick}`, `{Name: "refresh", Handle: tick, Cadence: 0}`, 1)
	_, err := deriveWithJobs(t, "j", src, syntheticJob("j", "refresh"))
	if err == nil {
		t.Fatal("the reader accepted a Job field it does not derive")
	}
	if !strings.Contains(err.Error(), "Job field Cadence is not derivable") {
		t.Fatalf("err = %v, want the field to be named", err)
	}
}

// TestBehaviorForAnUndeclaredJobIsRefused: a tick with no contract-declared
// kinds would have no cadence, no wall clock and no attempt cap, and the seam
// would be registering a kind the contract never published.
func TestBehaviorForAnUndeclaredJobIsRefused(t *testing.T) {
	_, err := deriveWithJobs(t, "j", jobUnitSource)
	if err == nil {
		t.Fatal("the reader accepted behavior for a job no fragment declares")
	}
	if !strings.Contains(err.Error(), `job "refresh" has behavior here`) {
		t.Fatalf("err = %v, want the job to be named", err)
	}
}

// TestADeclaredJobWithNoBehaviorIsAContractOnlyRequest: the reverse direction
// is legitimate — the manifest records the request an operator resolves and the
// boot registers nothing, which is the shape crm-hello's hello_ping already
// takes for a tool.
func TestADeclaredJobWithNoBehaviorIsAContractOnlyRequest(t *testing.T) {
	src := strings.Replace(jobUnitSource, "\t\tJobs:    []extension.Job{{Name: \"refresh\", Handle: tick}},\n", "", 1)
	derived, err := deriveWithJobs(t, "j", src, syntheticJob("j", "refresh"))
	if err != nil {
		t.Fatalf("deriving a contract-only job: %v", err)
	}
	if !strings.Contains(string(derived), `"id": "job/refresh"`) {
		t.Fatalf("a contract-only job left no manifest request:\n%s", derived)
	}
}

// TestAJobHandleMustBeAnIdentifierOrTheDocumentedNil: the AST cannot tell an
// inert package-level function from a method value that closes over a receiver,
// so identifier-only is the sole rule that keeps "a declaration is inert data"
// checkable — and the one nil conversion it accepts names extension.JobHandler,
// not the tool type.
func TestAJobHandleMustBeAnIdentifierOrTheDocumentedNil(t *testing.T) {
	for _, tc := range []struct {
		name, handle string
		wantErr      bool
	}{
		{"identifier", "tick", false},
		{"bare nil", "nil", false},
		{"typed nil", "extension.JobHandler(nil)", false},
		{"parenthesised nil", "(nil)", false},
		{"a call", "mkTick()", true},
		{"a method value", "recv.Tick", true},
		{"the tool type's nil", "extension.ToolHandler(nil)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(jobUnitSource, "Handle: tick", "Handle: "+tc.handle, 1)
			_, err := deriveWithJobs(t, "j", src, syntheticJob("j", "refresh"))
			if tc.wantErr && err == nil {
				t.Fatalf("the reader accepted Handle: %s", tc.handle)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("the reader refused Handle: %s: %v", tc.handle, err)
			}
		})
	}
}

// TestAJobDeclaredTwiceIsRefused: two entries for one job would register the
// same pair of kinds twice, which River refuses as a duplicate worker — a boot
// panic in place of a generation error at the author's own line.
func TestAJobDeclaredTwiceIsRefused(t *testing.T) {
	src := strings.Replace(jobUnitSource,
		`[]extension.Job{{Name: "refresh", Handle: tick}}`,
		`[]extension.Job{{Name: "refresh", Handle: tick}, {Name: "refresh", Handle: tick}}`, 1)
	_, err := deriveWithJobs(t, "j", src, syntheticJob("j", "refresh"))
	if err == nil || !strings.Contains(err.Error(), "job refresh declared twice") {
		t.Fatalf("err = %v, want the duplicate to be refused", err)
	}
}

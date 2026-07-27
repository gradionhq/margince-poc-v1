// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The no-payload-capture policy is a property of the TASK CONTRACT
// (api/ai-tasks.yaml), not of this package: a task pinned there must be
// refused capture here. Holding the runtime decision against the contract file
// keeps the two from drifting silently — the failure mode being a task that is
// declared no-payload upstream and quietly captured downstream, which no test
// of either side alone would notice.

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The payload prohibition is a parsed contract field, not a phrase inside a
// doc: string. This reads the contract the generator compiled and holds the
// runtime decision to it, so a task pinned no-payload upstream can never be
// served with capture on.
func TestNoPayloadTasksMatchTheTaskContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/ai-tasks.yaml")
	if err != nil {
		t.Fatalf("reading the task contract: %v", err)
	}
	var contract struct {
		Tasks map[string]struct {
			NoPayload bool `yaml:"no_payload"`
		} `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parsing the task contract: %v", err)
	}
	if len(contract.Tasks) == 0 {
		t.Fatal("no tasks parsed out of ai-tasks.yaml — the scan is broken, not the contract")
	}

	declared := 0
	for name, def := range contract.Tasks {
		if def.NoPayload {
			declared++
		}
		if got := NoPayload(Task(name)); got != def.NoPayload {
			t.Errorf("task %q: contract no_payload=%t, runtime NoPayload=%t", name, def.NoPayload, got)
		}
	}
	if declared == 0 {
		t.Fatal("no task in the contract declares no_payload — either the field was dropped upstream or the pin was lost")
	}

	// The prohibition outranks the deployment posture, which is the whole
	// point: an operator's choice about their own data cannot license
	// retaining a stranger's.
	r := &Router{capturePayloads: true}
	if r.CapturesPayload(TaskCaptureCounterpartyVerdict) {
		t.Error("a capture-on router would retain verdict content despite the contract pin")
	}
}

// The prohibition must not depend on the deployment posture: an operator who
// turns capture ON everywhere still gets no verdict payloads.
func TestAPinnedTaskIsRefusedCaptureEvenWithCaptureEnabled(t *testing.T) {
	enabled := &Router{capturePayloads: true}
	if enabled.CapturesPayload(TaskCaptureCounterpartyVerdict) {
		t.Error("the verdict task captured payloads under an enabling posture — the pin must outrank the operator setting")
	}
	if !enabled.CapturesPayload(TaskCaptureClassify) {
		t.Error("an unpinned task was refused capture — the pin must be narrow, not a blanket off switch")
	}

	disabled := &Router{capturePayloads: false}
	if disabled.CapturesPayload(TaskCaptureClassify) {
		t.Error("capture happened under a disabling posture — the operator setting still governs every unpinned task")
	}
}

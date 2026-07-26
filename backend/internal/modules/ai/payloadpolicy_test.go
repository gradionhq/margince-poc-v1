// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The no-payload-capture policy is a property of the TASK CONTRACT
// (api/ai-tasks.yaml), not of this package: a task pinned there must be
// refused capture here. Holding the Go set against the contract file keeps the
// two from drifting silently — the failure mode being a task that is declared
// no-payload upstream and quietly captured downstream, which no test of either
// side alone would notice.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// noPayloadMarker is the phrase the task contract uses to pin the prohibition.
// It lives in each task's `doc:` string because the contract's structured
// fields (ladder, execution_mode, on_budget_exhausted) are a closed vocabulary
// this repo mirrors verbatim from the spec and may not unilaterally extend.
const noPayloadMarker = "No-payload capture policy"

// taskEntry matches one task's contract block: its name, then everything up to
// the next task key at the same indentation.
var taskEntry = regexp.MustCompile(`(?m)^  ([a-z_]+):\s*\{`)

func TestNoPayloadTasksMatchTheTaskContract(t *testing.T) {
	contract, err := os.ReadFile("../../../api/ai-tasks.yaml")
	if err != nil {
		t.Fatalf("reading the task contract: %v", err)
	}
	body := string(contract)

	declared := map[string]bool{}
	matches := taskEntry.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		t.Fatal("no task entries parsed out of ai-tasks.yaml — the scan is broken, not the contract")
	}
	for i, m := range matches {
		end := len(body)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		if strings.Contains(body[m[0]:end], noPayloadMarker) {
			declared[body[m[2]:m[3]]] = true
		}
	}
	if len(declared) == 0 {
		t.Fatalf("no task in the contract carries %q — either the marker changed or the pin was dropped upstream", noPayloadMarker)
	}

	for name := range declared {
		if !payloadCaptureForbidden[Task(name)] {
			t.Errorf("task %q is pinned no-payload in ai-tasks.yaml but payloadCaptureForbidden would let its content reach ai_call_payload", name)
		}
	}
	for task := range payloadCaptureForbidden {
		if !declared[string(task)] {
			t.Errorf("task %q is refused capture here but the task contract does not pin it — add the pin upstream or drop the entry", task)
		}
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

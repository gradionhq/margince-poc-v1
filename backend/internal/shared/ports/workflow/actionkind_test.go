// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package workflow_test

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// The action set is closed: a new kind is a code-and-test change, never data.
// This is the anti-builder guard the ActionKind doc comment promises.
func TestActionKindSetIsClosed(t *testing.T) {
	want := []workflow.ActionKind{
		workflow.ActionCreateRecord, workflow.ActionUpdateRecord, workflow.ActionCreateTask,
		workflow.ActionAssignOwner, workflow.ActionAdvanceDeal, workflow.ActionSendEmail,
		workflow.ActionEmitFlowEvent, workflow.ActionRecomputeScore, workflow.ActionEnqueueJob,
		workflow.ActionNotify, workflow.ActionAddToList, workflow.ActionDraftEmail,
	}
	got := workflow.AllActionKinds()
	if len(got) != len(want) {
		t.Fatalf("AllActionKinds has %d kinds, pinned list has %d — update both together", len(got), len(want))
	}
	inGot := map[workflow.ActionKind]bool{}
	for _, k := range got {
		inGot[k] = true
	}
	for _, k := range want {
		if !inGot[k] {
			t.Errorf("pinned kind %q is not in AllActionKinds", k)
		}
	}
}

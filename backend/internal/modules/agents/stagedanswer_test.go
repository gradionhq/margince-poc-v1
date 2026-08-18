// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What a 🟡 refusal TELLS the agent, for the two answers the engine can give:
// this call has been staged and needs a human, or a human already answered it
// and the agent is holding the id to spend. An agent given the first line for
// the second case waits for a decision it already has, gives up, and calls
// again — which is how one enrichment collected four approvals.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// The whole-patch staging branch: every touched field is human-owned, so
// nothing applies and the refusal IS the answer. The engine's verdict has to
// reach that answer rather than being dropped on the way.
func TestAWholePatchRefusalTellsTheAgentToSpendAnApprovalItAlreadyHas(t *testing.T) {
	target := ids.NewV7()
	provider := &fixedProvider{record: nativeRecord(datasource.Record{
		Ref:     datasource.EntityRef{Type: datasource.EntityPerson, ID: target},
		Fields:  json.RawMessage(`{"full_name":"Greta Human"}`),
		Version: 7,
	})}
	args := json.RawMessage(`{"record_type":"person","id":"` + target.String() + `","fields":{"full_name":"Greta Machine"}}`)

	for _, tc := range []struct {
		name            string
		alreadyApproved bool
		want            string
		unwanted        string
	}{
		{"undecided", false, "staged as approval", "already approved"},
		{"already approved", true, "already approved this exact call", "once a human approves"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approvals := &recordingApprovals{alreadyApproved: tc.alreadyApproved}
			r := splitRegistry([]string{"full_name"}, approvals, provider)
			_, err := r.Invoke(agentCtx(), "update_record", args)
			var staged *workflow.StagedApprovalError
			if !errors.As(err, &staged) {
				t.Fatalf("refusal = %v, want a StagedApprovalError", err)
			}
			if staged.AlreadyApproved != tc.alreadyApproved {
				t.Fatalf("AlreadyApproved = %v, want %v — the engine's verdict did not reach the agent",
					staged.AlreadyApproved, tc.alreadyApproved)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not say %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.unwanted) {
				t.Fatalf("refusal %q still says %q", err, tc.unwanted)
			}
			// Either way the call still needs the approval presented, so the
			// sentinel does not move — only the instruction does.
			if !errors.Is(err, apperrors.ErrRequiresApproval) {
				t.Fatalf("refusal = %v, want ErrRequiresApproval", err)
			}
			if len(approvals.staged) != 1 {
				t.Fatalf("the gate consulted the engine %d times, want exactly 1", len(approvals.staged))
			}
		})
	}
}

// The split branch: part of the patch landed, and the note spliced into the
// answer carries the same distinction. A note that always says "once a human
// approves it" is what sends the agent back to stage the residue twice.
func TestASplitPatchNoteTellsTheAgentToSpendAnApprovalItAlreadyHas(t *testing.T) {
	id := ids.From[ids.ApprovalKind](ids.NewV7())
	undecided := splitStagingNote([]string{"full_name"}, id, false)
	if !strings.Contains(undecided, "once a human approves it") {
		t.Fatalf("undecided note %q does not tell the agent to wait for a human", undecided)
	}
	released := splitStagingNote([]string{"full_name"}, id, true)
	if !strings.Contains(released, "already approved this exact overwrite") {
		t.Fatalf("released note %q does not tell the agent the decision exists", released)
	}
	if strings.Contains(released, "once a human approves it") {
		t.Fatalf("released note %q still tells the agent to wait", released)
	}
	for _, note := range []string{undecided, released} {
		if !strings.Contains(note, id.String()) {
			t.Fatalf("note %q does not name the approval to present", note)
		}
		if !strings.Contains(note, "full_name") {
			t.Fatalf("note %q does not name the withheld field", note)
		}
	}
}

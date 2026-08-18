// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the REST door tells an agent about the fields it withheld. The MCP twin
// of these two lines is agents.splitStagingNote, and they have to agree on the
// one thing that matters: a note that always says "once a human approves it"
// sends an agent that already HAS the decision back to stage the residue again.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestTheSplitStagingNoteDistinguishesAnUndecidedResidueFromAnApprovedOne(t *testing.T) {
	id := ids.From[ids.ApprovalKind](ids.NewV7())
	undecided := splitStagingNote([]string{"amount_minor"}, id, false)
	if !strings.Contains(undecided, "once a human approves it") {
		t.Fatalf("undecided note %q does not tell the agent to wait for a human", undecided)
	}
	released := splitStagingNote([]string{"amount_minor"}, id, true)
	if !strings.Contains(released, "already approved this exact overwrite") {
		t.Fatalf("released note %q does not tell the agent the decision exists", released)
	}
	if strings.Contains(released, "once a human approves it") {
		t.Fatalf("released note %q still tells the agent to wait", released)
	}
	// Both halves have to name the credential the retry presents and the field
	// at stake, or the agent cannot act on either one.
	for _, note := range []string{undecided, released} {
		if !strings.Contains(note, id.String()) {
			t.Fatalf("note %q does not name the approval to present", note)
		}
		if !strings.Contains(note, approvalTokenHeader) {
			t.Fatalf("note %q does not name the %s header", note, approvalTokenHeader)
		}
		if !strings.Contains(note, "amount_minor") {
			t.Fatalf("note %q does not name the withheld field", note)
		}
	}
}

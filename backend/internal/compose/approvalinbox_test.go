// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func stagedFixture() crmcontracts.Approval {
	summary := "Archive person \"Queue Subject\""
	target := "person"
	targetID := openapi_types.UUID(ids.NewV7())
	change := map[string]any{"record_type": "person"}
	snippet := crmcontracts.ApprovalEvidence{EvidenceSnippet: "as the transcript reads"}
	return crmcontracts.Approval{
		Id: openapi_types.UUID(ids.NewV7()), Kind: "archive_record", Status: "pending",
		ProposedBy: "agent:test", CreatedAt: time.Now(), Summary: &summary,
		TargetEntityType: &target, TargetEntityId: &targetID,
		ProposedChange: &change, Evidence: &[]crmcontracts.ApprovalEvidence{snippet},
	}
}

// The listing is scanned to choose between proposals; the staged document is
// what read_approval answers. A queue that carried every payload would spend a
// run's window on documents nobody asked to see.
func TestTheListingCarriesTheSummaryAndNotTheStagedDocument(t *testing.T) {
	listed := stagedApproval(stagedFixture(), false)
	if listed.Summary == "" {
		t.Error("the listed item carries no sentence a person could answer from")
	}
	if len(listed.ProposedChange) != 0 {
		t.Errorf("the listing carries the staged change: %s", listed.ProposedChange)
	}
	if listed.Evidence != nil {
		t.Errorf("the listing carries evidence: %v", listed.Evidence)
	}
	read := stagedApproval(stagedFixture(), true)
	if len(read.ProposedChange) == 0 || len(read.Evidence) == 0 {
		t.Error("read_approval answered without the change or the evidence it was formed on")
	}
}

// A member with nothing to say is ABSENT. `omitempty` cannot drop a struct, so
// carrying these as values would publish 00000000-0000-… on every proposal that
// has none — and a caller reading decided_by would be told a nobody answered it.
func TestAnAbsentIdIsAbsentAndNotAZeroUUID(t *testing.T) {
	encoded, err := json.Marshal(stagedApproval(stagedFixture(), true))
	if err != nil {
		t.Fatalf("encoding a staged action: %v", err)
	}
	for _, absent := range []string{"decided_by", "bundle_id", "decided_at"} {
		if strings.Contains(string(encoded), absent) {
			t.Errorf("a pending proposal carries %s: %s", absent, encoded)
		}
	}
	if strings.Contains(string(encoded), "00000000-0000") {
		t.Errorf("the answer names a record nobody can look up: %s", encoded)
	}
}

// A reason nobody gave is not an empty quotation: an audit row carrying "" reads
// as if the decider wrote nothing down, which is what they did — so the column
// stays null instead.
func TestAnOmittedReasonIsRecordedAsNoReason(t *testing.T) {
	if got := decisionReason(""); got != nil {
		t.Errorf("decisionReason(\"\") = %q, want nil", *got)
	}
	if got := decisionReason("the customer asked"); got == nil || *got != "the customer asked" {
		t.Errorf("decisionReason lost the decider's own words: %v", got)
	}
}

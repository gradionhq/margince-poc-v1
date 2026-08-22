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
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
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
	listed := stagedActionFrom(stagedFixture(), false)
	if listed.Summary == "" {
		t.Error("the listed item carries no sentence a person could answer from")
	}
	if len(listed.ProposedChange) != 0 {
		t.Errorf("the listing carries the staged change: %s", listed.ProposedChange)
	}
	if listed.Evidence != nil {
		t.Errorf("the listing carries evidence: %v", listed.Evidence)
	}
	read := stagedActionFrom(stagedFixture(), true)
	if len(read.ProposedChange) == 0 || len(read.Evidence) == 0 {
		t.Error("read_approval answered without the change or the evidence it was formed on")
	}
}

// A member with nothing to say is ABSENT. `omitempty` cannot drop a struct, so
// carrying these as values would publish 00000000-0000-… on every proposal that
// has none — and a caller reading decided_by would be told a nobody answered it.
func TestAnAbsentIdIsAbsentAndNotAZeroUUID(t *testing.T) {
	encoded, err := json.Marshal(stagedActionFrom(stagedFixture(), true))
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

// The tool surface decides through its OWN approvals engine, and a kind with no
// executor on that engine is the silent half-effect: the decision commits, the
// caller is answered success, and the release never runs. held_draft is the one
// that matters — its release is a send — and it is registered LATE, from the
// assembled send path, which is exactly the registration a second door forgets.
//
// Held at the WIRING and not at the helper: building the queue over an engine
// the registration list alone produced is the mistake, so that is what has to
// fail. Both directions, because a helper that registers everything proves
// nothing about the line that calls it.
func TestTheToolSurfaceDecidesThroughAnEngineThatCanReleaseEveryKind(t *testing.T) {
	if len(lateApprovalEffects) == 0 {
		t.Fatal("no late effect is registered anywhere — this gate checked nothing")
	}
	// The engine the registry actually composes carries both halves.
	approvalQueue(decidingApprovalsService(nil, SendPath{}, nil))

	// And the one the registration list alone produces does not, so the check
	// above is a check rather than a formality.
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("an engine that cannot release a held draft was accepted as the tool surface's queue")
		}
	}()
	approvalQueue(approvalsServiceWithEffects(nil))
}

// Every kind this installation can RELEASE says whether releasing it sends.
//
// The send cap on a decision is charged off a closed set inside the approvals
// module (`sendingKinds`), and that module cannot see what compose registers —
// so a new kind whose accept effect puts a message on the wire would be
// releasable by a passport whose human deliberately withheld `send`, with every
// test still green. This is the direction that cannot be derived: the effects
// are opaque functions, and only the person wiring one knows whether it sends.
//
// So the census fails closed. A kind registered below and named in neither list
// stops the build until somebody classifies it, and the classification is a
// sentence in a review rather than an omission nobody sees.
func TestEveryReleasableKindSaysWhetherItsReleaseSends(t *testing.T) {
	// The releases that write records and nothing else. Named here rather than
	// in the module because this is the list that grows with the composition.
	inert := map[string]bool{
		"coldstart": true, "enrich": true, "deepread": true, "site_lead": true,
		"capture_counterparty": true, "org_name_promotion": true, "linkedin_match": true,
		"lifecycle_change": true, "assign_owner": true, "close_date_correction": true,
		"deal_follow_up": true, "transcript_proposal": true,
		"fx_rate_proposal": true, "ai_model_rate_proposal": true,
		"project_attribution": true,
	}
	kinds := decidingApprovalsService(nil, SendPath{}, nil).EffectKinds()
	if len(kinds) == 0 {
		t.Fatal("the deciding engine registers no effect at all — this census counted nothing")
	}
	for _, kind := range kinds {
		sends, inertly := approvals.ReleaseSends(kind), inert[kind]
		if sends == inertly {
			fault := "is in neither list"
			if sends {
				fault = "is claimed by both lists at once"
			}
			t.Errorf("%s is registered as a releasable kind and %s. Say which it is: a release that "+
				"puts a message on the wire belongs in approvals.sendingKinds, so a credential whose "+
				"human withheld the send cap cannot spend it; one that only writes records belongs in "+
				"the list above", kind, fault)
		}
	}
}

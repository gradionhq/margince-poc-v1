// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// One brief, two surfaces.
//
// There were two answers to "prepare me for this meeting": a person read eight
// cited sections, and an agent got a separate context walk with its open tasks
// pulled forward. Both were individually reasonable, which is exactly why the
// drift went unnoticed — nobody put the two answers side by side.
//
// The mapping below is the whole crossing, so it is where the two can silently
// stop agreeing again: a section dropped, a citation flattened, a judgment
// served as a fact.

func meetingID() openapi_types.UUID { return openapi_types.UUID(ids.NewV7()) }

// wireBrief is a brief as the HTTP surface serves it, carrying one of each
// thing the crossing has to preserve: a plain fact, a labelled judgment, and a
// line citing two records.
func wireBrief() crmcontracts.MeetingBrief {
	assessment := crmcontracts.Assessment
	deal, activity := meetingID(), meetingID()
	return crmcontracts.MeetingBrief{
		ActivityId:  meetingID(),
		GeneratedAt: time.Date(2026, time.August, 22, 9, 0, 0, 0, time.UTC),
		GeneratedBy: crmcontracts.Deterministic,
		Sections: []crmcontracts.MeetingBriefSection{{
			Kind: crmcontracts.MeetingBriefSectionKindHeader,
			Sentences: []crmcontracts.OrganizationBriefSentence{{
				Text:     "Cutover review with Northwind, Mon 24 Aug 09:00 UTC.",
				Evidence: []crmcontracts.OrganizationBriefEvidence{{EntityType: "activity", EntityId: activity}},
			}},
		}, {
			Kind: crmcontracts.MeetingBriefSectionKindRisks,
			Sentences: []crmcontracts.OrganizationBriefSentence{{
				Text:     "The security pack is four days past its promise.",
				Nature:   &assessment,
				Evidence: []crmcontracts.OrganizationBriefEvidence{{EntityType: "deal", EntityId: deal}, {EntityType: "activity", EntityId: activity}},
			}},
		}},
	}
}

func TestTheAgentReadsEverySectionThePersonReads(t *testing.T) {
	wire := wireBrief()
	got := agentMeetingBrief(wire)

	if len(got.Sections) != len(wire.Sections) {
		t.Fatalf("sections: agent %d, person %d — the two surfaces would disagree about one meeting",
			len(got.Sections), len(wire.Sections))
	}
	for i, section := range wire.Sections {
		if got.Sections[i].Kind != string(section.Kind) {
			t.Errorf("section %d: agent %q, person %q", i, got.Sections[i].Kind, section.Kind)
		}
		if len(got.Sections[i].Sentences) != len(section.Sentences) {
			t.Errorf("section %s: agent %d lines, person %d", section.Kind,
				len(got.Sections[i].Sentences), len(section.Sentences))
		}
		for j, sentence := range section.Sentences {
			if got.Sections[i].Sentences[j].Text != sentence.Text {
				t.Errorf("section %s line %d: agent %q, person %q", section.Kind, j,
					got.Sections[i].Sentences[j].Text, sentence.Text)
			}
		}
	}
	if got.ActivityID != ids.UUID(wire.ActivityId) {
		t.Errorf("the agent was told the brief is about %s, want %s", got.ActivityID, wire.ActivityId)
	}
	if got.GeneratedBy != string(wire.GeneratedBy) {
		t.Errorf("generated_by = %q, want %q — an agent that cannot tell a written brief from a "+
			"deterministic floor cannot weigh what it read", got.GeneratedBy, wire.GeneratedBy)
	}
}

func TestEveryCitationSurvivesTheCrossing(t *testing.T) {
	// The citations are the half an agent acts on. Prose alone would hand it
	// claims it cannot follow up, and the brief's own rule — every sentence
	// cited or dropped whole — would be silently undone at the boundary.
	got := agentMeetingBrief(wireBrief())

	risks := got.Sections[1].Sentences[0]
	if len(risks.Evidence) != 2 {
		t.Fatalf("the risk line cites %d records, want 2", len(risks.Evidence))
	}
	if risks.Evidence[0].RecordType != "deal" || risks.Evidence[1].RecordType != "activity" {
		t.Errorf("citation kinds = %q, %q; want deal then activity, in the order the line cites them",
			risks.Evidence[0].RecordType, risks.Evidence[1].RecordType)
	}
	for _, cited := range risks.Evidence {
		if cited.RecordID.IsZero() {
			t.Error("a citation crossed with no record id, which is the only part an agent can act on")
		}
	}
}

func TestAJudgmentReachesTheAgentLabelledAsOne(t *testing.T) {
	// A reading served as a record is the one error a grounding rule cannot
	// recover from downstream: the agent repeats it as something the record
	// says.
	got := agentMeetingBrief(wireBrief())

	if got.Sections[0].Sentences[0].Nature != "" {
		t.Errorf("a plain fact was labelled %q; empty is the contract's default and means fact",
			got.Sections[0].Sentences[0].Nature)
	}
	if got.Sections[1].Sentences[0].Nature != string(crmcontracts.Assessment) {
		t.Errorf("nature = %q, want %q", got.Sections[1].Sentences[0].Nature, crmcontracts.Assessment)
	}
}

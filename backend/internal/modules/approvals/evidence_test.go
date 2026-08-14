// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestEvidenceWithNothingToCiteStaysNullRatherThanEmpty(t *testing.T) {
	raw, err := marshalEvidence(nil)
	if err != nil {
		t.Fatalf("marshalling absent evidence: %v", err)
	}
	if raw != nil {
		t.Fatalf("absent evidence must persist as SQL NULL so it reads as %q rather than %q, got %s",
			"nothing was read", "it was read and nothing backed it", raw)
	}
}

func TestEvidenceRoundTripsThroughTheStoredShape(t *testing.T) {
	source := ids.New[ids.ActivityKind]()
	raw, err := marshalEvidence([]Evidence{{
		Snippet:     "Priya: I'll send the revised pricing by Friday.",
		SourceType:  "activity",
		SourceID:    source.UUID,
		SourceLines: []int{12},
	}})
	if err != nil {
		t.Fatalf("marshalling evidence: %v", err)
	}

	wired := wireEvidence(raw)
	if wired == nil {
		t.Fatal("evidence that was stored must read back; the human confirming the proposal has nothing to check otherwise")
	}
	got := *wired
	if len(got) != 1 {
		t.Fatalf("want one evidence element, got %d", len(got))
	}
	if got[0].EvidenceSnippet != "Priya: I'll send the revised pricing by Friday." {
		t.Errorf("snippet did not survive the round trip: %q", got[0].EvidenceSnippet)
	}
	if got[0].SourceType == nil || string(*got[0].SourceType) != "activity" {
		t.Errorf("source_type did not survive the round trip: %+v", got[0].SourceType)
	}
	if got[0].SourceId == nil || ids.UUID(*got[0].SourceId) != source.UUID {
		t.Errorf("source_id did not survive the round trip: %+v", got[0].SourceId)
	}
	if got[0].SourceLines == nil || len(*got[0].SourceLines) != 1 || (*got[0].SourceLines)[0] != 12 {
		t.Errorf("source_lines did not survive the round trip: %+v", got[0].SourceLines)
	}
}

func TestEvidenceWithoutASourceOmitsThePointerInsteadOfNamingTheNilRecord(t *testing.T) {
	raw, err := marshalEvidence([]Evidence{{Snippet: "the deal has no close date"}})
	if err != nil {
		t.Fatalf("marshalling sourceless evidence: %v", err)
	}
	var stored []evidenceJSON
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("stored evidence is not readable JSON: %v", err)
	}
	if stored[0].SourceID != nil || stored[0].SourceType != nil {
		t.Errorf("evidence with no source must persist null, not the nil uuid which reads as a record that does not exist: %+v", stored[0])
	}
	if wired := wireEvidence(raw); wired == nil || (*wired)[0].SourceId != nil {
		t.Error("a null source id must read back as absent")
	}
}

func TestEvidenceRefusesCitationsAHumanCouldNotCheck(t *testing.T) {
	source := ids.New[ids.ActivityKind]().UUID
	for _, tc := range []struct {
		name     string
		evidence Evidence
		wantMsg  string
	}{
		{
			name:     "no snippet",
			evidence: Evidence{SourceType: "activity", SourceID: source},
			wantMsg:  "no snippet",
		},
		{
			name:     "snippet re-tells the record",
			evidence: Evidence{Snippet: strings.Repeat("x", MaxEvidenceSnippet+1)},
			wantMsg:  "over the",
		},
		{
			name:     "source type outside the contract enum",
			evidence: Evidence{Snippet: "s", SourceType: "transcript", SourceID: source},
			wantMsg:  "is not one of",
		},
		{
			name:     "source id with no type to resolve it",
			evidence: Evidence{Snippet: "s", SourceID: source},
			wantMsg:  "no source_type",
		},
		{
			name:     "source type with no id",
			evidence: Evidence{Snippet: "s", SourceType: "activity"},
			wantMsg:  "no source id",
		},
		{
			name:     "line zero in a 1-based addressing scheme",
			evidence: Evidence{Snippet: "s", SourceType: "activity", SourceID: source, SourceLines: []int{0}},
			wantMsg:  "1-based",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := marshalEvidence([]Evidence{tc.evidence})
			if err == nil {
				t.Fatal("want a refusal; an unreadable citation is worse than none because it reads as corroboration")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the message must say what is wrong; want it to contain %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}

func TestUnreadableStoredEvidenceReadsAsAbsentRatherThanPartial(t *testing.T) {
	if got := wireEvidence(json.RawMessage(`{"not":"an array"}`)); got != nil {
		t.Errorf("evidence that cannot be parsed must read as absent so nobody confirms against half a citation, got %+v", got)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Specs for progress_deal: it inherits advance_deal's won/lost 🟡 floor
// unchanged (an intent composition never widens authority), and its
// effect is the composed pair — the stage move first, then the linked
// timeline note.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

type fixedStages struct {
	semantic string
	pipeline ids.UUID
}

func (f fixedStages) StageSemantic(context.Context, ids.UUID) (string, ids.UUID, error) {
	return f.semantic, f.pipeline, nil
}

func TestProgressDealResolverKeepsTheWonLostFloor(t *testing.T) {
	resolver := progressDeal{}.Spec().TierResolver
	if resolver == nil {
		t.Fatal("progress_deal is TierDynamic and must carry a resolver")
	}
	cases := map[string]mcp.RiskTier{
		"open": mcp.TierAutoExecute,
		"won":  mcp.TierConfirmationRequired,
		"lost": mcp.TierConfirmationRequired,
		"":     mcp.TierConfirmationRequired, // an unprovable semantic fails toward the gate
	}
	for semantic, want := range cases {
		if got := resolver(mcp.TierResolverInput{TargetStageSemantic: semantic}); got != want {
			t.Errorf("semantic %q resolves tier %v, want %v", semantic, got, want)
		}
	}
}

func TestProgressDealResolverInputReadsTheStageSemantic(t *testing.T) {
	pipeline := ids.NewV7()
	tool := progressDeal{stages: fixedStages{semantic: "won", pipeline: pipeline}}
	in, err := tool.ResolverInput(context.Background(),
		json.RawMessage(`{"deal_id":"`+ids.NewV7().String()+`","to_stage_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.TargetStageSemantic != "won" || in.PipelineID != pipeline.String() {
		t.Fatalf("resolver input = %+v, want the configured semantic and pipeline", in)
	}
}

func progressFixture(dealID, noteID ids.UUID) *fakeSoR {
	dealRef := datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID}
	return &fakeSoR{
		records: map[datasource.EntityRef]datasource.Record{
			dealRef: {Ref: dealRef, Fields: json.RawMessage(`{"name":"Acme renewal"}`), Version: 5},
		},
		createRef: datasource.EntityRef{Type: datasource.EntityActivity, ID: noteID},
	}
}

func TestProgressDealAdvancesThenLogsTheLinkedNote(t *testing.T) {
	dealID, stageID, noteID := ids.NewV7(), ids.NewV7(), ids.NewV7()
	p := progressFixture(dealID, noteID)
	tool := progressDeal{p: p, stages: fixedStages{semantic: "open"}}

	raw, err := tool.Handle(context.Background(), json.RawMessage(
		`{"deal_id":"`+dealID.String()+`","to_stage_id":"`+stageID.String()+`","note":"pinged the CFO, demo re-booked"}`))
	if err != nil {
		t.Fatal(err)
	}

	if len(p.advances) != 1 || p.advances[0].DealID != dealID || p.advances[0].ToStageID != stageID {
		t.Fatalf("advances = %+v, want the one requested move", p.advances)
	}
	if p.advances[0].Source != toolSource {
		t.Fatalf("advance source = %q, want %q", p.advances[0].Source, toolSource)
	}
	if len(p.creates) != 1 {
		t.Fatalf("creates = %d, want the one note", len(p.creates))
	}
	noteRaw, err := datasource.RawFields(p.creates[0].Fields)
	if err != nil {
		t.Fatal(err)
	}
	var note struct {
		Kind  string `json:"kind"`
		Body  string `json:"body"`
		Links []struct {
			EntityType string   `json:"entity_type"`
			EntityID   ids.UUID `json:"entity_id"`
		} `json:"links"`
	}
	if err := json.Unmarshal(noteRaw, &note); err != nil {
		t.Fatal(err)
	}
	if note.Kind != "note" || note.Body != "pinged the CFO, demo re-booked" {
		t.Fatalf("note fields = %+v, want the caller's note as a note activity", note)
	}
	if len(note.Links) != 1 || note.Links[0].EntityType != "deal" || note.Links[0].EntityID != dealID {
		t.Fatalf("note links = %+v, want the deal link", note.Links)
	}

	var out struct {
		NoteActivityID string `json:"note_activity_id"`
		Deal           struct {
			RecordType string `json:"record_type"`
			ID         string `json:"id"`
		} `json:"deal"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.NoteActivityID != noteID.String() {
		t.Fatalf("note_activity_id = %q, want %q", out.NoteActivityID, noteID)
	}
	if out.Deal.RecordType != "deal" || out.Deal.ID != dealID.String() {
		t.Fatalf("deal read-back = %+v, want the advanced deal", out.Deal)
	}
}

func TestProgressDealWithoutANoteWritesNoActivity(t *testing.T) {
	dealID := ids.NewV7()
	p := progressFixture(dealID, ids.NewV7())
	tool := progressDeal{p: p, stages: fixedStages{semantic: "open"}}

	raw, err := tool.Handle(context.Background(), json.RawMessage(
		`{"deal_id":"`+dealID.String()+`","to_stage_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.creates) != 0 {
		t.Fatalf("creates = %d, want none without a note", len(p.creates))
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if _, present := out["note_activity_id"]; present {
		t.Fatal("note_activity_id must be absent when nothing was logged — never a fabricated id")
	}
}

func TestProgressDealStageInfoPinsTheCurrentDealVersion(t *testing.T) {
	dealID := ids.NewV7()
	p := progressFixture(dealID, ids.NewV7())
	tool := progressDeal{p: p, stages: fixedStages{semantic: "won"}}

	info, err := tool.StageInfo(context.Background(), json.RawMessage(
		`{"deal_id":"`+dealID.String()+`","to_stage_id":"`+ids.NewV7().String()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if info.TargetType != "deal" || info.TargetID != dealID {
		t.Fatalf("stage info targets %s %s, want the deal", info.TargetType, info.TargetID)
	}
	if info.TargetVersion == nil || *info.TargetVersion != 5 {
		t.Fatalf("TargetVersion = %v, want the deal's current version 5 — approval covers the deal as seen", info.TargetVersion)
	}
	if info.Summary == "" {
		t.Fatal("the inbox needs a one-line summary")
	}
}

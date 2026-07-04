package upstream

import (
	"testing"

	"github.com/gradionhq/margince/cli/craft/gate"
	"github.com/gradionhq/margince/cli/craft/rubric"
)

func block(seq int, cats ...string) Record {
	return Record{Seq: seq, Verdict: gate.VerdictBlock, BlockerCategories: cats}
}
func pass(seq int) Record { return Record{Seq: seq, Verdict: gate.VerdictPass} }

func TestRankBlockers_byFrequency(t *testing.T) {
	records := []Record{
		block(1, "over-commenting", "type-escape-hatch"),
		block(2, "over-commenting"),
		block(3, "over-commenting"),
		block(4, "type-escape-hatch"),
	}
	ranked := RankBlockers(records)
	if ranked[0].Category != "over-commenting" || ranked[0].Count != 3 {
		t.Fatalf("top blocker = %+v, want over-commenting x3", ranked[0])
	}
	if ranked[1].Category != "type-escape-hatch" || ranked[1].Count != 2 {
		t.Errorf("second blocker = %+v, want type-escape-hatch x2", ranked[1])
	}
}

func TestBlockRateTrend_tracksFallingRateOverTime(t *testing.T) {
	// Early window all blocks, late window all passes — the rate should fall, the
	// signal that the upstream guardrails are working.
	records := []Record{
		block(1, "over-commenting"), block(2, "over-commenting"),
		pass(3), pass(4),
	}
	trend := BlockRateTrend(records, 2)
	if len(trend) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(trend))
	}
	if trend[0] <= trend[1] {
		t.Errorf("expected a falling block rate, got %v", trend)
	}
	if trend[0] != 1.0 || trend[1] != 0.0 {
		t.Errorf("trend = %v, want [1 0]", trend)
	}
}

func TestPropose_topBlockersWithRubricText(t *testing.T) {
	r, err := rubric.Load()
	if err != nil {
		t.Fatalf("load rubric: %v", err)
	}
	records := []Record{
		block(1, "over-commenting"), block(2, "over-commenting"), block(3, "type-escape-hatch"),
	}
	proposals := Propose(records, 1, r)
	if len(proposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(proposals))
	}
	if proposals[0].Category != "over-commenting" {
		t.Errorf("top proposal = %q, want over-commenting", proposals[0].Category)
	}
	if proposals[0].Rule == "" {
		t.Error("proposal must carry the rubric rule text so the guardrail is grounded")
	}
}

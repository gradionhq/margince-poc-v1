package golden

import (
	"testing"

	"github.com/gradionhq/margince/cli/craft/gate"
)

func outcome(kind Kind, want, got gate.Verdict) Outcome {
	return Outcome{Kind: kind, WantVerdict: want, GotVerdict: got, VerdictMatch: want == got}
}

func TestComputeMetrics_perfectGate(t *testing.T) {
	m := ComputeMetrics([]Outcome{
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictBlock),
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictBlock),
		outcome(KindGood, gate.VerdictPass, gate.VerdictPass),
	})
	if m.BlockPrecision != 1 || m.SlopRecall != 1 || m.FalsePositiveRate != 0 {
		t.Errorf("perfect gate: %+v", m)
	}
}

func TestComputeMetrics_falsePositiveDropsPrecision(t *testing.T) {
	m := ComputeMetrics([]Outcome{
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictBlock), // TP
		outcome(KindGood, gate.VerdictPass, gate.VerdictBlock),  // FP — good wrongly blocked
	})
	if m.BlockPrecision != 0.5 {
		t.Errorf("BlockPrecision = %v, want 0.5", m.BlockPrecision)
	}
	if m.FalsePositiveRate != 1 {
		t.Errorf("FalsePositiveRate = %v, want 1", m.FalsePositiveRate)
	}
}

func TestComputeMetrics_falseNegativeDropsRecall(t *testing.T) {
	m := ComputeMetrics([]Outcome{
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictBlock), // TP
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictPass),  // FN — slop missed
	})
	if m.SlopRecall != 0.5 {
		t.Errorf("SlopRecall = %v, want 0.5", m.SlopRecall)
	}
}

func TestEvaluate_failsOnPrecisionDropOrRegression(t *testing.T) {
	clean := []Outcome{
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictBlock),
		outcome(KindGood, gate.VerdictPass, gate.VerdictPass),
	}
	if r := Evaluate(clean, 1.0); !r.Pass {
		t.Errorf("clean run should pass: %+v", r)
	}

	withFP := []Outcome{
		outcome(KindSlop, gate.VerdictBlock, gate.VerdictBlock),
		outcome(KindGood, gate.VerdictPass, gate.VerdictBlock),
	}
	if r := Evaluate(withFP, 1.0); r.Pass {
		t.Error("a false positive must fail the precision floor")
	}

	withRegression := []Outcome{outcome(KindSlop, gate.VerdictBlock, gate.VerdictPass)}
	if r := Evaluate(withRegression, 0.0); r.Pass || len(r.Mismatches) != 1 {
		t.Errorf("a regressed confirmed case must fail: %+v", r)
	}
}

func TestWithDisputeRate(t *testing.T) {
	if m := (Metrics{}).WithDisputeRate(3, 100); m.DisputeRate != 0.03 {
		t.Errorf("DisputeRate = %v, want 0.03", m.DisputeRate)
	}
	if (Metrics{}).WithDisputeRate(0, 0).DisputeRate != 0 {
		t.Error("no reviews => dispute rate 0")
	}
}

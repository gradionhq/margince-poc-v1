package golden

// The calibration metrics over a golden run. BLOCK precision is the headline:
// because the block has no human override, a single false block wedges the build
// agent, so precision must stay ~100%. Its regression is a hard promotion failure
// (B-EP11.8b). Slop recall and the false-positive rate track the other direction
// (misses) and operational health.

// Metrics is the confusion matrix over a golden run plus the derived rates. A
// "positive" is a BLOCK verdict; slop cases are the true class that should block.
type Metrics struct {
	TruePositives  int `json:"true_positives"`  // slop correctly blocked
	FalsePositives int `json:"false_positives"` // good wrongly blocked
	TrueNegatives  int `json:"true_negatives"`  // good correctly passed
	FalseNegatives int `json:"false_negatives"` // slop wrongly passed

	BlockPrecision    float64 `json:"block_precision"`     // TP / (TP+FP) — of all blocks, how many were real
	SlopRecall        float64 `json:"slop_recall"`         // TP / (TP+FN) — of all slop, how much caught
	FalsePositiveRate float64 `json:"false_positive_rate"` // FP / (FP+TN) — of all good, how much wrongly blocked
	DisputeRate       float64 `json:"dispute_rate"`        // disputes / reviews — fed from B-EP11.7 signals
}

// ComputeMetrics derives the confusion matrix and rates from a golden run. Rates
// with an empty denominator are defined to their safe value (precision 1 when
// nothing blocked, recall 1 when there is no slop, FP-rate 0 when nothing good).
func ComputeMetrics(outcomes []Outcome) Metrics {
	var m Metrics
	for _, o := range outcomes {
		blocked := o.GotVerdict == "BLOCK"
		switch {
		case o.Kind == KindSlop && blocked:
			m.TruePositives++
		case o.Kind == KindSlop && !blocked:
			m.FalseNegatives++
		case o.Kind == KindGood && blocked:
			m.FalsePositives++
		default:
			m.TrueNegatives++
		}
	}
	m.BlockPrecision = ratio(m.TruePositives, m.TruePositives+m.FalsePositives, 1)
	m.SlopRecall = ratio(m.TruePositives, m.TruePositives+m.FalseNegatives, 1)
	m.FalsePositiveRate = ratio(m.FalsePositives, m.FalsePositives+m.TrueNegatives, 0)
	return m
}

// WithDisputeRate returns m with the dispute rate populated from operational
// signals (B-EP11.7): the share of reviews an author contested.
func (m Metrics) WithDisputeRate(disputes, reviews int) Metrics {
	m.DisputeRate = ratio(disputes, reviews, 0)
	return m
}

func ratio(num, den int, emptyDen float64) float64 {
	if den == 0 {
		return emptyDen
	}
	return float64(num) / float64(den)
}

// EvalReport is the verdict of a calibration run against the golden set.
type EvalReport struct {
	Metrics    Metrics   `json:"metrics"`
	Mismatches []Outcome `json:"mismatches"` // confirmed cases whose verdict regressed
	Pass       bool      `json:"pass"`
}

// Evaluate fails if BLOCK precision drops below the floor, or if any confirmed
// case regressed (got != expected). Both are hard failures — the candidate gate
// cannot be promoted (B-EP11.8b) and CI fails.
func Evaluate(outcomes []Outcome, minBlockPrecision float64) EvalReport {
	m := ComputeMetrics(outcomes)
	var mismatches []Outcome
	for _, o := range outcomes {
		if !o.VerdictMatch {
			mismatches = append(mismatches, o)
		}
	}
	return EvalReport{
		Metrics:    m,
		Mismatches: mismatches,
		Pass:       m.BlockPrecision >= minBlockPrecision && len(mismatches) == 0,
	}
}

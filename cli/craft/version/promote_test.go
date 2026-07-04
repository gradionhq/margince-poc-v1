package version

import (
	"testing"

	"github.com/gradionhq/margince/cli/craft/golden"
)

func TestDecide_promoteAndRollback(t *testing.T) {
	current := Tuple{Prompt: "1", Rubric: "1", ExemplarSet: "1", Model: "m"}
	candidate := Tuple{Prompt: "2", Rubric: "1", ExemplarSet: "2", Model: "m"}

	pass := func(precision float64) golden.EvalReport {
		return golden.EvalReport{Pass: true, Metrics: golden.Metrics{BlockPrecision: precision}}
	}

	t.Run("promotes a non-regressing candidate", func(t *testing.T) {
		p := Decide(current, candidate, pass(1.0), 1.0)
		if !p.Promoted || p.Active != candidate {
			t.Errorf("expected promotion to candidate, got %+v", p)
		}
	})

	t.Run("rolls back when eval fails", func(t *testing.T) {
		failed := golden.EvalReport{Pass: false, Metrics: golden.Metrics{BlockPrecision: 0.8}}
		p := Decide(current, candidate, failed, 1.0)
		if p.Promoted || p.Active != current {
			t.Errorf("expected rollback to current, got %+v", p)
		}
	})

	t.Run("rolls back on a BLOCK-precision regression even if eval passed its own floor", func(t *testing.T) {
		// Candidate's own eval passed at a lower floor, but precision regressed
		// below the active version's — auto-rollback.
		p := Decide(current, candidate, pass(0.95), 1.0)
		if p.Promoted || p.Active != current {
			t.Errorf("expected rollback on precision regression, got %+v", p)
		}
	})
}

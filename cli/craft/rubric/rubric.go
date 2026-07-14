// Package rubric is the machine-readable craftsmanship standard the review agent
// reviews against. The canonical text lives in specs/quality/craftsmanship.md; rubric.json
// is the version the gate consumes. See architecture/15 (ADR-0045/A60).
package rubric

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed rubric.json
var raw []byte

// Kind distinguishes a forbidden anti-tell from a positive-rubric guideline.
type Kind string

const (
	// KindAntiTell is a forbidden craftsmanship anti-pattern.
	KindAntiTell Kind = "anti_tell"
	// KindPositive is a positive-rubric guideline to uphold.
	KindPositive Kind = "positive"
)

// Rule is one craftsmanship rule. Every rule carries an id, a category, and a
// BLOCK-eligibility flag — only block_eligible categories can produce a merge-blocking verdict.
type Rule struct {
	ID            string `json:"id"`
	Kind          Kind   `json:"kind"`
	Category      string `json:"category"`
	BlockEligible bool   `json:"block_eligible"`
	Title         string `json:"title"`
	Rule          string `json:"rule"`
}

// Rubric is the versioned standard. Version pins one third of the gate's identity
// tuple (prompt, rubric, exemplars, model).
type Rubric struct {
	Version       string            `json:"version"`
	Source        string            `json:"source"`
	Binds         []string          `json:"binds"`
	MetaRule      string            `json:"meta_rule"`
	SeverityModel map[string]string `json:"severity_model"`
	Rules         []Rule            `json:"rules"`
}

// Load returns the embedded rubric.
func Load() (*Rubric, error) {
	var r Rubric
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("parse rubric.json: %w", err)
	}
	return &r, nil
}

// BlockEligible reports whether a finding in this category may produce a BLOCK
// verdict. An unknown category is never block-eligible — the gate fails safe toward PASS.
func (r *Rubric) BlockEligible(category string) bool {
	for _, rule := range r.Rules {
		if rule.Category == category {
			return rule.BlockEligible
		}
	}
	return false
}

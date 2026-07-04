// Package upstream closes the flywheel's cheapest loop: it ranks the most
// frequent BLOCKER categories and proposes promoting them into the authoring
// guardrails (the AGENTS.md ## Craftsmanship section), so author-agents stop
// making the mistake and the block rate falls over time. The real success metric
// is a falling block rate, not a busy gate (architecture/17 §6).
package upstream

import (
	"sort"

	"github.com/gradionhq/margince/cli/craft/gate"
	"github.com/gradionhq/margince/cli/craft/rubric"
)

// Record is one recorded review verdict. Seq is the ordering axis (a monotonic
// counter, not a wall clock) so the trend is reproducible.
type Record struct {
	Seq               int          `json:"seq"`
	Verdict           gate.Verdict `json:"verdict"`
	BlockerCategories []string     `json:"blocker_categories"`
}

// CategoryCount is a blocker category and how often it caused a block.
type CategoryCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

// RankBlockers ranks blocker categories by frequency (desc), ties broken by name.
func RankBlockers(records []Record) []CategoryCount {
	counts := map[string]int{}
	for _, r := range records {
		for _, c := range r.BlockerCategories {
			counts[c]++
		}
	}
	ranked := make([]CategoryCount, 0, len(counts))
	for c, n := range counts {
		ranked = append(ranked, CategoryCount{Category: c, Count: n})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Category < ranked[j].Category
	})
	return ranked
}

// BlockRate is the share of records that blocked.
func BlockRate(records []Record) float64 {
	if len(records) == 0 {
		return 0
	}
	blocked := 0
	for _, r := range records {
		if r.Verdict == gate.VerdictBlock {
			blocked++
		}
	}
	return float64(blocked) / float64(len(records))
}

// BlockRateTrend splits the records (in order) into up to `windows` chunks and
// returns the block rate of each — so a falling block rate is visible over time.
func BlockRateTrend(records []Record, windows int) []float64 {
	if windows < 1 || len(records) == 0 {
		return nil
	}
	if windows > len(records) {
		windows = len(records)
	}
	trend := make([]float64, 0, windows)
	size := len(records) / windows
	for w := 0; w < windows; w++ {
		start := w * size
		end := start + size
		if w == windows-1 {
			end = len(records) // last window absorbs the remainder
		}
		trend = append(trend, BlockRate(records[start:end]))
	}
	return trend
}

// Proposal is an authoring-guardrail addition for a frequent blocker, to be
// applied as a human-ratified PR against AGENTS.md ## Craftsmanship.
type Proposal struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
	Rule     string `json:"rule"`
}

// Propose returns the top-N most frequent blocker categories as guardrail
// additions, using the rubric's own rule text so the proposal is grounded.
func Propose(records []Record, topN int, r *rubric.Rubric) []Proposal {
	ruleByCategory := map[string]string{}
	for _, rule := range r.Rules {
		ruleByCategory[rule.Category] = rule.Rule
	}
	ranked := RankBlockers(records)
	if topN < len(ranked) {
		ranked = ranked[:topN]
	}
	proposals := make([]Proposal, 0, len(ranked))
	for _, c := range ranked {
		proposals = append(proposals, Proposal{Category: c.Category, Count: c.Count, Rule: ruleByCategory[c.Category]})
	}
	return proposals
}

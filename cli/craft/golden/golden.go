// Package golden is the versioned regression suite for the craftsmanship gate:
// curated good/slop cases with expected verdicts. Running a candidate gate over
// the corpus is what makes a no-override hard block safe to leave merge-blocking
// (B-EP11.6b calibrates BLOCK precision; B-EP11.8b gates promotion on it).
package golden

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/cli/craft/gate"
)

//go:embed corpus.json
var corpusRaw []byte

//go:embed confirmed.json
var confirmedRaw []byte

// Kind labels a case as known-good (expected PASS) or known-slop (expected BLOCK).
type Kind string

const (
	// KindGood marks a known-good case expected to PASS.
	KindGood Kind = "good"
	// KindSlop marks a known-slop case expected to BLOCK.
	KindSlop Kind = "slop"
)

// Case is one curated diff with its expected verdict and the categories a correct
// reviewer should raise.
type Case struct {
	ID               string            `json:"id"`
	Kind             Kind              `json:"kind"`
	Diff             string            `json:"diff"`
	Files            map[string]string `json:"files"`
	ExpectVerdict    gate.Verdict      `json:"expect_verdict"`
	ExpectCategories []string          `json:"expect_categories"`
}

// Corpus is the versioned set of curated regression cases.
type Corpus struct {
	Version string `json:"version"`
	Cases   []Case `json:"cases"`
}

// Load returns the embedded corpus.
func Load() (*Corpus, error) {
	var c Corpus
	if err := json.Unmarshal(corpusRaw, &c); err != nil {
		return nil, fmt.Errorf("parse corpus.json: %w", err)
	}
	return &c, nil
}

// ConfirmedIDs returns the case ids that must always be present (the growth
// guarantee — cases are never silently pruned).
func ConfirmedIDs() ([]string, error) {
	var c struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(confirmedRaw, &c); err != nil {
		return nil, fmt.Errorf("parse confirmed.json: %w", err)
	}
	return c.IDs, nil
}

// Outcome is the result of replaying one case through a candidate gate.
type Outcome struct {
	CaseID       string
	Kind         Kind
	WantVerdict  gate.Verdict
	GotVerdict   gate.Verdict
	VerdictMatch bool
}

// Run replays every case through the reviewer and records the per-case outcome.
// It is the engine both the eval gate (B-EP11.6b) and promotion (B-EP11.8b) use.
func Run(ctx context.Context, rv *gate.Reviewer, cases []Case) ([]Outcome, error) {
	outcomes := make([]Outcome, 0, len(cases))
	for _, c := range cases {
		res, err := rv.Review(ctx, gate.Inputs{Diff: c.Diff, TouchedFiles: c.Files})
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		outcomes = append(outcomes, Outcome{
			CaseID:       c.ID,
			Kind:         c.Kind,
			WantVerdict:  c.ExpectVerdict,
			GotVerdict:   res.Verdict,
			VerdictMatch: res.Verdict == c.ExpectVerdict,
		})
	}
	return outcomes, nil
}

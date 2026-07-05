// Package version pins the craftsmanship gate's identity tuple
// (prompt, rubric, exemplar-set, code, model). The composed gate_version is stamped on
// every verdict so any review is reproducible and auditable; improvement is an
// explicit, eval-gated promotion of this tuple (B-EP11.8b), never a live edit of a
// running gate (architecture/17 §1; forbids the "zombie agent" failure mode, P12).
package version

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/cli/craft/rubric"
)

//go:embed gate-version.json
var pinnedRaw []byte

// Tuple is the five-part gate identity. Three of the parts are pinned in
// gate-version.json (prompt, code, model); the rubric and exemplar-set versions come
// from their own version-controlled files, so a change to any of them changes the
// gate_version and is visible in review history.
type Tuple struct {
	Prompt      string `json:"prompt"`
	Rubric      string `json:"rubric"`
	ExemplarSet string `json:"exemplar_set"`
	Code        string `json:"code"`
	Model       string `json:"model"`
}

// String is the gate_version stamped on every verdict. It is stable for a fixed
// set of pinned files — the reproducibility guarantee.
func (t Tuple) String() string {
	return fmt.Sprintf("p%s+r%s+e%s+c%s+%s", t.Prompt, t.Rubric, t.ExemplarSet, t.Code, t.Model)
}

type pinned struct {
	Prompt string `json:"prompt_version"`
	Code   string `json:"code_version"`
	Model  string `json:"model_id"`
}

// Current composes the pinned tuple from the version-controlled files. It is
// deterministic: same files in, same tuple out.
func Current() (Tuple, error) {
	var p pinned
	if err := json.Unmarshal(pinnedRaw, &p); err != nil {
		return Tuple{}, fmt.Errorf("parse gate-version.json: %w", err)
	}
	r, err := rubric.Load()
	if err != nil {
		return Tuple{}, err
	}
	set, err := LoadExemplars()
	if err != nil {
		return Tuple{}, err
	}
	return Tuple{Prompt: p.Prompt, Rubric: r.Version, ExemplarSet: set.Version, Code: p.Code, Model: p.Model}, nil
}

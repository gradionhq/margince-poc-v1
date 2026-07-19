// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package aicert is the manual-lane AI certification harness's pure-library
// layer: the scenario corpus format, structural output checks, the §5
// verdict math, and the on-disk record format. It has no side effects
// beyond the file I/O its functions are named for (LoadCorpus,
// WriteRecord/LoadRecords) — no time.Now, no network, no database — so a
// certification run is reproducible from a corpus, a set of RunResults, and
// a clock reading the CALLER supplies. The runner that drives real model
// calls lives in this package too, but as its own file: this file and its
// siblings (checks.go, score.go, record.go) stay callable without one.
package aicert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// sourceHandAuthored is the only Source value this loader accepts today.
// "extracted:<uuid>" is reserved wire vocabulary for a future extractor
// that turns real captured transcripts into scenarios once consent and
// redaction are wired for it — LoadCorpus refuses it explicitly rather
// than silently accepting a scenario this codebase cannot yet have
// produced safely.
const sourceHandAuthored = "hand_authored"

// extractedSourcePrefix marks the reserved-but-not-yet-supported source.
const extractedSourcePrefix = "extracted:"

// Turn is one prior message in a scenario's conversation history, replayed
// to the candidate model ahead of Input.
type Turn struct {
	Role string `yaml:"role"`
	Text string `yaml:"text"`
}

// Check is one structural assertion run against a candidate's raw output
// text. Kind selects the assertion: "json_schema" (Schema must validate
// per shared/schema.ValidateJSON), "contains"/"not_contains" (Arg is the
// substring), or "min_facts" (Arg is the minimum count, parsed as an
// integer, of elements in the output's top-level JSON object's "facts"
// array — the shape modules/ai's extraction tasks already emit).
type Check struct {
	Kind   string
	Arg    string
	Schema json.RawMessage
}

// checkKnownKeys is Check's yaml key allowlist — decoded by hand (see
// UnmarshalYAML) because Schema's value is an arbitrary nested JSON Schema
// mapping, not a scalar yaml.v3 can decode straight into json.RawMessage;
// enforcing the allowlist here keeps a typo'd key an error, the same
// promise the corpus-wide decoder's KnownFields(true) makes for every
// other field.
var checkKnownKeys = map[string]bool{"kind": true, "arg": true, "schema": true}

// UnmarshalYAML decodes a Check, converting its optional nested "schema"
// mapping into JSON bytes so Check.Schema is directly usable with
// shared/schema.ValidateJSON without a second parse step at check time.
func (c *Check) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("aicert: check: want a mapping, got kind %d at line %d", node.Kind, node.Line)
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !checkKnownKeys[key] {
			return fmt.Errorf("aicert: check: unknown field %q at line %d", key, node.Content[i].Line)
		}
	}
	var raw struct {
		Kind   string    `yaml:"kind"`
		Arg    string    `yaml:"arg"`
		Schema yaml.Node `yaml:"schema"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("aicert: check: %w", err)
	}
	c.Kind = raw.Kind
	c.Arg = raw.Arg
	if raw.Schema.IsZero() {
		return nil
	}
	var v any
	if err := raw.Schema.Decode(&v); err != nil {
		return fmt.Errorf("aicert: check: decoding schema: %w", err)
	}
	rendered, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("aicert: check: schema is not JSON-representable: %w", err)
	}
	c.Schema = rendered
	return nil
}

// Bands are the 0-100 score thresholds a run set is graded against (spec
// §5): CertifiedMin and DegradedMin gate the median score, Floor gates the
// worst single run.
type Bands struct {
	CertifiedMin int `yaml:"certified_min"`
	DegradedMin  int `yaml:"degraded_min"`
	Floor        int `yaml:"floor"`
}

// Caps are the resource ceilings a run is judged against alongside its
// structural/rubric score. P95LatencyMS applies to cloud-served candidates
// only (a local model's latency is a deployment fact, not a certification
// criterion).
type Caps struct {
	P95LatencyMS int64 `yaml:"p95_latency_ms,omitempty"`
	MaxTokens    int   `yaml:"max_tokens,omitempty"`
}

// Expectations is what a scenario's candidate output is graded against.
type Expectations struct {
	Structural []Check `yaml:"structural,omitempty"`
	Rubric     string  `yaml:"rubric,omitempty"`
	Bands      Bands   `yaml:"bands"`
	Caps       Caps    `yaml:"caps,omitempty"`
}

// Scenario is one certification test case, parsed from
// corpus/<task>/<name>.yaml.
type Scenario struct {
	Name        string       `yaml:"name"`
	Task        string       `yaml:"task"`
	Source      string       `yaml:"source"`
	SanitizedBy string       `yaml:"sanitized_by"`
	System      string       `yaml:"system,omitempty"`
	History     []Turn       `yaml:"history,omitempty"`
	Input       string       `yaml:"input"`
	Expect      Expectations `yaml:"expect"`
}

// LoadCorpus reads every *.yaml file under dir (recursively, so a task's
// own subdirectories — e.g. fixture assets that are not themselves
// scenarios — are simply not *.yaml and are skipped) into a Scenario,
// validating each: Task must name a contract task ai.AllTasks() actually
// carries, Source must be "hand_authored" (an "extracted:" scenario is
// refused — see sourceHandAuthored), and SanitizedBy must be non-empty —
// every scenario names who reviewed it for sensitive content before it
// entered the corpus. A malformed or non-conforming file fails the whole
// load: a corpus with one bad scenario is not a corpus a certification run
// can trust the rest of.
func LoadCorpus(dir string) ([]Scenario, error) {
	var scenarios []Scenario
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("aicert: corpus %s: %w", path, err)
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		raw, readErr := os.ReadFile(path) // #nosec G304 G122 -- path is a *.yaml file from walking the trusted corpus tree
		if readErr != nil {
			return fmt.Errorf("aicert: reading %s: %w", path, readErr)
		}
		var sc Scenario
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if decodeErr := dec.Decode(&sc); decodeErr != nil {
			return fmt.Errorf("aicert: parsing %s: %w", path, decodeErr)
		}
		if validateErr := validateScenario(sc, path); validateErr != nil {
			return validateErr
		}
		scenarios = append(scenarios, sc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scenarios, nil
}

// validateScenario enforces the invariants LoadCorpus promises: an
// unknown task, a not-yet-supported or unrecognized source, a missing
// sign-off, or an incoherent set of score bands all fail the load, naming
// both the scenario file and the offending value so a corpus author can
// fix it without re-reading this function.
func validateScenario(sc Scenario, path string) error {
	if !isKnownTask(sc.Task) {
		return fmt.Errorf("aicert: %s: unknown task %q (not in ai.AllTasks())", path, sc.Task)
	}
	if strings.HasPrefix(sc.Source, extractedSourcePrefix) {
		return fmt.Errorf(
			"aicert: %s: source %q refused — extracted scenarios are not yet supported; hand-author it instead (source: %s)",
			path, sc.Source, sourceHandAuthored,
		)
	}
	if sc.Source != sourceHandAuthored {
		return fmt.Errorf("aicert: %s: unknown source %q (want %q)", path, sc.Source, sourceHandAuthored)
	}
	if sc.SanitizedBy == "" {
		return fmt.Errorf("aicert: %s: sanitized_by is required — name who reviewed this scenario for sensitive content", path)
	}
	return validateBands(sc.Expect.Bands, path)
}

// validateBands enforces the §5 ordering Verdict (score.go) relies on:
// CertifiedMin ≤ 100 and ≥ 1 (0 means the author omitted `bands:` entirely,
// which would otherwise auto-Certify every run — every score is a 0-100
// int, so a zero CertifiedMin is never a real threshold, only a forgotten
// one), DegradedMin between 1 and CertifiedMin, and Floor between 0 and
// DegradedMin. A scenario that fails this check would silently defeat the
// score gate rather than measuring anything, so LoadCorpus refuses it
// outright instead of trusting a caller to notice.
func validateBands(b Bands, path string) error {
	if b.CertifiedMin < 1 || b.CertifiedMin > 100 {
		return fmt.Errorf(
			"aicert: %s: expect.bands.certified_min is %d, want 1-100 — bands are required; did you forget the `bands:` block?",
			path, b.CertifiedMin,
		)
	}
	if b.DegradedMin < 1 || b.DegradedMin > b.CertifiedMin {
		return fmt.Errorf(
			"aicert: %s: expect.bands.degraded_min is %d, want 1-%d (at most certified_min)",
			path, b.DegradedMin, b.CertifiedMin,
		)
	}
	if b.Floor < 0 || b.Floor > b.DegradedMin {
		return fmt.Errorf(
			"aicert: %s: expect.bands.floor is %d, want 0-%d (at most degraded_min)",
			path, b.Floor, b.DegradedMin,
		)
	}
	return nil
}

// isKnownTask reports whether task names a task the generated contract
// (ai.AllTasks) actually carries.
func isKnownTask(task string) bool {
	for _, t := range ai.AllTasks() {
		if string(t) == task {
			return true
		}
	}
	return false
}

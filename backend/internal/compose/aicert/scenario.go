// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package aicert is the manual-lane AI certification harness's pure-library
// layer: the scenario corpus format, the §5 verdict math, and the on-disk
// record format. It has no side effects beyond the file I/O its functions
// are named for (LoadCorpus, WriteRecord/LoadRecords) — no time.Now, no
// network, no database — so a certification run is reproducible from a
// corpus, a set of RunResults, and a clock reading the CALLER supplies. The
// runner that drives real model calls lives in this package too, but as its
// own file: this file and its siblings (score.go, record.go) stay callable
// without one.
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

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
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

// JSONValue is a corpus value the harness never reads itself and hands
// straight to the site that owns it: a fixture is whatever that site's
// Prepare takes, and an expected answer is whatever that site's own
// vocabulary spells an answer in.
//
// It is written as YAML and carried as JSON because those are the two
// different jobs: YAML is what a scenario author edits, JSON is what
// CaseFactory.Prepare unmarshals. yaml.v3 cannot decode a nested mapping
// straight into json.RawMessage, so the node is decoded and re-encoded HERE,
// once, at load time — rather than at every read, where two callers could
// disagree about what the bytes meant.
//
// Declaring it as its own type rather than hand-decoding Scenario keeps the
// corpus-wide decoder's KnownFields(true) in force at every OTHER level: a
// typo'd key in expect.bands is still an error, while the arbitrary keys
// inside a fixture are still the site's business.
type JSONValue []byte

// UnmarshalYAML renders one YAML value as the JSON bytes the owning site
// parses. An absent value stays absent — a nil JSONValue, never the four
// bytes "null" — so a scenario that simply omits the block is refused by the
// field's own rule rather than reaching a site's Prepare as a JSON null.
func (v *JSONValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" {
		return nil
	}
	// The shape is the site's, not this package's: decoding through the empty
	// interface is what "carry it through unread" means here.
	var decoded any
	if err := node.Decode(&decoded); err != nil {
		return fmt.Errorf("aicert: value at line %d: %w", node.Line, err)
	}
	rendered, err := json.Marshal(decoded)
	if err != nil {
		return fmt.Errorf("aicert: the value at line %d is not JSON-representable: %w", node.Line, err)
	}
	*v = rendered
	return nil
}

// MarshalJSON emits the value's own JSON bytes, mirroring json.RawMessage —
// without it a []byte-shaped type marshals as base64, and PromptVersion's
// digest would stamp an encoding nobody wrote.
func (v JSONValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
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
// validator verdict and rubric score. P95LatencyMS applies to cloud-served candidates
// only (a local model's latency is a deployment fact, not a certification
// criterion).
type Caps struct {
	P95LatencyMS int64 `yaml:"p95_latency_ms,omitempty"`
	MaxTokens    int   `yaml:"max_tokens,omitempty"`
}

// Expectations is what a scenario's candidate output is graded against.
//
// Outcome names which of the three things a certified reply can be this
// scenario asserts, in the seam's own vocabulary (aitasks.OutcomeAccepted and
// its siblings). Answer is the answer itself, in the site's vocabulary rather
// than a common one: the verdict site takes a bare verdict token because a
// correct reply differs from an incorrect one in that token alone, and a
// wrapper around it would be a second thing to keep true.
type Expectations struct {
	Outcome string    `yaml:"outcome"`
	Answer  JSONValue `yaml:"answer,omitempty"`
	Rubric  string    `yaml:"rubric,omitempty"`
	Bands   Bands     `yaml:"bands"`
	Caps    Caps      `yaml:"caps,omitempty"`
}

// Scenario is one certification test case, parsed from
// corpus/<task>/<name>.yaml.
//
// It carries the DATA production is given, never the prompt production sends:
// Site names which registered invocation site is under certification, and that
// site's own code builds the request from Fixture. A scenario that carried a
// prompt would certify a copy of one, and a copy stays green through the change
// that breaks the original.
type Scenario struct {
	Name        string       `yaml:"name"`
	Task        string       `yaml:"task"`
	Site        string       `yaml:"site"`
	Source      string       `yaml:"source"`
	SanitizedBy string       `yaml:"sanitized_by"`
	Fixture     JSONValue    `yaml:"fixture"`
	Expect      Expectations `yaml:"expect"`
}

// LoadCorpus reads every *.yaml file under dir (recursively, so a task's
// own subdirectories — e.g. fixture assets that are not themselves
// scenarios — are simply not *.yaml and are skipped) into a Scenario,
// validating each: Task must name a contract task ai.AllTasks() actually
// carries, Site must name an invocation site census registers, Source must be
// "hand_authored" (an "extracted:" scenario is refused — see
// sourceHandAuthored), and SanitizedBy must be non-empty — every scenario
// names who reviewed it for sensitive content before it entered the corpus. A
// malformed or non-conforming file fails the whole load: a corpus with one bad
// scenario is not a corpus a certification run can trust the rest of.
//
// The census is required rather than optional because a scenario's site is the
// only thing that says which code certifies it. Without one, every scenario
// naming a site nobody built would load cleanly and fail on a paid run.
func LoadCorpus(dir string, census *aitasks.Registry) ([]Scenario, error) {
	if census == nil {
		return nil, fmt.Errorf("aicert: corpus %s: no census supplied — a scenario names the site that certifies it, and only the census says which sites this build has", dir)
	}
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
		if validateErr := validateScenario(sc, path, census); validateErr != nil {
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

// validateScenario enforces the invariants LoadCorpus promises: an unknown
// task, a site this build does not register, a missing fixture, an outcome
// outside the seam's vocabulary, a not-yet-supported or unrecognized source, a
// missing sign-off, or an incoherent set of score bands all fail the load,
// naming both the scenario file and the offending value so a corpus author can
// fix it without re-reading this function.
func validateScenario(sc Scenario, path string, census *aitasks.Registry) error {
	if !isKnownTask(sc.Task) {
		return fmt.Errorf("aicert: %s: unknown task %q (not in ai.AllTasks())", path, sc.Task)
	}
	if sc.Site == "" {
		return fmt.Errorf(
			"aicert: %s: names no site — a scenario certifies ONE invocation site, so `site:` carries the variant the task contract declares",
			path)
	}
	if _, registered := census.Lookup(ai.Task(sc.Task), sc.Site); !registered {
		return fmt.Errorf(
			"aicert: %s: site %s/%s is not registered by this build (fix the site name, or register the site in NewTaskCensus)",
			path, sc.Task, sc.Site)
	}
	if len(sc.Fixture) == 0 {
		return fmt.Errorf(
			"aicert: %s: carries no fixture — the corpus holds the data production is GIVEN, and the site's own code builds the request from it",
			path)
	}
	if err := validateOutcome(sc.Expect.Outcome, path); err != nil {
		return err
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

// validateOutcome holds expect.outcome to the four things a certified reply can
// be. The vocabulary is the seam's own (aitasks), not a corpus-side copy: a
// scenario expecting a fifth word would assert something no Evaluate can ever
// report, and would read as an unmet expectation forever.
func validateOutcome(outcome, path string) error {
	if aitasks.KnownOutcome(outcome) {
		return nil
	}
	return fmt.Errorf("aicert: %s: expect.outcome is %q, want one of %s|%s|%s|%s",
		path, outcome,
		aitasks.OutcomeAccepted, aitasks.OutcomeWrongAnswer, aitasks.OutcomeInvalid, aitasks.OutcomeAbstained)
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

// scenarioForRender mirrors Scenario for OUTPUT only. It exists because
// Scenario carries its fixture as JSONValue — a []byte, which yaml.Marshal
// renders as a list of byte values rather than as the mapping it holds.
// Fixture and Answer are `any` here so the JSON is decoded back into a plain
// value and emits as the YAML a human edits and LoadScenarioFile reads back.
type scenarioForRender struct {
	Name        string          `yaml:"name"`
	Task        string          `yaml:"task"`
	Site        string          `yaml:"site"`
	Source      string          `yaml:"source"`
	SanitizedBy string          `yaml:"sanitized_by"`
	Fixture     any             `yaml:"fixture"`
	Expect      expectForRender `yaml:"expect"`
}

type expectForRender struct {
	Outcome string `yaml:"outcome"`
	Answer  any    `yaml:"answer,omitempty"`
	Rubric  string `yaml:"rubric,omitempty"`
	Bands   Bands  `yaml:"bands"`
	Caps    Caps   `yaml:"caps,omitempty"`
}

// RenderScenario emits a scenario as the YAML the corpus format uses, suitable
// as a starting point an operator edits. The round trip is the contract:
// whatever this writes, LoadScenarioFile must read back.
func RenderScenario(sc Scenario) ([]byte, error) {
	out := scenarioForRender{
		Name: sc.Name, Task: sc.Task, Site: sc.Site,
		Source: sc.Source, SanitizedBy: sc.SanitizedBy,
		Expect: expectForRender{
			Outcome: sc.Expect.Outcome, Rubric: sc.Expect.Rubric,
			Bands: sc.Expect.Bands, Caps: sc.Expect.Caps,
		},
	}
	if err := json.Unmarshal(sc.Fixture, &out.Fixture); err != nil {
		return nil, fmt.Errorf("aicert: the fixture of %s is not decodable JSON: %w", sc.Name, err)
	}
	if len(sc.Expect.Answer) > 0 {
		if err := json.Unmarshal(sc.Expect.Answer, &out.Expect.Answer); err != nil {
			return nil, fmt.Errorf("aicert: the expected answer of %s is not decodable JSON: %w", sc.Name, err)
		}
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("aicert: rendering %s: %w", sc.Name, err)
	}
	return body, nil
}

// LoadScenarioFile reads ONE scenario file in the corpus format — the debug
// lane's entry point, distinct from LoadCorpus.
//
// It deliberately does NOT apply the corpus's admission rules. Source and
// SanitizedBy gate what may ENTER the committed corpus; a scratch scenario an
// operator is probing with is not entering it, and demanding a provenance
// stamp for a throwaway would only teach people to type a false one.
// Everything that says what to RUN is still checked: the site must be one this
// build registers, and the fixture must exist.
func LoadScenarioFile(path string, census *aitasks.Registry) (Scenario, error) {
	if census == nil {
		return Scenario{}, fmt.Errorf("aicert: %s: no census supplied — a scenario names the site that runs it", path)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- an operator-named scratch scenario is the point of the debug lane
	if err != nil {
		return Scenario{}, fmt.Errorf("aicert: reading %s: %w", path, err)
	}
	var sc Scenario
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil {
		return Scenario{}, fmt.Errorf("aicert: %s is not a scenario: %w", path, err)
	}
	if _, ok := census.Lookup(ai.Task(sc.Task), sc.Site); !ok {
		return Scenario{}, fmt.Errorf("aicert: %s names site %s/%s, which this build does not register", path, sc.Task, sc.Site)
	}
	if len(sc.Fixture) == 0 {
		return Scenario{}, fmt.Errorf("aicert: %s carries no fixture, so there is nothing to give the site", path)
	}
	return sc, nil
}

// ScenarioRenderShape and ExpectRenderShape expose the render-only mirrors so a
// test can hold their field sets against the types they mirror. A dropped field
// would otherwise break RenderScenario's round-trip contract silently.
func ScenarioRenderShape() any { return scenarioForRender{} }

// ExpectRenderShape is ScenarioRenderShape's sibling for the expectation half.
func ExpectRenderShape() any { return expectForRender{} }

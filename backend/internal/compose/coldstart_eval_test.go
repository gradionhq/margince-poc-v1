// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cold_start golden-dataset gate (B-EP06.23a): every recorded case
// in evals/cold_start runs through the REAL deterministic gates — the
// §5.2 shape check and the no-guess evidence gate — and must land
// EXACTLY on its expected survivors. Zero tolerance: one ungrounded
// field surviving one adversarial case fails the build, and a corpus
// that shrinks below the ticket's minimums fails too (a deleted case
// class reads as "covered" otherwise). Runs in the plain test lane, so
// `make check` IS the hard gate (ai-operational-spec §3.3: recorded
// fixtures, never live model calls).

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const evalDir = "../../../evals/cold_start"

type evalCase struct {
	Name   string `json:"name"`
	Class  string `json:"class"`
	Inputs struct {
		SourceURL   string `json:"source_url"`
		PageText    string `json:"page_text"`
		ModelOutput string `json:"model_output"`
	} `json:"inputs"`
	Expected struct {
		ShapeValid bool              `json:"shape_valid"`
		Survivors  map[string]string `json:"survivors"`
	} `json:"expected"`
	Rubric string `json:"rubric"`
}

type evalThresholds struct {
	MinCases       int      `json:"min_cases"`
	MinLongTail    int      `json:"min_long_tail"`
	MinAdversarial int      `json:"min_adversarial"`
	Classes        []string `json:"classes"`
}

func loadEvalCorpus(t *testing.T) ([]evalCase, evalThresholds) {
	t.Helper()
	rawThresholds, err := os.ReadFile(filepath.Join(evalDir, "thresholds.json"))
	if err != nil {
		t.Fatalf("the eval corpus is part of the merge gate: %v", err)
	}
	var thresholds evalThresholds
	if err := json.Unmarshal(rawThresholds, &thresholds); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(filepath.Join(evalDir, "cases", "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no case files under %s/cases (%v)", evalDir, err)
	}
	var cases []evalCase
	for _, file := range files {
		f, err := os.Open(file) // #nosec G304 -- repo-relative corpus path from filepath.Glob, not caller input
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			var c evalCase
			if err := json.Unmarshal(scanner.Bytes(), &c); err != nil {
				t.Fatalf("%s: malformed case line: %v", file, err)
			}
			cases = append(cases, c)
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return cases, thresholds
}

func TestColdStartGoldenDatasetShape(t *testing.T) {
	cases, thresholds := loadEvalCorpus(t)

	perClass := map[string]int{}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.Name] {
			t.Errorf("duplicate case name %s", c.Name)
		}
		seen[c.Name] = true
		perClass[c.Class]++
		if c.Rubric == "" {
			t.Errorf("case %s carries no rubric — a case must say what it proves", c.Name)
		}
	}
	if len(cases) < thresholds.MinCases {
		t.Errorf("corpus shrank to %d cases (< %d) — the golden set is version-controlled, not optional", len(cases), thresholds.MinCases)
	}
	if perClass["long_tail"] < thresholds.MinLongTail {
		t.Errorf("long-tail cases = %d, want ≥ %d", perClass["long_tail"], thresholds.MinLongTail)
	}
	if perClass["adversarial"] < thresholds.MinAdversarial {
		t.Errorf("adversarial cases = %d, want ≥ %d", perClass["adversarial"], thresholds.MinAdversarial)
	}
	for _, class := range thresholds.Classes {
		if perClass[class] == 0 {
			t.Errorf("class %q vanished from the corpus", class)
		}
	}
}

func TestColdStartGoldenDataset(t *testing.T) {
	cases, _ := loadEvalCorpus(t)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			shapeErr := extractionShapeValid(c.Inputs.ModelOutput)
			if c.Expected.ShapeValid && shapeErr != nil {
				t.Fatalf("shape gate refused a recorded reply it must accept: %v\nrubric: %s", shapeErr, c.Rubric)
			}
			if !c.Expected.ShapeValid && shapeErr == nil {
				t.Fatalf("shape gate accepted a malformed reply\nrubric: %s", c.Rubric)
			}

			survivors := evidencedFields(c.Inputs.ModelOutput, c.Inputs.PageText, c.Inputs.SourceURL)
			got := map[string]string{}
			for _, f := range survivors {
				got[string(f.Field)] = f.Value
				if f.EvidenceSnippet == "" || f.SourceUrl != c.Inputs.SourceURL {
					t.Errorf("survivor %s lost its provenance (snippet=%q url=%q)", f.Field, f.EvidenceSnippet, f.SourceUrl)
				}
			}
			if len(got) != len(c.Expected.Survivors) {
				t.Fatalf("survivors = %v, want %v\nrubric: %s", got, c.Expected.Survivors, c.Rubric)
			}
			for name, value := range c.Expected.Survivors {
				if got[name] != value {
					t.Fatalf("survivor %s = %q, want %q\nrubric: %s", name, got[name], value, c.Rubric)
				}
			}
		})
	}
}

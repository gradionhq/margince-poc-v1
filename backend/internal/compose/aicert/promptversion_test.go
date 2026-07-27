// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// A certification record is a claim about specific scenarios. These tests keep
// that claim falsifiable: the stamp must move when any part of the claim moves,
// and a committed record whose stamp does not match this corpus must be
// reported as no longer describing what ships.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
)

// Every part of a scenario changes what a score MEANS: the fixture is the data
// the site is given, the expected answer decides what "right" is, and the bands
// decide what passes. A stamp that moved for only one of them would leave the
// other two able to change under a record still claiming to cover them.
func TestPromptVersionMovesWithEveryPartOfTheClaim(t *testing.T) {
	base := Scenario{
		Name:    "one",
		Fixture: JSONValue(`{"page":"the page"}`),
		Expect: Expectations{
			Outcome: "accepted",
			Answer:  JSONValue(`"noise"`),
			Rubric:  "Score the grounding.",
			Bands:   Bands{CertifiedMin: 70, DegradedMin: 50, Floor: 40},
		},
	}
	stamp := PromptVersion([]Scenario{base})

	edits := map[string]func(sc *Scenario){
		"the fixture": func(sc *Scenario) { sc.Fixture = JSONValue(`{"page":"a different page"}`) },
		"the expected answer": func(sc *Scenario) {
			sc.Expect.Answer = JSONValue(`"real"`)
		},
		"the rubric": func(sc *Scenario) { sc.Expect.Rubric = "Score the grounding leniently." },
		"the bands":  func(sc *Scenario) { sc.Expect.Bands.CertifiedMin = 60 },
		"the caps":   func(sc *Scenario) { sc.Expect.Caps.MaxTokens = 300 },
	}
	for what, edit := range edits {
		t.Run(what, func(t *testing.T) {
			edited := base
			edit(&edited)
			if PromptVersion([]Scenario{edited}) == stamp {
				t.Fatalf("editing %s kept the certification stamp — a stale record could not be detected", what)
			}
		})
	}
}

func TestPromptVersionIsOrderIndependent(t *testing.T) {
	a := Scenario{Name: "a", Site: "one", Fixture: JSONValue(`{"page":"a"}`)}
	b := Scenario{Name: "b", Site: "two", Fixture: JSONValue(`{"page":"b"}`)}
	if PromptVersion([]Scenario{a, b}) != PromptVersion([]Scenario{b, a}) {
		t.Fatal("the stamp depends on scenario order, so a reordered corpus reads as a scenario change")
	}
}

// The staleness report: which committed records were scored against prompts
// this corpus no longer contains. It is a test rather than prose in a status
// file so the answer is computed from the tree, and it FAILS while any record
// still claims to cover prompts that changed — the fix is a re-certification
// run (see records/README.md), not an edit here.
func TestEveryCommittedRecordNamesTheCurrentPromptVersion(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	scenarios, err := LoadCorpus("corpus", census)
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	byTask := map[string][]Scenario{}
	for _, sc := range scenarios {
		byTask[string(sc.Task)] = append(byTask[string(sc.Task)], sc)
	}
	records, err := LoadRecords("records")
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	var stale []string
	for _, rec := range records {
		current, ok := byTask[rec.Task]
		if !ok {
			// A record for a task with no scenarios cannot be checked against
			// anything; the corpus-coverage test owns that case.
			continue
		}
		if want := PromptVersion(current); rec.PromptVersion != want {
			stale = append(stale, rec.Task+"/"+rec.Provider+"_"+rec.ServedModel+
				" was certified against prompt "+rec.PromptVersion+", this corpus is "+want)
		}
	}
	if len(stale) > 0 {
		t.Errorf("these certification records were scored against prompts that have since changed, so they no longer describe what ships — re-run certification for these tasks:\n  %s",
			strings.Join(stale, "\n  "))
	}
}

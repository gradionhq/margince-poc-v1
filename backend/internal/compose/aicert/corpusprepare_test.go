// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The shipped corpus's second self-test: every committed scenario must PREPARE
// against the case its site binds. LoadCorpus proves a scenario parses and names
// a registered site; only Prepare proves the fixture is the shape that site takes
// and the expectation is one its validator could ever satisfy. A scenario that
// fails here measures nothing, and the paid lane is where that would otherwise be
// discovered — one run per scenario, per repeat.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestEveryCorpusScenarioPreparesAgainstItsSite(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	scenarios, err := aicert.LoadCorpus("corpus", census)
	if err != nil {
		t.Fatalf("LoadCorpus(corpus): %v", err)
	}
	for _, sc := range scenarios {
		t.Run(sc.Task+"/"+sc.Site+"/"+sc.Name, func(t *testing.T) {
			factory, bound := census.CaseFor(ai.Task(sc.Task), sc.Site)
			if !bound {
				t.Fatalf("site %s/%s binds no certification case", sc.Task, sc.Site)
			}
			if _, err := factory.Prepare(json.RawMessage(sc.Fixture), json.RawMessage(sc.Expect.Answer)); err != nil {
				t.Fatalf("Prepare: %v", err)
			}
		})
	}
}

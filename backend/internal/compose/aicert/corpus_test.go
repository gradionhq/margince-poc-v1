// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

// The shipped corpus's own self-test: no e2e_llm build tag, no network, no
// model call — LoadCorpus is a pure parse over the committed corpus/ tree
// (aicert.LoadCorpus's own doc: "no time.Now, no network, no database").
// The obligation follows the contract's status, in both directions: a
// shipped SITE without a scenario has nothing to certify, and a planned task
// WITH one scores a prompt that does not ship — which reads as coverage it has
// not earned. Both are derived from ai-tasks.yaml rather than a maintained
// list, the same way arch_test.go's fitness tests derive their obligations
// from the tree.
//
// The unit is the site, not the task, because a task is not one prompt: rate
// extraction has two sites, cold start four, and voice building three. A
// per-task obligation let one scenario stand for all of a task's sites, so a
// site could ship with its prompt never once scored while the corpus reported
// the task covered.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/aicert"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

func TestLoadCorpusCoversEveryShippedSite(t *testing.T) {
	census, err := compose.NewTaskCensus()
	if err != nil {
		t.Fatalf("building the task census: %v", err)
	}
	scenarios, err := aicert.LoadCorpus("corpus", census)
	if err != nil {
		t.Fatalf("LoadCorpus(corpus): %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("the shipped corpus loaded zero scenarios")
	}

	sitesSeen := map[string]int{}
	tasksSeen := map[string]int{}
	for _, sc := range scenarios {
		sitesSeen[sc.Task+"/"+sc.Site]++
		tasksSeen[sc.Task]++
	}

	var missing, unexpected []string
	for _, task := range ai.AllTasks() {
		switch ai.Status(task) {
		case ai.StatusShipped:
			for _, site := range ai.SitesFor(task) {
				if sitesSeen[string(task)+"/"+site.Name] == 0 {
					missing = append(missing, string(task)+"/"+site.Name)
				}
			}
		case ai.StatusPlanned:
			if tasksSeen[string(task)] > 0 {
				unexpected = append(unexpected, string(task))
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("shipped sites with no corpus scenario: %v — each is a prompt that ships uncertified", missing)
	}
	if len(unexpected) > 0 {
		t.Errorf("planned tasks carry corpus scenarios: %v — a task nobody built cannot be certified, and its scenario reads as coverage", unexpected)
	}
}

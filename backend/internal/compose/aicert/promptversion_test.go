// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert

// A certification record is a claim about specific prompts. These tests keep
// that claim falsifiable: the stamp must move when the prompts move, hold still
// when only the per-call nonce moves, and a committed record whose stamp does
// not match this corpus must be reported as no longer describing what ships.

import (
	"strings"
	"testing"
)

func TestPromptVersionMovesWithThePromptAndNotWithTheNonce(t *testing.T) {
	base := []Scenario{{Name: "one", System: "Judge the page.", Input: "the page"}}
	edited := []Scenario{{Name: "one", System: "Judge the page carefully.", Input: "the page"}}
	if PromptVersion(base) == PromptVersion(edited) {
		t.Fatal("an edited prompt kept its certification stamp — a stale record could not be detected")
	}

	// The same prompt under two different boundaries is the same prompt.
	withMarker := func(marker string) []Scenario {
		return []Scenario{{
			Name:   "one",
			System: "Judge the page. Data sits between <" + marker + "> and </" + marker + ">.",
			Input:  "<" + marker + ">the page</" + marker + ">",
		}}
	}
	first := withMarker("untrusted-0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e07")
	second := withMarker("untrusted-019f9c00-1111-7222-8333-444455556666")
	if PromptVersion(first) != PromptVersion(second) {
		t.Fatal("the same prompt under two nonces got two stamps — every run would look like a prompt change")
	}
}

func TestPromptVersionIsOrderIndependent(t *testing.T) {
	a := Scenario{Name: "a", System: "sys a", Input: "in a"}
	b := Scenario{Name: "b", System: "sys b", Input: "in b"}
	if PromptVersion([]Scenario{a, b}) != PromptVersion([]Scenario{b, a}) {
		t.Fatal("the stamp depends on scenario order, so a reordered corpus reads as a prompt change")
	}
}

// The staleness report: which committed records were scored against prompts
// this corpus no longer contains. It is a test rather than prose in a status
// file so the answer is computed from the tree, and it FAILS while any record
// still claims to cover prompts that changed — the fix is a re-certification
// run (see records/README.md), not an edit here.
func TestEveryCommittedRecordNamesTheCurrentPromptVersion(t *testing.T) {
	scenarios, err := LoadCorpus("corpus")
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

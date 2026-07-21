// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aicert_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

func TestRunChecksContains(t *testing.T) {
	checks := []aicert.Check{{Kind: "contains", Arg: "won"}}
	if passed, failures := aicert.RunChecks(checks, "the deal was won"); !passed || len(failures) != 0 {
		t.Fatalf("passed=%v failures=%v, want true, none", passed, failures)
	}
	if passed, failures := aicert.RunChecks(checks, "the deal was lost"); passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure", passed, failures)
	}
}

func TestRunChecksNotContains(t *testing.T) {
	checks := []aicert.Check{{Kind: "not_contains", Arg: "TODO"}}
	if passed, _ := aicert.RunChecks(checks, "clean output"); !passed {
		t.Fatal("want pass when the forbidden substring is absent")
	}
	if passed, failures := aicert.RunChecks(checks, "TODO: fix this"); passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure", passed, failures)
	}
}

func TestRunChecksMinFacts(t *testing.T) {
	checks := []aicert.Check{{Kind: "min_facts", Arg: "2"}}
	if passed, _ := aicert.RunChecks(checks, `{"facts":[{"a":1},{"b":2}]}`); !passed {
		t.Fatal("want pass with exactly the minimum count of facts")
	}
	if passed, failures := aicert.RunChecks(checks, `{"facts":[{"a":1}]}`); passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure (below minimum)", passed, failures)
	}
	if passed, failures := aicert.RunChecks(checks, `not json`); passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure (invalid JSON)", passed, failures)
	}
}

func TestRunChecksMinFactsRejectsANonIntegerArg(t *testing.T) {
	checks := []aicert.Check{{Kind: "min_facts", Arg: "two"}}
	if passed, failures := aicert.RunChecks(checks, `{"facts":[]}`); passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure (bad arg)", passed, failures)
	}
}

func TestRunChecksJSONSchema(t *testing.T) {
	checks := []aicert.Check{{Kind: "json_schema", Schema: []byte(`{
		"type": "object",
		"properties": {"summary": {"type": "string"}},
		"required": ["summary"],
		"additionalProperties": false
	}`)}}
	if passed, _ := aicert.RunChecks(checks, `{"summary":"looks good"}`); !passed {
		t.Fatal("want pass for output conforming to the schema")
	}
	if passed, failures := aicert.RunChecks(checks, `{"other":"field"}`); passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure", passed, failures)
	}
}

func TestRunChecksUnknownKindIsAFailureNotASilentPass(t *testing.T) {
	checks := []aicert.Check{{Kind: "not_a_real_check"}}
	passed, failures := aicert.RunChecks(checks, "anything")
	if passed || len(failures) != 1 {
		t.Fatalf("passed=%v failures=%v, want false, one failure", passed, failures)
	}
	if !strings.Contains(failures[0], "not_a_real_check") {
		t.Fatalf("failure %q does not name the unknown kind", failures[0])
	}
}

func TestRunChecksCollectsEveryFailureNotJustTheFirst(t *testing.T) {
	checks := []aicert.Check{
		{Kind: "contains", Arg: "won"},
		{Kind: "not_contains", Arg: "lost"},
	}
	passed, failures := aicert.RunChecks(checks, "the deal was lost")
	if passed || len(failures) != 2 {
		t.Fatalf("passed=%v failures=%v, want false, two failures", passed, failures)
	}
}

func TestRunChecksOnAnEmptySetPasses(t *testing.T) {
	passed, failures := aicert.RunChecks(nil, "anything")
	if !passed || len(failures) != 0 {
		t.Fatalf("passed=%v failures=%v, want true, none", passed, failures)
	}
}

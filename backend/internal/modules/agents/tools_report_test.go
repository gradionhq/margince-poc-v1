// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The rendered description is what an agent actually reads, so the enum, the
// three vocabularies and the pipeline pointer have to survive rendering — not
// merely exist on the struct the composition root hands over.
func TestRunReportDescribesEveryReportAndItsVocabulary(t *testing.T) {
	described := describeReportCatalog(probeReportCatalog)
	for _, entry := range probeReportCatalog {
		if !strings.Contains(described, entry.Report) {
			t.Errorf("the description never names %q", entry.Report)
		}
		for _, names := range [][]string{entry.GroupBy, entry.Filters, entry.Aggregates} {
			for _, name := range names {
				if !strings.Contains(described, name) {
					t.Errorf("%s: the description never names %q", entry.Report, name)
				}
			}
		}
		if entry.Defaults != "" && !strings.Contains(described, entry.Defaults) {
			t.Errorf("%s: the description never says what it answers by default", entry.Report)
		}
	}
	// The obligation the registry walk enforces for every tool naming a stage
	// or pipeline id: say where one comes from.
	if !strings.Contains(described, "list_pipelines") {
		t.Error("the vocabularies name pipeline_id/stage_id and the description points nowhere for them")
	}
}

// The `report` argument is closed to the catalog's keys, so a caller reads the
// answer instead of guessing it — and the schema stays valid JSON either way.
func TestRunReportClosesTheReportArgumentToTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		name     string
		catalog  []ReportCatalogEntry
		wantEnum []string
	}{
		{"a catalog closes the argument", probeReportCatalog, []string{"deals-by-stage"}},
		{"an empty catalog omits the enum rather than emitting an unsatisfiable one", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Decoded into a checked shape rather than walked with bare type
			// assertions: a schema regression should FAIL this test, not panic
			// inside it, and a panic reports the wrong thing about the wrong line.
			var parsed struct {
				Properties struct {
					Report struct {
						Enum []string `json:"enum"`
					} `json:"report"`
				} `json:"properties"`
			}
			if err := json.Unmarshal(runReport{catalog: tc.catalog}.Spec().InputSchema, &parsed); err != nil {
				t.Fatalf("InputSchema is not valid JSON, or `report` is not the shape this asserts: %v", err)
			}
			if !slices.Equal(parsed.Properties.Report.Enum, tc.wantEnum) {
				t.Errorf("report enum = %v, want %v", parsed.Properties.Report.Enum, tc.wantEnum)
			}
		})
	}
}

// An empty vocabulary says so. A blank reads as an omission, and a caller who
// reads it that way sends a plausible name into an argument that accepts none.
func TestRunReportNamesAnEmptyVocabularyRatherThanLeavingABlank(t *testing.T) {
	described := describeReportCatalog([]ReportCatalogEntry{{Report: "activities-by-kind"}})
	if !strings.Contains(described, "(none)") {
		t.Errorf("an empty vocabulary rendered as a blank:\n%s", described)
	}
}

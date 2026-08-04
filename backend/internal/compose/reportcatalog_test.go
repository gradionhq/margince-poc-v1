// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"slices"
	"testing"
)

// The catalog is published so an agent can call run_report without guessing, so
// what it publishes has to be what the engine accepts. Both halves are derived
// from prebuiltReports — but from DIFFERENT fields of it, and a name rendered
// under the wrong heading sends a caller to put a filter key in group_by and
// read a refusal for it.
//
// Derived from the catalog rather than listed here, so a report added to the
// engine inherits the check instead of quietly escaping it.
func TestReportToolCatalogPublishesOnlyWhatTheEngineAccepts(t *testing.T) {
	catalog := reportToolCatalog()
	if len(catalog) != len(prebuiltReports) {
		t.Fatalf("catalog has %d entries, the engine has %d prebuilt reports", len(catalog), len(prebuiltReports))
	}
	for _, entry := range catalog {
		spec, served := prebuiltReports[entry.Report]
		if !served {
			t.Errorf("catalog advertises %q, which the engine does not serve", entry.Report)
			continue
		}
		for _, name := range entry.GroupBy {
			if _, ok := spec.dimensions[name]; !ok {
				t.Errorf("%s: group_by advertises %q, not a dimension of that report", entry.Report, name)
			}
		}
		for _, name := range entry.Filters {
			if _, ok := spec.filters[name]; !ok {
				t.Errorf("%s: filters advertises %q, not a filter of that report", entry.Report, name)
			}
		}
		for _, name := range entry.Aggregates {
			if _, ok := spec.measures[name]; !ok {
				t.Errorf("%s: aggregate fields advertises %q, not a measure of that report", entry.Report, name)
			}
		}
	}
}

// The other direction: a vocabulary the engine accepts and the catalog omits is
// a capability no caller can reach, which is the same dead end in reverse.
func TestReportToolCatalogOmitsNothingTheEngineAccepts(t *testing.T) {
	byReport := make(map[string]ReportCatalogEntryView, len(prebuiltReports))
	for _, entry := range reportToolCatalog() {
		byReport[entry.Report] = ReportCatalogEntryView{entry.GroupBy, entry.Filters, entry.Aggregates}
	}
	for report, spec := range prebuiltReports {
		published, ok := byReport[report]
		if !ok {
			t.Errorf("the engine serves %q and the catalog never names it", report)
			continue
		}
		assertCovers(t, report, "group_by", spec.dimensions, published.GroupBy)
		assertCovers(t, report, "filters", spec.filters, published.Filters)
		assertCovers(t, report, "aggregate fields", spec.measures, published.Aggregates)
	}
}

// ReportCatalogEntryView is the three vocabularies of one entry, so the
// coverage walk above reads as three comparisons rather than nine lines.
type ReportCatalogEntryView struct{ GroupBy, Filters, Aggregates []string }

func assertCovers(t *testing.T, report, heading string, engine map[string]string, published []string) {
	t.Helper()
	for name := range engine {
		if !slices.Contains(published, name) {
			t.Errorf("%s: the engine accepts %s %q and the catalog does not publish it", report, heading, name)
		}
	}
}

// A report with default aggregates and no default grouping must not render a
// sentence that trails off. runAdHocPlan already builds that spec shape, so it
// is one prebuilt report away from being reachable.
func TestReportDefaultsRenderEachHalfOnlyWhenItExists(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec reportSpec
		want string
	}{
		{
			name: "aggregates and grouping",
			spec: reportSpec{defaultBy: []string{"stage_id"}, defaultAggs: []reportAggregate{{Fn: "count", As: "deals"}}},
			want: "count as deals grouped by stage_id",
		},
		{
			name: "aggregates with no grouping",
			spec: reportSpec{defaultAggs: []reportAggregate{{Fn: "count", As: "deals"}}},
			want: "count as deals over the whole set",
		},
		{
			name: "grouping with no aggregates",
			spec: reportSpec{defaultBy: []string{"kind"}},
			want: "grouped by kind",
		},
		{
			name: "neither",
			spec: reportSpec{},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeReportDefaults(tc.spec); got != tc.want {
				t.Errorf("defaults = %q, want %q", got, tc.want)
			}
		})
	}
}

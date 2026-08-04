// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the run_report TOOL tells a caller about the report catalog. Separate
// from report.go because it answers a different question: that file is the
// engine — which SQL each report compiles to — and this one is the surface, the
// vocabulary a caller may send. They change for different reasons, and the
// engine file sits at the package's file-length cap precisely because it has
// only one concern in it.

import (
	"maps"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
)

// reportToolCatalog publishes the prebuilt catalog to the run_report tool: the
// keys and, per key, the three vocabularies the engine will accept.
//
// DERIVED from prebuiltReports rather than listed, so a report added to the
// engine describes itself on the tool surface and one deleted stops being
// advertised. The tool previously named three keys in an "e.g." and called the
// rest "the report's vocabulary" — a phrase pointing at something no tool
// yielded, leaving a caller to discover four keys and their words by refusal.
//
// Everything is sorted: the map is not ordered, and a description that
// reshuffles per boot reads as a changed tool to a client that caches it.
func reportToolCatalog() []agents.ReportCatalogEntry {
	catalog := make([]agents.ReportCatalogEntry, 0, len(prebuiltReports))
	for report, spec := range prebuiltReports {
		catalog = append(catalog, agents.ReportCatalogEntry{
			Report:     report,
			GroupBy:    slices.Sorted(maps.Keys(spec.dimensions)),
			Filters:    slices.Sorted(maps.Keys(spec.filters)),
			Aggregates: slices.Sorted(maps.Keys(spec.measures)),
			Defaults:   describeReportDefaults(spec),
		})
	}
	slices.SortFunc(catalog, func(a, b agents.ReportCatalogEntry) int {
		return strings.Compare(a.Report, b.Report)
	})
	return catalog
}

// describeReportDefaults renders what a report answers with no plan arguments,
// which is the call a caller should make first and the one they cannot see from
// the vocabularies alone.
func describeReportDefaults(spec reportSpec) string {
	aggs := make([]string, 0, len(spec.defaultAggs))
	for _, agg := range spec.defaultAggs {
		rendered := agg.Fn
		if agg.Field != "" {
			rendered += "(" + agg.Field + ")"
		}
		if agg.As != "" {
			rendered += " as " + agg.As
		}
		aggs = append(aggs, rendered)
	}
	// Each half is rendered only if it EXISTS. runAdHocPlan already builds a spec
	// with default aggregates and no default grouping (report.go), so an `&&`
	// guard here would ship "count as deals grouped by " to an agent the first
	// time such a report joined the prebuilt catalog.
	switch {
	case len(aggs) > 0 && len(spec.defaultBy) > 0:
		return strings.Join(aggs, ", ") + " grouped by " + strings.Join(spec.defaultBy, ", ")
	case len(aggs) > 0:
		return strings.Join(aggs, ", ") + " over the whole set"
	case len(spec.defaultBy) > 0:
		return "grouped by " + strings.Join(spec.defaultBy, ", ")
	default:
		return ""
	}
}

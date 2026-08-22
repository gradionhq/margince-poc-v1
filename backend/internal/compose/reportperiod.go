// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The period bucket vocabulary (reporting REPORT-PARAM-4..7) and win-loss
// (REPORT-KEY-8 / REPORT-VOCAB-1), its first consumer. They live together
// because they ship and change together: the bucket has exactly one caller
// today, and a shared vocabulary with one caller is a file two readers have to
// open to answer one question. A second key adopting periods is what splits
// them.
//
// Separate from report.go because that file sits at the package's file-length
// cap, which reportcatalog.go's header already records as deliberate.

import (
	"maps"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

const (
	// colClosedAt is win-loss's period anchor: the instant a deal was actually
	// won or lost. The anchor is the KEY's to declare, never the caller's to
	// choose (REPORT-PARAM-7) — a caller who could pick it could produce two
	// reports that disagree and both look right. It is NOT NULL for every row
	// this report reads, guaranteed by the deal_closed_at CHECK.
	colClosedAt = "t.closed_at"
	colSource   = "t.source"

	fieldSource        = "source"
	fieldPeriodYear    = "period_year"
	fieldPeriodQuarter = "period_quarter"
	fieldPeriodMonth   = "period_month"

	// The three grains' to_char patterns (REPORT-PARAM-5). Q is Postgres's
	// quarter-of-year; the "-Q" is a quoted literal inside the pattern, not a
	// second field. Each renders a value that is BOTH what a reader sees and
	// what sorts: '2025-Q4' < '2026-Q1' lexically as well as chronologically,
	// so a period report needs no separate sort key.
	patternYear    = "YYYY"
	patternQuarter = `YYYY"-Q"Q`
	patternMonth   = "YYYY-MM"
)

// periodBucketExpr renders one grain over one anchor.
//
// The value is TEXT, and that is the load-bearing choice rather than a
// formatting preference. A derivation handle carries a group key out through a
// URL query string and binds it back as an equality predicate against this very
// expression, so a bucket value has to survive that round trip exactly. A
// timestamp does not: it leaves as a driver-native instant and returns as
// whatever the transport's default rendering produced, which Postgres is under
// no obligation to parse back. Text makes the round trip total instead of
// incidental, which is what keeps "Explain This Number" working on the reports
// most likely to be doubted.
//
// The bucket is cut in the installation's reporting zone (REPORT-PARAM-6,
// data-semantics §2 r4) — the same zone the forecast's "today" uses, and for
// the same reason: a year that begins at midnight in Berlin is not a year that
// begins at midnight in UTC, and a total that moves with the deployment region
// is the fast-but-wrong number reporting exists to refuse. The zone arrives as
// a bind parameter through reportZoneToken, so this stays a static expression
// with no bind position of its own to name.
func periodBucketExpr(anchor, pattern string) string {
	return "to_char(timezone(" + reportZoneToken + ", " + anchor + "), '" + pattern + "')"
}

// periodDimensions is the three grains over one anchor, keyed by their wire
// names (REPORT-PARAM-4). The set is closed: a caller asking for a grain
// outside it is refused by the ordinary out-of-vocabulary path, never widened.
func periodDimensions(anchor string) map[string]string {
	return map[string]string{
		fieldPeriodYear:    periodBucketExpr(anchor, patternYear),
		fieldPeriodQuarter: periodBucketExpr(anchor, patternQuarter),
		fieldPeriodMonth:   periodBucketExpr(anchor, patternMonth),
	}
}

// withProjectFilter adds the project_id filter a deal-based report carries:
// every deal report answers "for this project" the same way.
func withProjectFilter(filters map[string]string) map[string]string {
	filters[fieldProjectID] = colProjectID
	return filters
}

// winLossSpec is REPORT-KEY-8's vocabulary, pinned upstream as REPORT-VOCAB-1.
//
// The grain is one row per deal, so a deal counts once. The base set is closed
// deals ONLY: an open deal has not been won or lost, so it is absent from this
// report rather than a zero inside it.
//
// Win RATE is deliberately not a measure. It is a ratio across two groups, so
// it is read off the won and lost rows rather than computed inside a single
// one — the same reason the stage-conversion key leaves conversion rates out of
// V1. A measure that silently answered a per-group ratio would reconcile
// against nothing a drill-through could show.
func winLossSpec() reportSpec {
	dimensions := map[string]string{
		fieldStatus:         colStatus,
		fieldSource:         colSource,
		fieldOwnerID:        colOwnerID,
		fieldPipelineID:     colPipelineID,
		fieldOrganizationID: colOrganizationID,
		fieldCurrency:       colCurrency,
	}
	maps.Copy(dimensions, periodDimensions(colClosedAt))

	return reportSpec{
		entity:    datasource.EntityDeal,
		table:     tableDeal,
		baseWhere: "t.archived_at IS NULL AND t.status IN ('won','lost')",
		basePlain: "live (unarchived) deals that have been won or lost, bucketed by when they closed " +
			"in the installation's reporting timezone (an open deal is absent from this report, not a zero in it)",
		dimensions: dimensions,
		measures:   map[string]string{fieldAmountMinor: colAmountMinor},
		// Every dimension also filters (REPORT-VOCAB-1): the period grains
		// included, so "won deals in 2026" is one call rather than a group-by
		// the caller then has to sift.
		//
		// Cloned rather than aliased — the two vocabularies are equal today and
		// nothing about them has to stay equal, so sharing one map would make a
		// later edit to either silently change both.
		filters:      withProjectFilter(maps.Clone(dimensions)),
		filterScopes: projectFilterScope,
		// The company a won or lost deal points at is row-scoped and masked on
		// a normal deal read, so grouping by it carries the same obligation the
		// partner dimension does on deals-by-stage.
		referenceScopes: map[string]string{colOrganizationID: tableOrganization},
		defaultBy:       moneyDefaultBy(fieldStatus),
		defaultAggs: []reportAggregate{
			{Fn: aggFnCount, As: aliasDeals},
			{Fn: aggFnSum, Field: fieldAmountMinor, As: "amount_minor_sum"},
		},
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"slices"
	"testing"
)

// Money never sums across currencies (data-semantics §1 r4, DM-FX-4,
// AC-DS-FX1): amount_minor is a minor-unit integer in the deal's own
// currency, so a total spanning currencies is a number with no unit.
//
// The two tests below derive that obligation from the catalog rather than
// listing the reports that owe it, so a report added later cannot ship a
// money measure the caller has no way to split — which is how the deals
// board came to refuse a mixed-currency column while the reports screen
// printed one as euros.
//
// moneyMeasures is the set a report cannot aggregate honestly without
// naming the currency the figure is in.
var moneyMeasures = map[string]bool{
	fieldAmountMinor:         true,
	fieldWeightedAmountMinor: true,
}

func TestEveryReportThatMeasuresMoneyCanSplitItByCurrency(t *testing.T) {
	t.Parallel()
	for report, spec := range prebuiltReports {
		if !measuresMoney(spec) {
			continue
		}
		if _, ok := spec.dimensions[fieldCurrency]; !ok {
			t.Errorf("report %q measures money but has no currency dimension: a caller "+
				"summing it cannot say which currency the total is in", report)
		}
		// A dimension with no matching filter can only be grouped BY, never
		// narrowed TO — so a caller wanting one currency's total has to read
		// every currency's row and add the ones it wants, which is the sum
		// this rule exists to prevent.
		if _, ok := spec.filters[fieldCurrency]; !ok {
			t.Errorf("report %q groups by currency but cannot filter on it", report)
		}
	}
}

func TestEveryDefaultPlanThatSumsMoneyGroupsByCurrency(t *testing.T) {
	t.Parallel()
	for report, spec := range prebuiltReports {
		if !defaultSumsMoney(spec) {
			continue
		}
		if !slices.Contains(spec.defaultBy, fieldCurrency) {
			t.Errorf("report %q sums money in its DEFAULT plan without grouping by "+
				"currency: that plan is what an agent calls first and what a screen "+
				"renders unattended, so the cross-currency sum reaches both", report)
		}
	}
}

func measuresMoney(spec reportSpec) bool {
	for measure := range spec.measures {
		if moneyMeasures[measure] {
			return true
		}
	}
	return false
}

func defaultSumsMoney(spec reportSpec) bool {
	for _, agg := range spec.defaultAggs {
		if agg.Fn == aggFnSum && moneyMeasures[agg.Field] {
			return true
		}
	}
	return false
}

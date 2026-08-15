// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// REPORT-VOCAB-1 pins win-loss's four vocabularies. These assert the
// PROPERTIES it pins rather than re-listing the map, so a deliberate addition
// stays green and a rule broken goes red.
func TestWinLossVocabularyMatchesItsPinnedShape(t *testing.T) {
	spec, ok := prebuiltReports["win-loss"]
	if !ok {
		t.Fatal("win-loss missing from the prebuilt catalog")
	}
	// The base set is closed deals only: an open deal is absent from this
	// report, never a zero in it.
	if !strings.Contains(spec.baseWhere, "t.status IN ('won','lost')") {
		t.Errorf("baseWhere = %q, want it restricted to won/lost", spec.baseWhere)
	}
	// Every dimension also filters, so "won deals in 2026" is one call.
	for name := range spec.dimensions {
		if _, ok := spec.filters[name]; !ok {
			t.Errorf("dimension %q is not also a filter", name)
		}
	}
	// Win rate is deliberately not a measure — it is a ratio across two groups.
	if _, ok := spec.measures["win_rate"]; ok {
		t.Error("win_rate is a measure; it is a ratio across groups and must be read off the rows")
	}
	if !reflect.DeepEqual(sortedKeys(spec.measures), []string{"amount_minor"}) {
		t.Errorf("measures = %v, want only amount_minor", sortedKeys(spec.measures))
	}
	// The grain is one row per deal: no join may widen it.
	if got := spec.fromClause(); got != "deal t" {
		t.Errorf("fromClause = %q, want the bare deal table — a join would multiply rows", got)
	}
}

// The three grains are the closed set (REPORT-PARAM-4), and each is anchored on
// the key's own date column (REPORT-PARAM-7) rather than a caller-chosen one.
func TestPeriodDimensionsAreTheThreeClosedGrains(t *testing.T) {
	got := sortedKeys(periodDimensions(colClosedAt))
	want := []string{"period_month", "period_quarter", "period_year"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grains = %v, want %v", got, want)
	}
	for name, expr := range periodDimensions(colClosedAt) {
		if !strings.Contains(expr, colClosedAt) {
			t.Errorf("%s is not anchored on %s: %q", name, colClosedAt, expr)
		}
		// The zone token is what bindInstallationZone swaps for a real bind
		// position; an expression without it reports in the server's zone
		// silently (REPORT-PARAM-6).
		if !strings.Contains(expr, reportZoneToken) {
			t.Errorf("%s does not bucket in the installation zone: %q", name, expr)
		}
	}
}

// The issue's third "done when": an out-of-vocabulary request still fails with
// the existing clear error rather than running. A grain outside the closed set
// is the case a caller is most likely to try.
func TestWinLossRefusesAGrainOutsideTheClosedSet(t *testing.T) {
	_, _, err := buildSelectList(prebuiltReports["win-loss"],
		[]string{"period_week"},
		[]reportAggregate{{Fn: "count", As: "deals"}})
	var notAllowed *FieldNotAllowedError
	if !errors.As(err, &notAllowed) {
		t.Fatalf("period_week → %v, want FieldNotAllowedError", err)
	}
	if notAllowed.Field != "period_week" {
		t.Errorf("refusal names %q, want period_week", notAllowed.Field)
	}
	// The refusal has to be actionable: it names the grains that DO work.
	_, message := notAllowed.MessageFault()
	for _, grain := range []string{"period_year", "period_quarter", "period_month"} {
		if !strings.Contains(message, grain) {
			t.Errorf("the refusal does not offer %q as an alternative: %s", grain, message)
		}
	}
}

// A period bucket's value must survive the derivation handle round trip: out as
// a group key in a URL query string, back as an equality predicate. Canonical
// text does; this is the unit-level half of the guarantee the integration suite
// proves against a real database.
func TestPeriodBucketValuesSurviveTheDerivationHandleRoundTrip(t *testing.T) {
	aggs := []reportAggregate{{Fn: "sum", Field: "amount_minor", As: "amount_minor_sum"}}
	for _, bucket := range []struct{ dimension, value string }{
		{"period_year", "2026"},
		{"period_quarter", "2026-Q1"},
		{"period_month", "2026-03"},
	} {
		minted := derivationURL("win-loss", map[string]any{"status": "won"},
			[]string{bucket.dimension}, aggs,
			map[string]any{bucket.dimension: bucket.value, "amount_minor_sum": int64(35000)})

		parsed, err := url.Parse(minted)
		if err != nil {
			t.Fatalf("%s: %v", bucket.dimension, err)
		}
		q, err := parseDerivationQuery(parsed.Query())
		if err != nil {
			t.Fatalf("%s: %v", bucket.dimension, err)
		}
		if got := q.Predicates[bucket.dimension]; got != bucket.value {
			t.Errorf("%s round-tripped to %q, want %q — the bucket does not survive its own handle",
				bucket.dimension, got, bucket.value)
		}
		// The handle must still compile against the report's vocabulary.
		if _, err := compileDerivation(prebuiltReports["win-loss"], q); err != nil {
			t.Errorf("%s: minted handle does not compile: %v", bucket.dimension, err)
		}
	}
}

// The definition is the plain-language half of AC-R6, and for this report it
// has to say the thing a reader would otherwise get wrong: an open deal is not
// in here at all.
func TestWinLossDefinitionSaysOpenDealsAreAbsent(t *testing.T) {
	got, err := renderDefinition(prebuiltReports["win-loss"], nil,
		[]boundPredicate{{Field: "period_year", Value: "2026"}},
		[]reportAggregate{{Fn: "sum", Field: "amount_minor", As: "amount_minor_sum"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `Over live (unarchived) deals that have been won or lost, bucketed by when they closed ` +
		`in the installation's reporting timezone (an open deal is absent from this report, not a zero in it), ` +
		`within the group where period_year = "2026": the sum of amount_minor as amount_minor_sum.`
	if got != want {
		t.Errorf("definition:\n got %q\nwant %q", got, want)
	}
}

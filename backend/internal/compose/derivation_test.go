// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
)

// The definition is the (a) half of AC-R6: the exact filter + group +
// aggregate in plain language. These are golden strings — a wording
// change is a product change and must show up here.
func TestRenderDefinitionReadsAsPlainLanguage(t *testing.T) {
	spec := prebuiltReports["forecast"]

	got, err := renderDefinition(spec,
		[]boundPredicate{{Field: "pipeline_id", Value: "018f-pipe"}},
		[]boundPredicate{{Field: "owner_id", Value: "018f-owner"}},
		[]reportAggregate{
			{Fn: "count", As: "deals"},
			{Fn: "sum", Field: "amount_minor", As: "unweighted_minor"},
			{Fn: "sum", Field: "weighted_amount_minor", As: "weighted_minor"},
		})
	if err != nil {
		t.Fatal(err)
	}
	want := `Over open, unarchived deals (win probability read live from the deal's current stage), ` +
		`filtered to pipeline_id = "018f-pipe", within the group where owner_id = "018f-owner": ` +
		`the number of matching records as deals; the sum of amount_minor as unweighted_minor; ` +
		`the sum of weighted_amount_minor as weighted_minor.`
	if got != want {
		t.Errorf("definition:\n got %q\nwant %q", got, want)
	}
}

func TestRenderDefinitionSpellsOutTheNullGroup(t *testing.T) {
	got, err := renderDefinition(prebuiltReports["forecast"], nil,
		[]boundPredicate{{Field: "owner_id", Value: ""}},
		[]reportAggregate{{Fn: "count"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `Over open, unarchived deals (win probability read live from the deal's current stage), ` +
		`within the group where owner_id is not set: the number of matching records.`
	if got != want {
		t.Errorf("definition:\n got %q\nwant %q", got, want)
	}
}

func TestRenderDefinitionRejectsUnknownAggregate(t *testing.T) {
	_, err := renderDefinition(prebuiltReports["forecast"], nil, nil,
		[]reportAggregate{{Fn: "median", Field: "amount_minor"}})
	var notAllowed *FieldNotAllowedError
	if !errors.As(err, &notAllowed) {
		t.Fatalf("unknown fn → %v, want FieldNotAllowedError", err)
	}
}

// A handle we mint must resolve: parseDerivationQuery is derivationURL's
// exact inverse, including the empty-string spelling of a NULL group key.
func TestDerivationURLRoundTrip(t *testing.T) {
	aggs := []reportAggregate{
		{Fn: "count", As: "deals"},
		{Fn: "sum", Field: "amount_minor", As: "unweighted_minor"},
	}
	minted := derivationURL("forecast",
		map[string]any{"pipeline_id": "018f-pipe"},
		[]string{"owner_id", "forecast_category"},
		aggs,
		map[string]any{"owner_id": "018f-owner", "forecast_category": nil, "deals": int64(3)})

	parsed, err := url.Parse(minted)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/v1/reports/forecast/derivation" {
		t.Errorf("path = %q", parsed.Path)
	}
	q, err := parseDerivationQuery(parsed.Query())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q.GroupBy, []string{"forecast_category", "owner_id"}) {
		t.Errorf("group_by = %v", q.GroupBy)
	}
	if !reflect.DeepEqual(q.Aggregates, aggs) {
		t.Errorf("aggregates = %+v", q.Aggregates)
	}
	wantPreds := map[string]string{
		"pipeline_id":       "018f-pipe",
		"owner_id":          "018f-owner",
		"forecast_category": "", // NULL group key travels as the empty value
	}
	if !reflect.DeepEqual(q.Predicates, wantPreds) {
		t.Errorf("predicates = %v, want %v", q.Predicates, wantPreds)
	}
}

func TestParseDerivationQueryRejectsMalformedHandles(t *testing.T) {
	for name, raw := range map[string]string{
		"agg without triplet": "agg=sum",
		"agg without fn":      "agg=:amount_minor:x",
		"repeated predicate":  "owner_id=a&owner_id=b",
	} {
		values, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, err = parseDerivationQuery(values)
		var notAllowed *FieldNotAllowedError
		if !errors.As(err, &notAllowed) {
			t.Errorf("%s → %v, want FieldNotAllowedError", name, err)
		}
	}
}

// The handle's query string reserves `by`, `agg`, and the injected
// row key; no report vocabulary may squat on them, or a minted URL
// would be ambiguous. Derived from the catalog, not a list.
func TestReportVocabularyAvoidsReservedDerivationNames(t *testing.T) {
	reserved := map[string]bool{"by": true, "agg": true, reservedDerivationColumn: true}
	for report, spec := range prebuiltReports {
		for _, vocab := range []map[string]string{spec.dimensions, spec.measures, spec.filters} {
			for field := range vocab {
				if reserved[field] {
					t.Errorf("report %q: field %q collides with a reserved derivation key", report, field)
				}
			}
		}
		for _, agg := range spec.defaultAggs {
			if reserved[agg.As] {
				t.Errorf("report %q: default aggregate alias %q collides with a reserved derivation key", report, agg.As)
			}
		}
	}
}

// A caller-chosen alias must not shadow the injected per-row handle.
func TestAggregateAliasCannotSquatOnDerivationURL(t *testing.T) {
	_, _, err := buildSelectList(prebuiltReports["forecast"],
		[]string{"owner_id"},
		[]reportAggregate{{Fn: "count", As: reservedDerivationColumn}})
	var notAllowed *FieldNotAllowedError
	if !errors.As(err, &notAllowed) || notAllowed.Field != reservedDerivationColumn {
		t.Fatalf("alias %q → %v, want FieldNotAllowedError on that field", reservedDerivationColumn, err)
	}
}

// The forecast is a parameterized report over the shared engine
// (B-E09.10): its weighted measure must divide by 100 with per-deal
// rounding, and its plan must aggregate the deal table alone — the
// stage join is a to-one lookup, never a row multiplier.
func TestForecastSpecShape(t *testing.T) {
	spec, ok := prebuiltReports["forecast"]
	if !ok {
		t.Fatal("forecast report missing from the prebuilt catalog")
	}
	if got := spec.fromClause(); got != "deal t JOIN stage s ON s.id = t.stage_id" {
		t.Errorf("fromClause = %q", got)
	}
	if spec.measures["weighted_amount_minor"] != "round((t.amount_minor * s.win_probability) / 100.0)::bigint" {
		t.Errorf("weighted measure = %q", spec.measures["weighted_amount_minor"])
	}
	if spec.measures["amount_minor"] != "t.amount_minor" {
		t.Errorf("unweighted measure = %q", spec.measures["amount_minor"])
	}
}

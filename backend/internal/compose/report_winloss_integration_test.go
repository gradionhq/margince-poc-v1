// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The win-loss report and the period bucket it groups by (REPORT-KEY-8,
// REPORT-VOCAB-1, REPORT-PARAM-4..7). Three invariants earn their own tests
// because each one fails silently rather than loudly:
//
//   - A bucket must survive the derivation handle's round trip. The handle
//     carries a group key out through a URL query string and binds it back as
//     an equality predicate; a value that cannot be rendered and re-parsed
//     makes "Explain This Number" fail on exactly the reports that need it.
//   - A bucket boundary is the installation's, not UTC's. This one is
//     invisible on a UTC installation, which is most test fixtures.
//   - An OPEN deal is absent from win-loss, not a zero in it.

import (
	"testing"
)

// seedClosedDeal writes one won/lost deal closed at a given instant. Closed
// deals carry a frozen FX rate and a lost reason by CHECK constraint, so the
// helper supplies both rather than letting each test rediscover them.
func (e *forecastEnv) seedClosedDeal(t *testing.T, name, status, closedAt string, amountMinor int64) {
	t.Helper()
	e.seedClosedDealWith(t, name, status, "manual", closedAt, amountMinor)
}

// seedClosedDealFromSource writes a WON deal recorded under a given source,
// including the empty one a deal may legitimately carry (deal.source is NOT
// NULL with no CHECK against "").
func (e *forecastEnv) seedClosedDealFromSource(t *testing.T, name, source, closedAt string, amountMinor int64) {
	t.Helper()
	e.seedClosedDealWith(t, name, "won", source, closedAt, amountMinor)
}

func (e *forecastEnv) seedClosedDealWith(t *testing.T, name, status, source, closedAt string, amountMinor int64) {
	t.Helper()
	lostReason := "no reason"
	if status == "won" {
		lostReason = ""
	}
	var reason *string
	if lostReason != "" {
		reason = &lostReason
	}
	e.seed(t, `INSERT INTO deal
		(id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency,
		 status, closed_at, lost_reason, fx_rate_to_base, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'EUR', $7, $8::timestamptz, $9, 1.0, $10, 'human:x')`,
		name, e.pipeline, e.stages[60], amountMinor, status, closedAt, reason, source)
}

// setInstallationZone rewrites the reporting zone the period buckets are
// computed in. Seeded as UTC by setupForecast; a test that cares says so.
func (e *forecastEnv) setInstallationZone(t *testing.T, zone string) {
	t.Helper()
	if _, err := e.owner.Exec(t.Context(),
		`UPDATE setting SET value = to_jsonb($1::text) WHERE key = 'installation.timezone'`, zone); err != nil {
		t.Fatalf("setting the installation timezone: %v", err)
	}
}

// bucketRow finds the one aggregate row whose group keys all match, so a test
// naming two dimensions cannot silently assert against the wrong group.
func bucketRow(t *testing.T, result reportResultWire, keys map[string]string) map[string]any {
	t.Helper()
	for _, row := range result.Rows {
		matched := true
		for dimension, want := range keys {
			if row[dimension] != want {
				matched = false
				break
			}
		}
		if matched {
			return row
		}
	}
	t.Fatalf("no row matching %v in %+v", keys, result.Rows)
	return nil
}

func TestWinLossGroupsClosedDealsByYearAndExcludesOpenOnes(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "Won early", "won", "2025-03-04T10:00:00Z", 10000)
	e.seedClosedDeal(t, "Won late", "won", "2025-11-30T10:00:00Z", 25000)
	e.seedClosedDeal(t, "Won next year", "won", "2026-02-01T10:00:00Z", 7000)
	e.seedClosedDeal(t, "Lost", "lost", "2025-06-01T10:00:00Z", 90000)
	// An open deal has not been won or lost: absent from this report, never a
	// zero in it (REPORT-VOCAB-1's base set).
	e.seedOpenDeal(t, "Still open", 60, nil, int64p(500000), stringp("commit"))

	result := e.runReport(e.Admin(), t, "win-loss",
		`{"group_by":["period_year","status"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}]}`)

	won2025 := bucketRow(t, result, map[string]string{"period_year": "2025", "status": "won"})
	if got := wireInt(t, won2025, "deals"); got != 2 {
		t.Errorf("2025 won deals = %d, want 2", got)
	}
	if got := wireInt(t, won2025, "amount_minor_sum"); got != 35000 {
		t.Errorf("2025 won amount = %d, want 35000", got)
	}
	// Three groups: 2025/won, 2025/lost, 2026/won. The open deal makes no fourth.
	if len(result.Rows) != 3 {
		t.Errorf("rows = %d, want 3 (the open deal must not appear): %+v", len(result.Rows), result.Rows)
	}
}

// THE test for this feature: a period bucket must round-trip through the
// derivation handle. The bucket value leaves as a group key, travels through a
// URL query string, and comes back as an equality predicate against the same
// expression that produced it. A value that does not survive that trip makes
// every period report's "Explain This Number" fail at exactly the moment a
// reader stops trusting the figure and asks what is behind it.
func TestWinLossPeriodDerivationReconcilesExactly(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "A", "won", "2025-03-04T10:00:00Z", 10000)
	e.seedClosedDeal(t, "B", "won", "2025-11-30T10:00:00Z", 25000)
	e.seedClosedDeal(t, "Other year", "won", "2026-02-01T10:00:00Z", 7000)

	result := e.runReport(e.Admin(), t, "win-loss",
		`{"group_by":["period_year"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}]}`)
	row := bucketRow(t, result, map[string]string{"period_year": "2025"})
	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("the 2025 bucket has no derivation_url: %+v", row)
	}

	derivation := e.explainReport(e.Admin(), t, "win-loss", handle)
	if derivation.TotalRows != 2 || len(derivation.Rows) != 2 {
		t.Fatalf("drill-through = %d rows (total %d), want the bucket's 2 deals: %+v",
			len(derivation.Rows), derivation.TotalRows, derivation.Rows)
	}
	var sum int64
	for _, source := range derivation.Rows {
		if got := source["period_year"]; got != "2025" {
			t.Errorf("source row period_year = %v, want 2025 — the drill-through left the bucket", got)
		}
		sum += wireInt(t, source, "amount_minor")
	}
	if displayed := wireInt(t, row, "amount_minor_sum"); sum != displayed {
		t.Errorf("drill-through sum %d != displayed %d", sum, displayed)
	}
	// The recompute over the same rows must agree with the displayed cell too.
	if got := wireInt(t, derivation.Aggregates, "amount_minor_sum"); got != wireInt(t, row, "amount_minor_sum") {
		t.Errorf("recomputed aggregate %d != displayed %d", got, wireInt(t, row, "amount_minor_sum"))
	}
}

// A bucket boundary is the installation's, not UTC's (REPORT-PARAM-6). This
// deal closes 30 minutes into 2026 UTC, which is still 2025 in New York — so a
// report that ignored the zone would put it in the wrong year, and on a UTC
// installation nothing would ever notice.
func TestWinLossPeriodBucketsFollowTheInstallationZone(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "New year's eve in New York", "won", "2026-01-01T00:30:00Z", 10000)

	utc := e.runReport(e.Admin(), t, "win-loss",
		`{"group_by":["period_year"],"aggregates":[{"fn":"count","as":"deals"}]}`)
	if got := utc.Rows[0]["period_year"]; got != "2026" {
		t.Fatalf("under UTC the bucket is %v, want 2026 — the fixture cannot discriminate", got)
	}

	e.setInstallationZone(t, "America/New_York")
	local := e.runReport(e.Admin(), t, "win-loss",
		`{"group_by":["period_year"],"aggregates":[{"fn":"count","as":"deals"}]}`)
	if got := local.Rows[0]["period_year"]; got != "2025" {
		t.Errorf("under America/New_York the bucket is %v, want 2025 — the bucket ignored the installation zone", got)
	}
}

// The three grains are spelled canonically (REPORT-PARAM-5), and the spelling
// is load-bearing twice over: it is what a reader sees, and text in these
// formats sorts chronologically, so the report needs no separate sort key.
func TestWinLossPeriodGrainsUseTheirCanonicalSpelling(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "Q1", "won", "2026-02-11T10:00:00Z", 10000)
	e.seedClosedDeal(t, "Q4", "won", "2025-10-02T10:00:00Z", 10000)

	for dimension, want := range map[string][]string{
		"period_year":    {"2025", "2026"},
		"period_quarter": {"2025-Q4", "2026-Q1"},
		"period_month":   {"2025-10", "2026-02"},
	} {
		result := e.runReport(e.Admin(), t, "win-loss",
			`{"group_by":["`+dimension+`"],"aggregates":[{"fn":"count","as":"deals"}]}`)
		if len(result.Rows) != len(want) {
			t.Fatalf("%s: %d rows, want %d: %+v", dimension, len(result.Rows), len(want), result.Rows)
		}
		// Ordered by the dimension, so the returned order IS the assertion that
		// these values sort chronologically as text.
		for i, bucket := range want {
			if got := result.Rows[i][dimension]; got != bucket {
				t.Errorf("%s row %d = %v, want %q", dimension, i, got, bucket)
			}
		}
	}
}

// The two doors onto one question must answer it the same way. A response's own
// derivation handle spells an unset group key as the empty value, which the
// drill-through binds as IS NULL; the filter door used to bind `= NULL`, which
// is never true. So a report could answer "no rows" and hand the reader a link
// that answered "six" — from the same response, about the same question. The
// package's whole premise is that an explanation cannot disagree with the number
// it explains.
func TestAnUnsetFilterMeansTheSameThingOnBothDoors(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "Unowned A", "won", "2025-03-04T10:00:00Z", 10000)
	e.seedClosedDeal(t, "Unowned B", "won", "2025-11-30T10:00:00Z", 25000)

	result := e.runReport(e.Admin(), t, "win-loss",
		`{"filters":{"owner_id":null},"aggregates":[{"fn":"count","as":"deals"}]}`)
	if len(result.Rows) == 0 {
		t.Fatalf("the filter door found nothing for owner_id=null, but both deals are unowned: %+v", result)
	}
	filtered := wireInt(t, result.Rows[0], "deals")

	derivation := e.explainReport(e.Admin(), t, "win-loss", result.DerivationURL)
	if got := wireInt(t, derivation.Aggregates, "deals"); got != filtered {
		t.Errorf("the filter door says %d and the handle it minted says %d — one question, two answers",
			filtered, got)
	}
}

// A year is spelled 2026 in JSON far more naturally than "2026", and the period
// grains are the first filters whose values look numeric. That call must come
// back as the caller's own mistake with the fix in it, never as an opaque 500
// that sends an agent retrying something that cannot succeed.
func TestANumericPeriodFilterIsRefusedAsTheCallersMistake(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "A", "won", "2026-03-04T10:00:00Z", 10000)

	if got := e.reportStatus(e.Admin(), "win-loss",
		`{"filters":{"period_year":2026},"aggregates":[{"fn":"count","as":"deals"}]}`); got != 422 {
		t.Errorf("a numeric period filter answered %d, want 422", got)
	}
	// The quoted spelling is the one that works, so the advice is actionable.
	if got := e.reportStatus(e.Admin(), "win-loss",
		`{"filters":{"period_year":"2026"},"aggregates":[{"fn":"count","as":"deals"}]}`); got != 200 {
		t.Errorf("the quoted spelling answered %d, want 200", got)
	}
}

// A group key that is EMPTY TEXT is not a group key that is ABSENT, and the
// handle has to keep them apart. `source` is the catalog's first unconstrained
// text dimension — every earlier one is a uuid, or carries a CHECK that forbids
// the empty string — so this is the first report where the two can collide.
// When they did, a bucket reported one deal and the handle it minted in the
// same response resolved to none.
func TestAnEmptyTextGroupKeyIsNotTheSameAsAnAbsentOne(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDealFromSource(t, "No source recorded", "", "2025-03-04T10:00:00Z", 10000)
	e.seedClosedDealFromSource(t, "Sourced", "manual", "2025-04-04T10:00:00Z", 20000)

	result := e.runReport(e.Admin(), t, "win-loss",
		`{"group_by":["source"],"aggregates":[{"fn":"count","as":"deals"}]}`)
	row := bucketRow(t, result, map[string]string{"source": ""})
	displayed := wireInt(t, row, "deals")

	derivation := e.explainReport(e.Admin(), t, "win-loss", row["derivation_url"].(string))
	if got := wireInt(t, derivation.Aggregates, "deals"); got != displayed {
		t.Errorf("the empty-source bucket shows %d and its own handle resolves to %d — "+
			"empty text and absent collapsed to one spelling", displayed, got)
	}
	for _, source := range derivation.Rows {
		if source["source"] != "" {
			t.Errorf("drill-through row has source %q, want the empty-source deal", source["source"])
		}
	}
}

// amount_minor is a minor-unit integer in the deal's OWN currency, so a total
// that spans currencies is a number with no unit. The default plan is the one
// an agent calls first and a screen renders unattended, so it has to split.
func TestTheWinLossDefaultNeverSumsAcrossCurrencies(t *testing.T) {
	e := setupForecast(t)
	e.seedClosedDeal(t, "In euros", "won", "2025-03-04T10:00:00Z", 10000)
	e.seed(t, `INSERT INTO deal
		(id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency,
		 status, closed_at, fx_rate_to_base, source, captured_by)
		VALUES ($1, $2, 'In dollars', $3, $4, 10000, 'USD', 'won', now(), 1.0, 'manual', 'human:x')`,
		e.pipeline, e.stages[60])

	result := e.runReport(e.Admin(), t, "win-loss", `{}`)
	for _, row := range result.Rows {
		if row["currency"] == nil {
			t.Fatalf("a default row carries no currency, so its total has no unit: %+v", row)
		}
		if got := wireInt(t, row, "amount_minor_sum"); got != 10000 {
			t.Errorf("row %v totals %d — the two currencies were added together", row["currency"], got)
		}
	}
	if len(result.Rows) != 2 {
		t.Errorf("rows = %d, want one per currency: %+v", len(result.Rows), result.Rows)
	}
}

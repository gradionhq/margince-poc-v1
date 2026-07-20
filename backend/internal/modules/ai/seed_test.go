// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// TestBackfillMigrationMatchesSeedModelRates is the drift guard the brief
// calls for: 0111_ai_model_rate_backfill.up.sql is a hand-written SQL
// mirror of SeedModelRates (a migration can't call Go code), so nothing
// stops the two from drifting the moment either one is edited alone. This
// test parses the migration's literal VALUES tuples and asserts they are
// exactly SeedModelRates' rows, at the same fixed effective_date the
// migration hard-codes — so a future SeedModelRates edit (new model, new
// price) fails THIS test loudly instead of leaving pre-existing
// workspaces backfilled with a stale sheet forever.

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// backfillMigrationPath is relative to this package directory
// (backend/internal/modules/ai) up to backend/migrations/core.
const backfillMigrationPath = "../../../migrations/core/0111_ai_model_rate_backfill.up.sql"

// backfillEffectiveDate is the fixed historical date 0111 hard-codes —
// duplicated here (not imported) because a migration's SQL literal has no
// Go symbol to reference. The live seed path (SeedWorkspaceDefaultsTx,
// called with the real bootstrap "now") is covered separately by the
// compose integration lane's bootstrap test.
var backfillEffectiveDate = time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)

var backfillRowPattern = regexp.MustCompile(
	`\('([a-z_]*)',\s*'([^']*)',\s*(-?\d+)::bigint,\s*(-?\d+)::bigint,\s*(-?\d+)::bigint,\s*(-?\d+)::bigint\)`)

// parseBackfillRates extracts the VALUES tuples from the migration file as
// ModelRate rows, in file order.
func parseBackfillRates(t *testing.T) []ModelRate {
	t.Helper()
	sql, err := os.ReadFile(backfillMigrationPath)
	if err != nil {
		t.Fatalf("reading %s: %v", backfillMigrationPath, err)
	}
	matches := backfillRowPattern.FindAllStringSubmatch(string(sql), -1)
	if len(matches) == 0 {
		t.Fatalf("no VALUES rows matched in %s — the parser regex or the migration's literal shape drifted", backfillMigrationPath)
	}
	rates := make([]ModelRate, len(matches))
	for i, m := range matches {
		in, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			t.Fatalf("row %d: parsing input rate %q: %v", i, m[3], err)
		}
		out, err := strconv.ParseInt(m[4], 10, 64)
		if err != nil {
			t.Fatalf("row %d: parsing output rate %q: %v", i, m[4], err)
		}
		cacheRead, err := strconv.ParseInt(m[5], 10, 64)
		if err != nil {
			t.Fatalf("row %d: parsing cache-read rate %q: %v", i, m[5], err)
		}
		cacheWrite, err := strconv.ParseInt(m[6], 10, 64)
		if err != nil {
			t.Fatalf("row %d: parsing cache-write rate %q: %v", i, m[6], err)
		}
		rates[i] = ModelRate{
			Provider: m[1], ModelID: m[2],
			InputPerMTokMicroUSD: in, OutputPerMTokMicroUSD: out,
			CacheReadPerMTokMicroUSD: cacheRead, CacheWritePerMTokMicroUSD: cacheWrite,
			EffectiveDate: backfillEffectiveDate,
		}
	}
	return rates
}

func TestBackfillMigrationMatchesSeedModelRates(t *testing.T) {
	migrated := parseBackfillRates(t)
	want := SeedModelRates(backfillEffectiveDate)

	if len(migrated) != len(want) {
		t.Fatalf("0111 backfill migration carries %d rows, SeedModelRates(%s) carries %d — the migration is a hand mirror that must match row-for-row",
			len(migrated), backfillEffectiveDate.Format("2006-01-02"), len(want))
	}

	index := func(rates []ModelRate) map[string]ModelRate {
		m := make(map[string]ModelRate, len(rates))
		for _, r := range rates {
			m[fmt.Sprintf("%s\x00%s", r.Provider, r.ModelID)] = r
		}
		return m
	}
	migratedByKey, wantByKey := index(migrated), index(want)

	for key, wantRate := range wantByKey {
		gotRate, ok := migratedByKey[key]
		if !ok {
			t.Errorf("SeedModelRates row %s/%s is missing from the 0111 backfill migration", wantRate.Provider, wantRate.ModelID)
			continue
		}
		if gotRate != wantRate {
			t.Errorf("0111 backfill row %s/%s = %+v, want %+v (SeedModelRates)", wantRate.Provider, wantRate.ModelID, gotRate, wantRate)
		}
	}
	for key, gotRate := range migratedByKey {
		if _, ok := wantByKey[key]; !ok {
			t.Errorf("0111 backfill migration carries %s/%s, which SeedModelRates does not produce", gotRate.Provider, gotRate.ModelID)
		}
	}
}

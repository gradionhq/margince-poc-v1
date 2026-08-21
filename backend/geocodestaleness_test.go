// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The staleness rule lives in the SCHEMA, and this holds it there.
//
// A company whose address moved must not keep answering radius queries from
// where it used to be. The first cut enforced that in Go, in the two address
// writers that were easy to find; a review found four more, each writing an
// address through its own table-driven SQL several columns deep in a generic
// builder with no seam to carry. Six writers is already more than anyone holds
// in their head, and the defect is invisible when missed — a wrong answer that
// reports success.
//
// So the rule is a trigger, and what this test guards is that the trigger
// exists and that nothing has quietly replaced it with a Go-side convention
// that a seventh writer could skip.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// geocodeMigrationFile finds where the rule lives.
//
// geocodeStalenessSQL is the trigger's declaration together with the body of the
// function it executes.
//
// Assembled from whichever core migrations hold them rather than read out of one
// file, because the two are not in one file and need not be: core opens with a
// baseline that puts every function ahead of every trigger, and a later
// migration may replace either half on its own. Scoped to the two objects rather
// than the whole namespace for the reason the column loop below needs — every
// address column also appears in `CREATE TABLE organization`, so a check against
// all of core would pass whether or not the trigger watches anything.
func geocodeStalenessSQL(t *testing.T) string {
	t.Helper()
	trigger := lastStatement(t, `CREATE (?:OR REPLACE )?TRIGGER trg_organization_geocode_stale\b.*?;`)
	fn := triggerFunction.FindStringSubmatch(trigger)
	if fn == nil {
		t.Fatalf("the staleness trigger names no function to execute:\n%s", trigger)
	}
	body := lastStatement(t, `CREATE (?:OR REPLACE )?FUNCTION `+regexp.QuoteMeta(fn[1])+`\(\).*?\$\$;`)
	return trigger + "\n" + body
}

var triggerFunction = regexp.MustCompile(`EXECUTE FUNCTION ([a-z_0-9]+)\(\)`)

// lastStatement returns the LAST statement in core/ matching pattern, and fails
// when there is none.
//
// Found rather than named: a test pinned to a filename fails for a reason that
// has nothing to do with what it checks. Core opens with one baseline file, and
// a later migration is a separate file sorting after it.
//
// The last match, not the only one, because revising a trigger body means
// shipping `CREATE OR REPLACE FUNCTION` in a new migration — leaving two
// definitions in the glob, of which the live one is the later. Requiring exactly
// one would keep asserting against the baseline copy while the database ran
// something else, which is the failure this helper exists to prevent.
func lastStatement(t *testing.T, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)` + pattern)
	files, err := filepath.Glob(filepath.Join("migrations", "core", "*.up.sql"))
	if err != nil {
		t.Fatalf("listing core migrations: %v", err)
	}
	var found []string
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		found = append(found, re.FindAllString(string(body), -1)...)
	}
	if len(found) == 0 {
		t.Fatalf("no statement in core/ matches %s", pattern)
	}
	return found[len(found)-1]
}

// addressColumns is every column the trigger must watch. A column added to the
// address later and not to the trigger is a writer path that silently stops
// invalidating.
var addressColumns = []string{
	"address_line1", "address_line2", "address_city",
	"address_region", "address_postal_code", "address_country",
}

func TestTheSchemaMarksCoordinatesStaleWhenAnAddressMoves(t *testing.T) {
	// Missing entirely is lastStatement's own failure: without the trigger every
	// address writer has to remember to invalidate, and the one that forgets
	// produces a company answering radius queries from an address it no longer
	// has, reporting success.
	src := geocodeStalenessSQL(t)

	for _, column := range addressColumns {
		if !strings.Contains(src, column) {
			t.Errorf("the trigger does not watch %s, so a write to that column alone leaves the "+
				"coordinates queryable and wrong", column)
		}
	}
	if !strings.Contains(src, "geocode_status IS DISTINCT FROM OLD.geocode_status") {
		t.Error("the trigger no longer yields to a writer that set geocode_status itself — the worker " +
			"recording a fresh point would have it stamped stale immediately, and no company would " +
			"ever be locatable")
	}
}

// The query predicate reads geocode_status, never the coordinates alone.
//
// This is the other half of the rule: the trigger marks a moved company stale,
// and only a status of 'ok' may be selected. A query that read lat/lon on its
// own would answer from the previous address however diligently the trigger
// fired.
func TestOnlyResolvedCoordinatesAreQueryable(t *testing.T) {
	// A missing index is lastStatement's own failure: a radius query would then
	// have nothing to select through.
	index := lastStatement(t, `CREATE INDEX idx_organization_geocoded\b.*?;`)
	if !strings.Contains(index, "geocode_status = 'ok'") {
		t.Errorf("the index does not restrict to resolved rows:\n%s\n\nA stale or failed row reachable "+
			"through it answers a distance from an address the company no longer has.", index)
	}
}

// Every organization address writer is still accounted for, so the trigger's
// coverage can be checked against something rather than assumed.
func TestTheAddressWritersAreStillTheOnesTheTriggerCovers(t *testing.T) {
	root := filepath.Join("internal", "modules", "people")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	writes := regexp.MustCompile(`(?i)(UPDATE|INSERT INTO)\s+organization\b[\s\S]{0,400}?address_(line1|city|country)`)
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if writes.MatchString(string(body)) {
			found++
		}
	}
	// The count is a floor, not a pin: writers may be added, and the trigger
	// covers them whether or not this test knows their names. What a zero would
	// mean is that the pattern has drifted from how addresses are written, and
	// this test is checking nothing.
	if found == 0 {
		t.Fatal("no organization address writer was found — the pattern has drifted from how addresses " +
			"are written, so this test proves nothing about the trigger's coverage")
	}
}

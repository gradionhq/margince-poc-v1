// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A suite that freezes the clock must seed against that same clock.
//
// The failure this prevents is a TIME BOMB, which is the reason it is worth a
// gate rather than a code review: it passes on the day it is written, keeps
// passing for weeks, and then fails on a date nobody can connect to a change.
//
// The worked example. `person_relationship_room_integration_test.go` drove its
// services from a frozen `roomFixedNow` of 2026-08-04 and seeded an activity at
// the DATABASE's `now() - interval '20 days'`. Those are two clocks. Every real
// day that passed moved the seeded row a day closer to the frozen now, so the
// row's apparent age fell by a day a day: nearly 20 days old the week it was
// written, and 6.9 days old on 2026-08-17 — one hour under the seven-day rule
// the gone-quiet rung applies. The suite went red on main with nothing changed
// but the date, and the failing assertion pointed at the dismissal logic, which
// was working perfectly.
//
// The gate is deliberately blunt: no `now()` arithmetic at all in a file that
// names a frozen clock. A fixture whose subject is a time passes it as a
// parameter (SeedRow takes them), derived from the same frozen value the
// service is given.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// databaseClockArithmetic finds a fixture leaning on the database's clock.
var databaseClockArithmetic = regexp.MustCompile(`now\(\)\s*[-+]\s*interval`)

// frozenClockFixture finds a file that pins the clock its services read.
var frozenClockFixture = regexp.MustCompile(`(?i)fixedNow|func\(\) time\.Time \{ return`)

// gateOwnFile is this file, excluded below.
const gateOwnFile = "fixtureclock_integration_test.go"

func TestAFrozenClockFixtureDoesNotSeedAgainstTheDatabaseClock(t *testing.T) {
	entries, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("listing the suite: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("found no test files at all — the walk is broken, and a walk " +
			"that finds nothing passes this test for the wrong reason")
	}

	var frozen int
	for _, path := range entries {
		// This file quotes the pattern in order to describe it, and is the one
		// place that must.
		if path == gateOwnFile {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		text := string(source)
		if !frozenClockFixture.MatchString(text) {
			continue
		}
		frozen++
		for _, offending := range databaseClockArithmetic.FindAllString(text, -1) {
			t.Errorf("%s freezes the clock its services read and then seeds with %q: "+
				"those are two clocks, and the distance between them grows by a day "+
				"every day this suite is not run. Pass the timestamp as a parameter, "+
				"derived from the frozen value (roomAgo/roomAhead).",
				path, strings.TrimSpace(offending))
		}
	}
	if frozen == 0 {
		t.Fatal("no suite here freezes a clock, so this gate matched nothing — " +
			"the detector is broken rather than the tree being clean")
	}
}

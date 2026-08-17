// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// closedSequenceEnd is the last core migration named by the four-digit
// sequence.
//
// The sequence is CLOSED, not renamed. Those versions are recorded in the
// schema_migrations_core of every database that ever applied them, and a
// renamed version strands its database: dbmigrate stops rather than apply a
// migration the ledger records under another name, because continuing would
// skip it as done forever. So the boundary is permanent, and a new migration
// is named for the unix second it was written instead — where the next number
// in a sequence is the name two branches off the same main both pick, a clock
// reading collides only between two branches stamped in the same second, and
// check-migration-versions.sh catches that against the base.
const closedSequenceEnd = "0284"

// stampSlack is how far ahead of the machine running this test a stamp may sit
// before the name is a skewed clock rather than a migration. It absorbs a
// reviewer whose clock trails the author's; it is nowhere near wide enough to
// admit a stamp that would outrank correctly written migrations for years.
const stampSlack = 24 * time.Hour

// TestCoreMigrationVersionsAreUnixSecondsAfterTheClosedSequence holds the two
// shapes core/ admits and the order between them. dbmigrate compares versions
// as strings, so "sorts after" is the whole contract — and it holds because
// every version in the closed sequence is ZERO-PADDED, not because ten digits
// beat four: "9999" sorts above "1787000000". Zero-padding is what lets one
// namespace carry both eras without renumbering an applied migration.
func TestCoreMigrationVersionsAreUnixSecondsAfterTheClosedSequence(t *testing.T) {
	core, _ := namespaces(t)

	sequence := regexp.MustCompile(`^[0-9]{4}$`)
	unixSecond := regexp.MustCompile(`^[0-9]{10}$`)
	// The clock is the subject here, not an incidental dependency: the only
	// place a stamp written by a wrong clock is detectable is against a right
	// one. A stamp from the past stays in the past, so this cannot flake in
	// the direction that matters.
	ceiling := time.Now().Add(stampSlack).Unix()

	highestSequence := ""
	for _, m := range core.Migrations {
		switch {
		case unixSecond.MatchString(m.Version):
			assertStampIsNotAhead(t, m.Version, m.Name, ceiling)
			if m.Version <= closedSequenceEnd {
				t.Errorf("core %s_%s: sorts at or below the closed sequence's last version %s, so it would run before the sequence on a fresh database and after it on every database already past %s — the same migrations in two orders",
					m.Version, m.Name, closedSequenceEnd, closedSequenceEnd)
			}
		case sequence.MatchString(m.Version):
			if m.Version > closedSequenceEnd {
				t.Errorf("core %s_%s: the four-digit sequence is closed at %s — scaffold the pair with `make migrate-create NAME=%s`, which names it for the current unix second",
					m.Version, m.Name, closedSequenceEnd, m.Name)
			}
			if !strings.HasPrefix(m.Version, "0") {
				t.Errorf("core %s_%s: a four-digit version that does not start with 0 sorts ABOVE the unix-second stamps, which inverts the two eras and hides every migration written after it",
					m.Version, m.Name)
			}
			if m.Version > highestSequence {
				highestSequence = m.Version
			}
		default:
			t.Errorf("core %s_%s: a core version is ten digits of unix seconds (`make migrate-create NAME=%s`) or a four-digit version from the closed 0001-%s sequence",
				m.Version, m.Name, m.Name, closedSequenceEnd)
		}
	}

	// The pin is the whole mechanism keeping the sequence closed, so it has to
	// keep describing the tree. Widening it reopens every version in between
	// and leaves every gate green.
	if highestSequence != closedSequenceEnd {
		t.Errorf("core/ ends its four-digit sequence at %q but closedSequenceEnd pins %q — the constant is what closes the sequence, so a value that no longer matches the tree reopens the versions between the two",
			highestSequence, closedSequenceEnd)
	}
}

// assertStampIsNotAhead fails a version stamped in the future. Such a version
// outranks every correctly stamped migration written after it, which the
// version gate then reports as THOSE migrations sorting too low — advice that
// cannot be followed, since re-stamping reproduces the same lower number. It
// also splits apply order: dbmigrate skips what a database already applied, so
// the future-stamped migration runs first on a fresh install and last on an
// existing one.
func assertStampIsNotAhead(t *testing.T, version, name string, ceiling int64) {
	t.Helper()
	stamp, err := strconv.ParseInt(version, 10, 64)
	if err != nil {
		t.Errorf("core %s_%s: ten digits that are not a number: %v", version, name, err)
		return
	}
	if stamp > ceiling {
		t.Errorf("core %s_%s: stamped %s, which is ahead of this machine's clock — the machine that wrote it was running fast, and the version can never be outranked by a correctly stamped migration",
			version, name, time.Unix(stamp, 0).UTC().Format(time.RFC3339))
	}
}

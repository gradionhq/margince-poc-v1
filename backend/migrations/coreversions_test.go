// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

import (
	"regexp"
	"testing"
)

// closedSequenceEnd is the last core migration named by the four-digit
// sequence.
//
// The sequence is CLOSED, not renamed. Those versions are recorded in the
// schema_migrations_core of every database that ever applied them, and a
// renamed version strands its database: dbmigrate stops rather than apply a
// migration the ledger records under another name, because continuing would
// skip it as done forever. So the boundary is permanent, and a new migration
// is named for the unix second it was written instead — a name no second
// branch can pick, where the next number in a sequence is exactly the name
// two branches off the same main both pick.
const closedSequenceEnd = "0284"

// TestCoreMigrationVersionsAreUnixSecondsAfterTheClosedSequence holds the two
// shapes core/ admits, and the order between them. pgmigrate compares versions
// as strings, so "sorts after" is the whole contract: every ten-digit unix
// second sorts above every four-digit version, which is what lets one
// namespace carry both eras without renumbering a single applied migration.
func TestCoreMigrationVersionsAreUnixSecondsAfterTheClosedSequence(t *testing.T) {
	core, _ := namespaces(t)

	sequence := regexp.MustCompile(`^[0-9]{4}$`)
	unixSecond := regexp.MustCompile(`^[0-9]{10}$`)

	for _, m := range core.Migrations {
		switch {
		case unixSecond.MatchString(m.Version):
			if m.Version <= closedSequenceEnd {
				t.Errorf("core %s_%s: sorts at or below the closed sequence's last version %s, so a database already past it would never apply this migration and would never report it missing",
					m.Version, m.Name, closedSequenceEnd)
			}
		case sequence.MatchString(m.Version):
			if m.Version > closedSequenceEnd {
				t.Errorf("core %s_%s: the four-digit sequence is closed at %s — scaffold the pair with `make migrate-create NAME=%s`, which names it for the current unix second",
					m.Version, m.Name, closedSequenceEnd, m.Name)
			}
		default:
			t.Errorf("core %s_%s: a core version is ten digits of unix seconds (`make migrate-create NAME=%s`) or a four-digit version from the closed 0001-%s sequence",
				m.Version, m.Name, m.Name, closedSequenceEnd)
		}
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Person-satellite lifecycle reach as a fitness function. piicoverage_test.go
// proves Art. 17 erasure and Art. 15 SAR reach every table its registry
// declares PII-bearing; it says nothing about the three OTHER lifecycle paths a
// person's child rows ride — the retention anonymizer, the merge relink, and the
// archive cascade. Those are where a new satellite rots invisibly: a satellite
// nobody archived stays live under an archived Person, and one nobody relinked
// is orphaned on the merged-away half. Neither errors, and neither is visible
// until someone reads the row that should be gone.
//
// The obligations are DERIVED, never listed:
//
//   - The satellite set comes from the migration DDL — a person_*-named table
//     with a person_id column. A new satellite is enrolled by the migration
//     that creates it, not by an edit here.
//   - The archive cascade binds a satellite IFF it has an archived_at column.
//     Four satellites (person_consent, person_social, person_profile_field,
//     person_signature_enrich_state) have none, so ArchivePerson has nothing
//     to soft-delete on them: their rows leave the database only when the
//     person ROW itself is deleted, through the person FK's
//     ON DELETE CASCADE. ArchivePerson is a SOFT delete, so it does not fire
//     that cascade, and no path in this tree hard-deletes a person — those
//     four rows therefore outlive the archive. That is a real coverage gap in
//     the lifecycle, not a discharge of it; what this gate can honestly hold
//     is only the obligation the table's own shape admits, and demanding a
//     soft delete on a table with no archived_at column would be red for a
//     requirement that does not apply.
//   - The retention anonymizer and the merge relink bind a satellite that the
//     PII registry (piiTables) declares subject-bearing. Registration in that
//     registry is the ONE act that declares a table holds a data subject, and
//     it already carries the ratified reasons a person_* table may sit outside
//     it — person_consent, for instance, is deliberately kept under Art. 5
//     accountability rather than erased. Re-deciding that here would fork the
//     judgment across two gates.
//
// Presence is the whole check: each path is a source-text scan of the file that
// discharges it, reusing the write-target extraction the ownership gate already
// spells (sqlWriteTargets). It proves the table is WRITTEN by that path, not
// that the write is correct — semantics belong to the module's own tests. What
// it catches is the silent omission.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// satellitePath is one lifecycle obligation: the file that discharges it, and
// the message a missing table gets. Every path here is a WRITE — archiving,
// deleting or relinking the satellite's rows.
type satellitePath struct {
	name   string
	file   string
	remedy string
	// archivedOnly restricts the path to satellites carrying archived_at.
	archivedOnly bool
	// piiOnly restricts the path to satellites the PII registry declares
	// subject-bearing.
	piiOnly bool
}

// satelliteLifecyclePaths are the person-satellite obligations this gate owns.
// Art. 17 erasure and Art. 15 SAR are deliberately absent: piicoverage_test.go
// already binds them to the same registry piiOnly reads, and duplicating them
// here would mean two gates to keep in step over one promise.
var satelliteLifecyclePaths = []satellitePath{
	{
		name:         "archive_cascade",
		file:         "internal/modules/people/person.go",
		remedy:       "add it to ArchivePerson's statement list — an unlisted satellite stays LIVE under an archived Person",
		archivedOnly: true,
	},
	{
		name:    "retention_anonymize",
		file:    "internal/modules/privacy/retention.go",
		remedy:  "delete its rows in the person/anonymize executor — the sweep anonymizes the person row and would leave this satellite's copy of the subject behind",
		piiOnly: true,
	},
	{
		name:    "merge_relink",
		file:    "internal/modules/people/mergerelink.go",
		remedy:  "relink its rows onto the survivor in relinkPersonReferences — rows left on the merged-away person are orphaned, invisible to every read of the survivor",
		piiOnly: true,
	},
}

var (
	// personSatelliteName matches the CREATE TABLE lines this gate governs:
	// child tables named for the person they hang off.
	personSatelliteName = regexp.MustCompile(`^person_[a-z_]+$`)
	// columnLine matches a bare column definition inside a CREATE TABLE block.
	// Constraint clauses (CONSTRAINT/PRIMARY/UNIQUE/CHECK/FOREIGN) never reach
	// the two names this gate reads, so no exclusion is needed.
	columnLine = regexp.MustCompile(`^\s+([a-z_]+)\s+[a-z]`)
	// alterColumn matches a later migration adding or dropping a column on an
	// existing table — person_consent gains lead_id that way (0056), so a
	// derivation that read only CREATE TABLE would be reading a stale schema.
	alterColumn = regexp.MustCompile(`(?i)^\s*ALTER TABLE ([a-z_]+)\s+(ADD|DROP) COLUMN (?:IF (?:NOT )?EXISTS )?([a-z_]+)`)
)

// personSatellites derives the governed satellites from the migration sources:
// table name → its column set. A person_*-named CREATE TABLE with a person_id
// column qualifies; ADD/DROP COLUMN in a later migration is folded in, in file
// order, so the column set is the one the migrated schema actually has.
func personSatellites(t *testing.T) map[string]map[string]bool {
	t.Helper()
	columns := map[string]map[string]bool{}
	var paths []string
	for _, root := range []string{"migrations/core", "migrations/custom"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
				return err
			}
			paths = append(paths, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// core is sequentially numbered and custom timestamp-ordered, so a
	// lexical sort per namespace IS apply order (ADR-0017).
	sort.Strings(paths)
	for _, path := range paths {
		raw, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.up.sql file from walking the trusted migrations tree
		if err != nil {
			t.Fatal(err)
		}
		current := ""
		for _, line := range strings.Split(string(raw), "\n") {
			if m := createTableLine.FindStringSubmatch(line); m != nil {
				if personSatelliteName.MatchString(m[1]) {
					current = m[1]
					columns[current] = map[string]bool{}
				}
				continue
			}
			if strings.HasPrefix(line, ");") {
				current = ""
				continue
			}
			if current != "" {
				if m := columnLine.FindStringSubmatch(line); m != nil {
					columns[current][m[1]] = true
				}
				continue
			}
			m := alterColumn.FindStringSubmatch(line)
			if m == nil || columns[m[1]] == nil {
				continue
			}
			if strings.EqualFold(m[2], "ADD") {
				columns[m[1]][m[3]] = true
			} else {
				delete(columns[m[1]], m[3])
			}
		}
	}
	satellites := map[string]map[string]bool{}
	for table, cols := range columns {
		if cols["person_id"] {
			satellites[table] = cols
		}
	}
	// The derivation reads DDL text, so a change in how migrations spell a
	// CREATE TABLE would empty it silently and the gate would pass by finding
	// nothing to check. person_email and person_phone have been satellites
	// since 0004 and cannot legitimately disappear.
	for _, expected := range []string{"person_email", "person_phone"} {
		if satellites[expected] == nil {
			t.Fatalf("derived no %s satellite from the migrations — the derivation is broken, not the schema", expected)
		}
	}
	return satellites
}

// pathWrites returns the tables one lifecycle file writes.
func pathWrites(t *testing.T, file string) map[string]bool {
	t.Helper()
	writes := map[string]bool{}
	for _, lit := range sqlLiterals(t, file) {
		for _, table := range sqlWriteTargets(lit) {
			writes[table] = true
		}
	}
	return writes
}

func TestEveryPersonSatelliteJoinsEveryLifecyclePathThatApplies(t *testing.T) {
	satellites := personSatellites(t)
	var missing []string
	for _, path := range satelliteLifecyclePaths {
		writes := pathWrites(t, path.file)
		for table, cols := range satellites {
			if path.archivedOnly && !cols["archived_at"] {
				continue // removed by the person FK cascade; nothing to archive
			}
			if _, subjectBearing := piiTables[table]; path.piiOnly && !subjectBearing {
				continue // not declared subject-bearing in piiTables
			}
			if writes[table] {
				continue
			}
			missing = append(missing, "person satellite "+table+" is not handled by the "+path.name+
				" path ("+path.file+") — "+path.remedy)
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Error(m)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// The committed schema catalog, the same file the migration lane regenerates.
// Reading it here means the set of held tables is derived from what the
// migrations build, not from a list somebody has to remember to extend.
const headCatalogPath = "../../../migrations/testdata/head_catalog.txt"

// activityHoldSelectors are every SQL statement in this package that decides
// whether an activity is frozen by a legal hold reached through its links, and
// every erasure statement that reaches activity-derived rows — the activity's
// text, its embedding, its participants. A hold on a linked record must cover
// the evidence about it, so each one owes an arm per held table; a statement
// missing one re-admits exactly what its siblings exclude.
//
// The package keeps no registry of its erasure statements, so the
// activity-derived ones are named here by hand. A new statement that touches
// an activity-derived table belongs in this list.
func activityHoldSelectors() map[string]string {
	return map[string]string{
		"erasure notTransitivelyHeld":       notTransitivelyHeld("x.id"),
		"erasure subjectOnlyActivities":     subjectOnlyActivities,
		"erasure unlinkedSubjectMail":       unlinkedSubjectMail,
		"erasure unlinkedSubjectChannel":    unlinkedSubjectChannel,
		"erasure embeddings delete":         subjectActivityEmbeddingsDelete,
		"erasure participants delete":       subjectParticipantsDelete,
		"erasure participants blank":        subjectParticipantsBlank,
		"restriction notHeldThroughAnyLink": notHeldThroughAnyLink("x.id"),
		"retention activity/":               retentionSelectors["activity/"],
		"retention activity/transcript":     retentionSelectors["activity/transcript"],
	}
}

// holdArm names one (selector, table) pair the gate asks about.
type holdArm string

func armOf(selector, table string) holdArm { return holdArm(selector + " / " + table) }

// The one arm a selector may lack, with what the omission costs.
var waivedHoldArms = gatekit.Waive(personArmWaivers(
	"erasure notTransitivelyHeld", "erasure subjectOnlyActivities", "erasure unlinkedSubjectMail",
	"erasure unlinkedSubjectChannel", "erasure embeddings delete", "erasure participants delete",
	"erasure participants blank",
))

// personArmWaivers states the one cost once for every erasure statement built
// on notTransitivelyHeld: the person arm is not there because it could never
// fire, not because it was forgotten.
func personArmWaivers(selectors ...string) map[holdArm]string {
	out := make(map[holdArm]string, len(selectors))
	for _, selector := range selectors {
		out[armOf(selector, "person")] = "a person-linked activity shared with another subject " +
			"is already outside every erasure selector, and the erased subject is proven unheld before " +
			"the cascade runs (ErasePerson's own-hold check); the arm would read a hold that cannot be set"
	}
	return out
}

func TestEveryLegalHoldColumnIsReadByEveryActivityHoldSelector(t *testing.T) {
	held := tablesCarryingLegalHoldReachableFromActivityLink(t)
	if len(held) < 4 {
		t.Fatalf("expected at least person/organization/deal/lead to carry legal_hold and an activity_link column, catalog yielded %v", held)
	}
	for name, sql := range activityHoldSelectors() {
		for _, table := range held {
			if waivedHoldArms.Waived(t, armOf(name, table)) {
				continue
			}
			assertSelectorReadsHold(t, name, sql, table)
		}
	}
	waivedHoldArms.AssertAllMatched(t)
}

// assertSelectorReadsHold proves the arm is wired end to end: the table is
// LEFT JOINed on its activity_link column, and the alias that join introduces
// is the one whose legal_hold the predicate reads. Checking only for the
// substring "legal_hold" would pass a join that reads the wrong alias.
func assertSelectorReadsHold(t *testing.T, selector, sql, table string) {
	t.Helper()
	join := regexp.MustCompile(fmt.Sprintf(`LEFT JOIN %s (\w+) ON (\w+)\.id = \w+\.%s_id`, table, table))
	m := join.FindStringSubmatch(sql)
	if m == nil || m[1] != m[2] {
		t.Fatalf("%s: no `LEFT JOIN %s <alias> ON <alias>.id = <link>.%s_id` — add the %s arm so an activity linked to a held %s is frozen like one linked to a held deal",
			selector, table, table, table, table)
	}
	read := fmt.Sprintf("coalesce(%s.legal_hold, false)", m[1])
	if !strings.Contains(sql, read) {
		t.Fatalf("%s: joins %s as %q but never reads %s", selector, table, m[1], read)
	}
}

// tablesCarryingLegalHoldReachableFromActivityLink derives, from the catalog,
// every table that has a legal_hold column AND a <table>_id column on
// activity_link — the set an activity can be held through.
func tablesCarryingLegalHoldReachableFromActivityLink(t *testing.T) []string {
	t.Helper()
	catalog, err := os.ReadFile(headCatalogPath)
	if err != nil {
		t.Fatalf("read schema catalog: %v", err)
	}
	holdColumn := regexp.MustCompile(`^public\.(\w+)\.legal_hold boolean`)
	linkColumn := regexp.MustCompile(`^public\.activity_link\.(\w+)_id uuid`)
	withHold := map[string]bool{}
	linked := map[string]bool{}
	for _, line := range strings.Split(string(catalog), "\n") {
		if m := holdColumn.FindStringSubmatch(line); m != nil {
			withHold[m[1]] = true
		}
		if m := linkColumn.FindStringSubmatch(line); m != nil {
			linked[m[1]] = true
		}
	}
	var out []string
	for table := range withHold {
		if linked[table] {
			out = append(out, table)
		}
	}
	return out
}

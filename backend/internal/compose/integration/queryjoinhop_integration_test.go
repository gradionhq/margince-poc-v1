// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The hop that lives in neither record, against real rows and real RLS.
//
// The unit suite proves the derivation and the statement it compiles to. What
// only a database can say is that the statement RUNS — a join edge is the one
// hop form whose subquery names a table the plan's own vocabulary never
// resolved, so a column that is not there is a database error rather than a
// refusal, and a validated plan must never produce one.
//
// The row scope is the other half. A hop is a read of the record it lands on,
// so a caller who cannot see the organization must not be able to select a
// person through it, or the employment table becomes a channel onto rows the
// scope hides. That is what bounds this read — ADR-0091 retired RLS (core 0217)
// and dropped workspace_id from both join tables, so row scope and object RBAC
// are the boundary rather than a tenant predicate.

import (
	"fmt"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// employmentFixture is one person at each of two organizations owned by
// different reps, plus a third whose employment is archived.
type employmentFixture struct {
	rep1Org, rep3Org           ids.UUID
	atRep1Org, atRep3Org       ids.UUID
	formerlyAtRep1Org          ids.UUID
	leftRep1Org                ids.UUID
	servingNoticeAtRep1Org     ids.UUID
	activityAboutRep1OrgPerson ids.UUID
}

func (q *queryEnv) seedEmployments(t *testing.T) employmentFixture {
	t.Helper()
	var f employmentFixture
	f.rep1Org = q.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, address_city, source, captured_by)
		VALUES ($1, $2, 'Stuttgart Werke', 'Stuttgart', 'manual', 'human:x')`, q.Rep1)
	f.rep3Org = q.SeedID(t, `INSERT INTO organization (id, owner_id, display_name, address_city, source, captured_by)
		VALUES ($1, $2, 'Hamburg Logistik', 'Hamburg', 'manual', 'human:x')`, q.Rep3)

	// archivedAt and endedAt are SQL expressions rather than bound parameters so
	// a case can seed `now()` and `current_date + 30` — the DATABASE's clock,
	// which is the one the hop compares against. A time computed here would
	// disagree with it whenever the two machines do.
	employ := func(name string, owner, org ids.UUID, archivedAt, endedAt string) ids.UUID {
		person := q.SeedID(t, `INSERT INTO person (id, full_name, owner_id, source, captured_by)
			VALUES ($1, $2, $3, 'manual', 'human:x')`, name, owner)
		q.SeedID(t, fmt.Sprintf(`INSERT INTO relationship
			(id, kind, person_id, organization_id, source, captured_by, archived_at, ended_at)
			VALUES ($1, 'employment', $2, $3, 'manual', 'human:x', %s, %s)`, archivedAt, endedAt),
			person, org)
		return person
	}
	f.atRep1Org = employ("Ronny Stuttgart", q.Rep1, f.rep1Org, "NULL", "NULL")
	f.atRep3Org = employ("Hanna Hamburg", q.Rep3, f.rep3Org, "NULL", "NULL")
	f.formerlyAtRep1Org = employ("Lars Leaver", q.Rep1, f.rep1Org, "now()", "NULL")
	// LEFT, not deleted: the row stays, which is the ordinary end of a job and
	// the state archived_at alone does not describe.
	f.leftRep1Org = employ("Mona Moved-On", q.Rep1, f.rep1Org, "NULL", "current_date - 30")
	// Serving out a notice period. Still employed until the date arrives.
	f.servingNoticeAtRep1Org = employ("Nils Notice", q.Rep1, f.rep1Org, "NULL", "current_date + 30")

	// One activity, linked to the person at rep1's organization, so the other
	// join table is exercised on the same corpus.
	// An activity carries no owner_id: its row scope is walked from the records
	// it links, which is the same edge this hop traverses.
	f.activityAboutRep1OrgPerson = q.SeedID(t, `INSERT INTO activity
		(id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, 'note', 'Kickoff notes', now(), 'manual', 'human:x')`)
	q.SeedID(t, `INSERT INTO activity_link (id, activity_id, entity_type, person_id)
		VALUES ($1, $2, 'person', $3)`, f.activityAboutRep1OrgPerson, f.atRep1Org)
	return f
}

// The question the grammar could not be asked before: people at a company in a
// named city. The employment lives in `relationship`, so neither record carries
// a column that could express it.
func TestAPersonIsSelectedByTheirEmployersAttributes(t *testing.T) {
	q := setupQuery(t)
	f := q.seedEmployments(t)

	answer, err := q.answer(q.admin(), `{"version": "v1", "target": "person",
		"traverse": {"relation": "organizations",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`)
	if err != nil {
		t.Fatalf("traversing the employment edge: %v", err)
	}
	found := map[ids.UUID]bool{}
	for _, row := range answer.Rows {
		found[row.ID] = true
	}
	if !found[f.atRep1Org] {
		t.Errorf("the person employed in Stuttgart was not selected; got %d rows", len(answer.Rows))
	}
	if found[f.atRep3Org] {
		t.Error("a person employed in Hamburg was selected by a Stuttgart predicate")
	}
	// An archived employment is a job the person no longer holds. It must not
	// carry the hop, and nothing else in the statement would exclude it — the
	// hop's own archived_at is the ORGANIZATION's.
	if found[f.formerlyAtRep1Org] {
		t.Error("an archived employment still selected its person")
	}
	// The state archived_at does not cover, and the one that actually happens:
	// the person left and the row stayed. Filtering only archival is what makes
	// a company somebody left go on reading as where they work.
	if found[f.leftRep1Org] {
		t.Error("a person who left still answers as staff of the company they left")
	}
	// A future ended_at is a notice period. They still work there.
	if !found[f.servingNoticeAtRep1Org] {
		t.Error("a person serving out their notice was dropped, though their employment has not ended yet")
	}
}

// The inverse of the same edge, which is the question the employment table was
// built to answer.
func TestAnOrganizationIsSelectedByItsPeople(t *testing.T) {
	q := setupQuery(t)
	f := q.seedEmployments(t)

	answer, err := q.answer(q.admin(), `{"version": "v1", "target": "organization",
		"traverse": {"relation": "persons",
		             "where": [{"field": "full_name", "op": "eq", "value": "Ronny Stuttgart"}]}}`)
	if err != nil {
		t.Fatalf("traversing the inverse employment edge: %v", err)
	}
	if len(answer.Rows) != 1 || answer.Rows[0].ID != f.rep1Org {
		t.Fatalf("the organization employing Ronny was not the only answer: %+v", answer.Rows)
	}
}

// The half nobody had noticed was missing: an activity can name the person it
// is about, through `activity_link`.
func TestAnActivityIsSelectedByThePersonItLinks(t *testing.T) {
	q := setupQuery(t)
	f := q.seedEmployments(t)

	answer, err := q.answer(q.admin(), `{"version": "v1", "target": "activity",
		"traverse": {"relation": "persons",
		             "where": [{"field": "full_name", "op": "eq", "value": "Ronny Stuttgart"}]}}`)
	if err != nil {
		t.Fatalf("traversing the activity link edge: %v", err)
	}
	if len(answer.Rows) != 1 || answer.Rows[0].ID != f.activityAboutRep1OrgPerson {
		t.Fatalf("the activity about Ronny was not the only answer: %+v", answer.Rows)
	}
}

// A hop is a read of the record it lands on, so it carries that record's row
// scope. The join table must not become a channel onto an organization the
// caller cannot see — and the count is the disclosure, not just the rows.
func TestAJoinHopCannotSelectThroughARecordTheCallerCannotSee(t *testing.T) {
	q := setupQuery(t)
	f := q.seedEmployments(t)

	// Rep3 owns the Hamburg organization and neither the Stuttgart one nor the
	// person employed there.
	answer, err := q.answer(q.teamRep(q.Rep3, q.Team2), `{"version": "v1", "target": "person",
		"traverse": {"relation": "organizations",
		             "where": [{"field": "address.city", "op": "eq", "value": "Stuttgart"}]}}`)
	if err != nil {
		t.Fatalf("the scoped traversal errored rather than answering narrowly: %v", err)
	}
	for _, row := range answer.Rows {
		if row.ID == f.atRep1Org {
			t.Error("a rep selected a person through an organization outside their row scope")
		}
	}
	if len(answer.Rows) != 0 {
		t.Errorf("a rep with no Stuttgart organization got %d rows through the employment edge", len(answer.Rows))
	}

	// The positive control, on the same principal and the same edge. Without it
	// the assertion above passes for a rep who can traverse nothing at all —
	// including one whose fixture never seeded, which is the reading an empty
	// answer most often has.
	own, err := q.answer(q.teamRep(q.Rep3, q.Team2), `{"version": "v1", "target": "person",
		"traverse": {"relation": "organizations",
		             "where": [{"field": "address.city", "op": "eq", "value": "Hamburg"}]}}`)
	if err != nil {
		t.Fatalf("the rep's own traversal errored: %v", err)
	}
	if len(own.Rows) != 1 || own.Rows[0].ID != f.atRep3Org {
		t.Fatalf("the rep could not reach the person at their OWN organization, so the refusal above "+
			"proves nothing about row scope: %+v", own.Rows)
	}
}

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The two rules that make a person's employer a single readable fact: an
// employment they already hold cannot be recorded a second time, and a person
// whose only employment this is holds it as their current primary one.
//
// Both are enforced in Postgres — a partial unique index and a subquery inside
// the insert — so they are proved here, over HTTP, against the real store.
// Each rule is paired with the case it must NOT refuse, because a rule tested
// only where it fires reads identically to one that refuses everything.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// employment posts one edge and returns the status plus what the store made of
// it — the created id, whether it landed primary, and the refusal detail when
// it did not land at all.
func (e *relEnv) employment(t *testing.T, orgID string, body apptest.AnyMap) (status int, id string, primary bool, detail string) {
	t.Helper()
	body["kind"] = "employment"
	body["person_id"] = e.personID
	body["organization_id"] = orgID
	body["source"] = "ui"
	var out struct {
		ID               string `json:"id"`
		IsCurrentPrimary bool   `json:"is_current_primary"`
		Detail           string `json:"detail"`
	}
	status = e.Call(t, "POST", "/v1/relationships", body, nil, &out)
	return status, out.ID, out.IsCurrentPrimary, out.Detail
}

// secondOrg creates one more company to employ the same person at.
func (e *relEnv) secondOrg(t *testing.T, name string) string {
	t.Helper()
	var org struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/organizations", apptest.AnyMap{"display_name": name}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create %s → %d", name, status)
	}
	return org.ID
}

func TestASecondCurrentEmploymentAtTheSameCompanyIsRefused(t *testing.T) {
	e := setupRelationships(t)

	status, first, _, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusCreated {
		t.Fatalf("first employment → %d", status)
	}

	// The same pair again. Before uq_rel_employment both rows landed, and the
	// account then counted the person twice.
	status, _, _, detail := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusConflict {
		t.Fatalf("duplicate employment → %d, want 409", status)
	}
	const want = "this person already works at that company — end the employment they have there before recording a new one"
	if detail != want {
		t.Errorf("refusal detail = %q, want %q — the caller is told which rule fired, never the index name", detail, want)
	}

	// The role is not part of the key: the same person cannot hold the same job
	// twice under two titles either.
	if status, _, _, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "ceo"}); status != http.StatusConflict {
		t.Errorf("duplicate under a different role → %d, want 409", status)
	}

	// The mirror. Once they have LEFT, being hired again by the same company is
	// a new fact, not a duplicate — which is why the index predicate carries
	// ended_at IS NULL and deliberately differs from uq_rel_project_stakeholder.
	var ended struct {
		IsCurrentPrimary bool `json:"is_current_primary"`
	}
	if status := e.Call(t, "PATCH", "/v1/relationships/"+first,
		apptest.AnyMap{"ended_at": "2026-01-31"}, nil, &ended); status != http.StatusOK {
		t.Fatalf("ending the first employment → %d", status)
	}
	if ended.IsCurrentPrimary {
		t.Error("a job the person has left is still flagged as their CURRENT primary employer")
	}
	status, _, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusCreated {
		t.Errorf("re-employment after leaving → %d, want 201: a former employer may hire someone back", status)
	}
	if !primary {
		t.Error("the re-employment is the person's only current job and did not land as their primary one")
	}
}

// Setting the flag on a job that is already over does not take either — the
// same rule read from the other direction, and the reason it is written against
// the row rather than as a condition on the patch.
func TestAnEndedEmploymentCannotBeMadeTheCurrentPrimaryOne(t *testing.T) {
	e := setupRelationships(t)

	status, edge, _, _ := e.employment(t, e.orgID, apptest.AnyMap{
		"started_at": "2019-01-01", "ended_at": "2021-06-30",
	})
	if status != http.StatusCreated {
		t.Fatalf("historical employment → %d", status)
	}
	var patched struct {
		IsCurrentPrimary bool `json:"is_current_primary"`
	}
	if status := e.Call(t, "PATCH", "/v1/relationships/"+edge,
		apptest.AnyMap{"is_current_primary": true}, nil, &patched); status != http.StatusOK {
		t.Fatalf("patching the flag onto an ended employment → %d", status)
	}
	if patched.IsCurrentPrimary {
		t.Error("an employment that ended in 2021 now reads as the person's current primary employer")
	}
}

func TestAPersonsOnlyCurrentEmploymentIsTheirPrimaryOne(t *testing.T) {
	e := setupRelationships(t)

	// Nobody asked for primary. is_current_primary defaults to false, so before
	// this rule the person ended up employed by exactly one company and having
	// no primary employer — a state every reader of the column has to guess at.
	status, _, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusCreated {
		t.Fatalf("first employment → %d", status)
	}
	if !primary {
		t.Error("a person's only employment did not land as their current primary one")
	}

	// A SECOND concurrent job is not promoted. Which of two employers is the
	// primary one is a fact about the person that the second insert does not
	// carry, and guessing it would overwrite the answer the first one gave.
	if status, _, primary, _ := e.employment(t, e.secondOrg(t, "Moonlight Ltd"), apptest.AnyMap{}); status != http.StatusCreated || primary {
		t.Errorf("second concurrent employment → %d primary=%t, want 201 and not primary", status, primary)
	}
}

// The store decides the flag only for a caller who left it out. A request that
// SENDS false is a person unticking "current employer" in the rail, and
// deriving over it would hand back the opposite of what they chose.
func TestAnExplicitlyUnsetPrimaryFlagIsHonouredOnTheOnlyEmployment(t *testing.T) {
	e := setupRelationships(t)

	status, first, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{
		"role": "cto", "is_current_primary": false,
	})
	if status != http.StatusCreated {
		t.Fatalf("employment with the flag explicitly unset → %d", status)
	}
	if primary {
		t.Error("a request that said is_current_primary=false got true back — the derivation overrode the caller")
	}

	// The choice sticks. Nothing later re-derives it, so the person keeps the
	// employer they recorded and no primary flag until somebody says otherwise.
	if status := e.Call(t, "PATCH", "/v1/relationships/"+first, apptest.AnyMap{"role": "ceo"}, nil, nil); status != http.StatusOK {
		t.Fatalf("patching an unrelated field → %d", status)
	}
	if e.isPrimary(t, first) {
		t.Error("editing the role re-derived the flag the caller had explicitly unset")
	}
}

// A recorded last day that has not arrived is a NOTICE PERIOD, and somebody
// serving one still works there. Reading `ended_at` as "gone" the moment it is
// set took them off their employer's contact list months early, and nothing
// could put them back: the column cannot be cleared through the API and the
// patch that would restore the flag is refused by the same rule.
// dbDate asks POSTGRES for a date relative to its own today, because that is the
// clock the predicate under test reads. A date built from the Go process would
// disagree with it by a day whenever the two sit either side of midnight, and the
// boundary cases below are exactly where that disagreement would show up as a
// flake nobody could reproduce.
func (e *relEnv) dbDate(t *testing.T, offsetDays int) string {
	t.Helper()
	var day string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT to_char(current_date + $1::int, 'YYYY-MM-DD')`, offsetDays).Scan(&day); err != nil {
		t.Fatalf("asking the database for today%+d: %v", offsetDays, err)
	}
	return day
}

func TestANoticePeriodDoesNotEndSomebodysEmployment(t *testing.T) {
	e := setupRelationships(t)
	future := e.dbDate(t, 90)

	// Created WITHOUT an end date, so the PATCH below is a real transition into a
	// notice period rather than a re-statement of what the row already said. A row
	// that starts with the future date and is then patched to the same value never
	// crosses the boundary this test is about.
	status, edge, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusCreated {
		t.Fatalf("employment → %d", status)
	}
	if !primary {
		t.Error("their only employment did not land as current primary")
	}

	// The transition: an active employment enters its notice period.
	var noticed struct {
		IsCurrentPrimary bool `json:"is_current_primary"`
	}
	if got := e.Call(t, "PATCH", "/v1/relationships/"+edge,
		apptest.AnyMap{"ended_at": future}, nil, &noticed); got != http.StatusOK {
		t.Fatalf("recording the notice → %d", got)
	}
	if !noticed.IsCurrentPrimary {
		t.Error("entering a notice period took the current-primary flag immediately")
	}

	// And a fresh employment created ALREADY in notice keeps it too, which is the
	// other door onto the same rule.
	if status, _, alreadyNoticed, _ := e.employment(t, e.secondOrg(t, "Backfilled GmbH"), apptest.AnyMap{
		"ended_at": future,
	}); status != http.StatusCreated || alreadyNoticed {
		// Not primary: this person already holds a current employment, so the
		// derivation does not fire — the point is only that it was accepted.
		if status != http.StatusCreated {
			t.Errorf("employment created already in notice → %d", status)
		}
	}
}

// Promoting a notice-period employment demotes the incumbent, on the update path
// exactly as on the create path. The two halves must ask the same question of a
// date: the guard that demotes the incumbent and the statement that grants the
// flag both mean "has that date arrived", never "does this row have a date at
// all". Ask it two ways and a future date leaves both rows flagged —
// uq_rel_current_primary_employer then answers for two employments at once.
//
// Only a patch of a row that ALREADY carries an end date reaches this.
func TestMakingANoticePeriodEmploymentThePrimaryOneReplacesTheIncumbent(t *testing.T) {
	e := setupRelationships(t)

	status, incumbent, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusCreated || !primary {
		t.Fatalf("the job they hold → %d primary=%t", status, primary)
	}
	// A second job, ending in ninety days, not primary yet.
	status, notice, _, _ := e.employment(t, e.secondOrg(t, "Leaving Soon GmbH"), apptest.AnyMap{
		"ended_at": e.dbDate(t, 90),
	})
	if status != http.StatusCreated {
		t.Fatalf("the notice-period job → %d", status)
	}

	// "That one is their main employer now." The create path accepts exactly
	// this; the patch path must too.
	var patched struct {
		IsCurrentPrimary bool `json:"is_current_primary"`
	}
	if got := e.Call(t, "PATCH", "/v1/relationships/"+notice,
		apptest.AnyMap{"is_current_primary": true}, nil, &patched); got != http.StatusOK {
		t.Fatalf("promoting the notice-period employment → %d, want 200 (a 409 here is the two-primaries bug)", got)
	}
	if !patched.IsCurrentPrimary {
		t.Error("the patch reported success and the flag did not take")
	}
	if e.isPrimary(t, incumbent) {
		t.Error("the incumbent kept the flag as well — two current primary employers is what the index refuses")
	}
}

// The flag is written once and nothing rewrites it, so every READER has to ask
// whether the employment is still theirs. Without that, a notice period keeps
// counting at the old employer on the day after the last day — and forever after,
// because no sweep revisits the row. That is precisely the state the dedupe
// migration was written to end, reintroduced with a delay.
//
// Driven through the account's own contact count, because that is the number a
// person actually looks at, and through the person-by-employer filter beside it.
func TestAnEmploymentPastItsLastDayStopsCountingAtTheAccount(t *testing.T) {
	e := setupRelationships(t)

	// Written as a notice period, so the flag is legitimately TRUE on the row.
	status, edge, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{
		"ended_at": e.dbDate(t, 30),
	})
	if status != http.StatusCreated || !primary {
		t.Fatalf("notice-period employment → %d primary=%t", status, primary)
	}
	if got := e.contactCount(t, e.orgID); got != 1 {
		t.Fatalf("contact count while serving notice = %d, want 1 — they still work there", got)
	}
	if got := len(e.peopleAtOrg(t, e.orgID)); got != 1 {
		t.Fatalf("people-at-employer while serving notice = %d, want 1", got)
	}

	// The last day arrives and passes. Nothing rewrites the row — the stored flag
	// is still true — so the only thing that can change the answer is the reader.
	if _, err := e.Owner.Exec(t.Context(),
		`UPDATE relationship SET ended_at = current_date - 1 WHERE id = $1`, edge); err != nil {
		t.Fatalf("moving the last day into the past: %v", err)
	}
	var stored bool
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT is_current_primary FROM relationship WHERE id = $1`, edge).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored {
		t.Fatal("the stored flag changed on its own; this test can no longer prove the readers derive")
	}
	if got := e.contactCount(t, e.orgID); got != 0 {
		t.Errorf("contact count after the last day passed = %d, want 0 — the reader trusted a stale flag", got)
	}
	if got := len(e.peopleAtOrg(t, e.orgID)); got != 0 {
		t.Errorf("people-at-employer after the last day passed = %d, want 0", got)
	}
}

// contactCount reads the account's own Contacts number, the one the companies
// list and the company page both show.
func (e *relEnv) contactCount(t *testing.T, orgID string) int {
	t.Helper()
	var org struct {
		ContactCount *int `json:"contact_count"`
	}
	if status := e.Call(t, "GET", "/v1/organizations/"+orgID, nil, nil, &org); status != http.StatusOK {
		t.Fatalf("reading the account → %d", status)
	}
	if org.ContactCount == nil {
		t.Fatal("the account answered no contact_count at all")
	}
	return *org.ContactCount
}

// peopleAtOrg is the person list's employer filter — the other reader of the
// flag, and the one a rep uses to find who they know at an account.
func (e *relEnv) peopleAtOrg(t *testing.T, orgID string) []string {
	t.Helper()
	var listed struct {
		Data []struct {
			FullName string `json:"full_name"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/people?organization_id="+orgID, nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("listing people at the employer → %d", status)
	}
	names := make([]string, 0, len(listed.Data))
	for _, person := range listed.Data {
		names = append(names, person.FullName)
	}
	return names
}

// The two days either side of the boundary, which is where a predicate written
// with the wrong comparison passes every other test and still gets one of these
// backwards. `ended_at` means the employment ended, so a date that has ARRIVED is
// a departure — which is also what keeps the rail's "End employment" button
// doing something the moment it is pressed.
func TestTheBoundaryBetweenNoticeAndDepartureIsTodayItself(t *testing.T) {
	for _, day := range []struct {
		name     string
		offset   int
		stillHis bool
	}{
		{"a last day still ahead", 1, true},
		{"a last day that is today", 0, false},
	} {
		t.Run(day.name, func(t *testing.T) {
			env := setupRelationships(t)
			status, _, primary, _ := env.employment(t, env.orgID, apptest.AnyMap{
				"ended_at": env.dbDate(t, day.offset),
			})
			if status != http.StatusCreated {
				t.Fatalf("employment ending today%+d → %d", day.offset, status)
			}
			if primary != day.stillHis {
				t.Errorf("is_current_primary = %t for %s, want %t", primary, day.name, day.stillHis)
			}
		})
	}
}

// An employment recorded as already over is history being backfilled. Promoting
// it would tell every reader the person currently works somewhere they left —
// and asking for it to be primary must not cost them the employer they have.
func TestAnAlreadyEndedEmploymentIsNeverPromoted(t *testing.T) {
	e := setupRelationships(t)

	status, current, primary, _ := e.employment(t, e.orgID, apptest.AnyMap{"role": "cto"})
	if status != http.StatusCreated || !primary {
		t.Fatalf("the job they actually hold → %d primary=%t, want 201 and primary", status, primary)
	}

	// Backfilled WITH the flag asked for. The row does not take it, and the
	// demotion that would have cleared the incumbent never runs — so the
	// employer they actually have keeps the flag instead of the job they left.
	status, _, primary, _ = e.employment(t, e.secondOrg(t, "Former Employer GmbH"), apptest.AnyMap{
		"started_at": "2019-01-01", "ended_at": "2021-06-30", "is_current_primary": true,
	})
	if status != http.StatusCreated {
		t.Fatalf("historical employment → %d", status)
	}
	if primary {
		t.Error("an employment created already ended took the current-primary flag because the request asked for it")
	}

	if !e.isPrimary(t, current) {
		t.Error("backfilling a job they left in 2021 took the primary flag off the job they hold today")
	}
}

// isPrimary re-reads one edge through the person's employment list — the only
// read this surface offers for a single relationship.
func (e *relEnv) isPrimary(t *testing.T, edgeID string) bool {
	t.Helper()
	var listed struct {
		Data []struct {
			ID               string `json:"id"`
			IsCurrentPrimary bool   `json:"is_current_primary"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/relationships?kind=employment&person_id="+e.personID, nil, nil, &listed); status != http.StatusOK {
		t.Fatalf("listing employments → %d", status)
	}
	for _, edge := range listed.Data {
		if edge.ID == edgeID {
			return edge.IsCurrentPrimary
		}
	}
	t.Fatalf("employment %s is not in the person's own list of %d", edgeID, len(listed.Data))
	return false
}

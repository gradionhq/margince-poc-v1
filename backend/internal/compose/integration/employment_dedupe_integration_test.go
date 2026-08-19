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

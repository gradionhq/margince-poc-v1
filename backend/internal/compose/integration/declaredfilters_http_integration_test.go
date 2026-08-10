// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The wire half of the declared list filters. The store-level semantics live
// in declaredfilters_integration_test.go; what is proved here is the thing the
// store cannot see — that the HANDLER carries each parameter to it, which is
// where all four of these were dropped.
//
// The distinction is not academic. A handler that mapped `tag` onto the wrong
// store field would satisfy both the store tests (which set the field
// themselves) and the AST gate (which only asks that the handler names the
// parameter). Only a request through the real route can tell those apart, so
// every filter below is asked for over HTTP, against records created over
// HTTP, and answered by the count that proves the narrowing.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

// listedIDs is the shape every list response shares, read down to the one
// thing these assertions are about: which records came back.
type listedIDs struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// onlyRecord fails unless the page carries exactly the record expected — the
// assertion a dropped filter cannot pass, since it answers with every
// readable row.
func onlyRecord(t *testing.T, e *apptest.AppEnv, path, want, what string) {
	t.Helper()
	var page listedIDs
	if status := e.Call(t, "GET", path, nil, nil, &page); status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, status)
	}
	if len(page.Data) != 1 || page.Data[0].ID != want {
		t.Fatalf("GET %s returned %d records, want only %s — a filter the handler drops answers "+
			"the whole list with the right shape", path, len(page.Data), what)
	}
}

// createdRecord posts one record and returns its id.
func createdRecord(t *testing.T, e *apptest.AppEnv, path string, body apptest.AnyMap) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", path, body, nil, &created); status != http.StatusCreated {
		t.Fatalf("POST %s = %d, want 201", path, status)
	}
	return created.ID
}

func TestThePersonListNarrowsByTagOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	tagged := createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Tagged Person"})
	createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Untagged Person"})
	tag := createdRecord(t, e, "/v1/tags", apptest.AnyMap{"name": "VIP"})
	if status := e.Call(t, "POST", "/v1/tags/"+tag+"/apply", apptest.AnyMap{
		"entity_type": "person", "entity_id": tagged,
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("applying the tag = %d, want 201", status)
	}

	onlyRecord(t, e, "/v1/people?tag=VIP", tagged, "the tagged person")
	onlyRecord(t, e, "/v1/people?tag=vip", tagged, "the tagged person, asked for in another case")
}

func TestTheOrganizationListNarrowsByDomainOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	held := createdRecord(t, e, "/v1/organizations", apptest.AnyMap{
		"display_name": "Acme", "domains": []apptest.AnyMap{{"domain": "acme.example", "is_primary": true}},
	})
	createdRecord(t, e, "/v1/organizations", apptest.AnyMap{
		"display_name": "Other", "domains": []apptest.AnyMap{{"domain": "other.example", "is_primary": true}},
	})

	onlyRecord(t, e, "/v1/organizations?domain=acme.example", held, "the account that lists the domain")
	onlyRecord(t, e, "/v1/organizations?domain=ACME.example", held, "the same account, asked for in another case")
}

func TestTheActivityListNarrowsByAssigneeOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status := e.Call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("GET /v1/me = %d, want 200", status)
	}
	mine := createdRecord(t, e, "/v1/activities", apptest.AnyMap{
		"kind": "task", "subject": "Call the buyer", "due_at": "2026-09-01T09:00:00Z", "assignee_id": me.User.ID,
	})
	// An unassigned task is the row a dropped filter hands back alongside it.
	createdRecord(t, e, "/v1/activities", apptest.AnyMap{"kind": "task", "subject": "Nobody's"})

	onlyRecord(t, e, "/v1/activities?assignee_id="+me.User.ID, mine, "the open task that person holds")
}

func TestThePipelineListAnswersIncludeArchivedOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	retired := createdRecord(t, e, "/v1/pipelines", apptest.AnyMap{"name": "Retired"})
	// Aged through the database because no wire operation archives a pipeline
	// (#835). The parameter is about rows in this state, and the state is
	// reachable in a deployment's data whether or not an endpoint mints it.
	if _, err := e.Owner.Exec(t.Context(),
		`UPDATE pipeline SET archived_at = now() WHERE id = $1`, retired); err != nil {
		t.Fatalf("archiving the pipeline: %v", err)
	}

	var live, all listedIDs
	if status := e.Call(t, "GET", "/v1/pipelines", nil, nil, &live); status != http.StatusOK {
		t.Fatalf("GET /v1/pipelines = %d, want 200", status)
	}
	if status := e.Call(t, "GET", "/v1/pipelines?include_archived=true", nil, nil, &all); status != http.StatusOK {
		t.Fatalf("GET /v1/pipelines?include_archived=true = %d, want 200", status)
	}
	if lists(live, retired) {
		t.Fatalf("the live pipeline list carries the archived pipeline")
	}
	if !lists(all, retired) {
		t.Fatalf("include_archived=true did not reach the read: the archived pipeline is absent from a page "+
			"that asked for it (live=%d, with archived=%d)", len(live.Data), len(all.Data))
	}
}

// lists reports whether a page carries one record id.
func lists(page listedIDs, id string) bool {
	for _, record := range page.Data {
		if record.ID == id {
			return true
		}
	}
	return false
}

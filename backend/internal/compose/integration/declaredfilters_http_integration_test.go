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
	"fmt"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/jackc/pgx/v5"
)

// disownOverDB nulls a record's owner at the database. A create over the wire
// stamps the calling seat as owner when the body names none, so the unowned
// state these queue tests are about has to be produced after the fact.
func disownOverDB(t *testing.T, e *apptest.AppEnv, table, id string) {
	t.Helper()
	if err := apptest.InWorkspace(e, t, e.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE `+table+` SET owner_id = NULL WHERE id = $1`, id)
		return err
	}); err != nil {
		t.Fatalf("disown %s %s: %v", table, id, err)
	}
}

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
	// Aged through the database because no wire operation archives a pipeline.
	// The parameter is about rows in this state, and the state is reachable in
	// a deployment's data whether or not an endpoint mints it.
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

// callerUserID reads the signed-in user's own id off /me — the same value a
// screen builds its "My records" filter from.
func callerUserID(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var me struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status := e.Call(t, "GET", "/v1/me", nil, nil, &me); status != http.StatusOK {
		t.Fatalf("GET /v1/me = %d, want 200", status)
	}
	return me.User.ID
}

func TestThePersonListNarrowsToTheUnownedQueueOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	owner := callerUserID(t, e)

	createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Owned Person", "owner_id": owner})
	unowned := createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Unowned Person"})
	disownOverDB(t, e, "person", unowned)

	// Unassigned is a fact with its own queue, not an absence: a list that
	// answered every row here would send somebody to claim records that are
	// already claimed.
	onlyRecord(t, e, "/v1/people?unassigned=true", unowned, "the unowned person")
}

func TestTheLeadListNarrowsToTheUnownedQueueOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	owner := callerUserID(t, e)

	createdRecord(t, e, "/v1/leads", apptest.AnyMap{"full_name": "Owned Lead", "email": "owned@lead.test", "owner_id": owner})
	unowned := createdRecord(t, e, "/v1/leads", apptest.AnyMap{"full_name": "Unowned Lead", "email": "unowned@lead.test"})
	disownOverDB(t, e, "lead", unowned)

	// The same dial the person and company lists answer (DM-VOCAB-OWN-1): a
	// lead queue nobody has claimed is the first thing a rep asks a lead list.
	onlyRecord(t, e, "/v1/leads?unassigned=true", unowned, "the unowned lead")
}

func TestTheOwnerDialsRefuseEachOtherOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	owner := callerUserID(t, e)

	// owner_id AND unassigned can only ever match nothing. Answering an empty
	// page would be indistinguishable from an honest one, so the request is
	// refused instead.
	path := "/v1/people?owner_id=" + owner + "&unassigned=true"
	if status := e.Call(t, "GET", path, nil, nil, nil); status != http.StatusUnprocessableEntity {
		t.Fatalf("GET %s = %d, want 422 — two owner dials name two different sets", path, status)
	}
}

// The project scope on the timeline list, over the wire. Three activities on
// one person — filed under the asked-for project, filed under another, filed
// under none — and the scoped page must be exactly the first and the third: a
// handler that drops `project_id` answers all three with the right shape.
func TestTheActivityListNarrowsByProjectOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	org := createdRecord(t, e, "/v1/organizations", apptest.AnyMap{"display_name": "Acme"})
	person := createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Dana Buyer"})
	project := func(name string) string {
		return createdRecord(t, e, "/v1/projects", apptest.AnyMap{
			"name": name, "organization_id": org, "source": "manual",
		})
	}
	erp, migration := project("ERP rollout"), project("Datacentre migration")

	mail := func(subject string, within string) string {
		links := []apptest.AnyMap{{"entity_type": "person", "entity_id": person}}
		if within != "" {
			links = append(links, apptest.AnyMap{"entity_type": "project", "entity_id": within})
		}
		return createdRecord(t, e, "/v1/activities", apptest.AnyMap{
			"kind": "email", "subject": subject, "direction": "inbound", "links": links,
		})
	}
	onERP := mail("ERP cutover plan", erp)
	onOther := mail("Rack decommissioning", migration)
	unfiled := mail("Invoice question", "")

	var page listedIDs
	path := "/v1/activities?entity_type=person&entity_id=" + person + "&project_id=" + erp
	if status := e.Call(t, "GET", path, nil, nil, &page); status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, status)
	}
	seen := map[string]bool{}
	for _, row := range page.Data {
		seen[row.ID] = true
	}
	if seen[onOther] {
		t.Errorf("GET %s returned the other engagement's mail — the handler dropped project_id", path)
	}
	if !seen[onERP] || !seen[unfiled] {
		t.Errorf("GET %s lost the scoped project's own mail or the unfiled one: got %v", path, seen)
	}
}

// The date range on the timeline list, over the wire. Three mails on three
// days; `occurred_after` keeps its own instant (inclusive) and
// `occurred_before` drops its own (exclusive), which is what makes a calendar
// day spell as [day 00:00, next day 00:00) without double-counting midnight.
func TestTheActivityListNarrowsByDateRangeOnTheWire(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	person := createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Dana Buyer"})
	mail := func(subject, occurredAt string) string {
		return createdRecord(t, e, "/v1/activities", apptest.AnyMap{
			"kind": "email", "subject": subject, "direction": "inbound", "occurred_at": occurredAt,
			"links": []apptest.AnyMap{{"entity_type": "person", "entity_id": person}},
		})
	}
	day1 := mail("Monday", "2026-03-02T10:00:00Z")
	day2 := mail("Tuesday", "2026-03-03T10:00:00Z")
	day3 := mail("Wednesday", "2026-03-04T10:00:00Z")

	list := func(query string) map[string]bool {
		var page listedIDs
		path := "/v1/activities?entity_type=person&entity_id=" + person + query
		if status := e.Call(t, "GET", path, nil, nil, &page); status != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, status)
		}
		seen := map[string]bool{}
		for _, row := range page.Data {
			seen[row.ID] = true
		}
		return seen
	}
	cases := []struct {
		query string
		want  map[string]bool
	}{
		// after: the bound itself stays in.
		{"&occurred_after=2026-03-03T10:00:00Z", map[string]bool{day2: true, day3: true}},
		// before: the bound itself is out.
		{"&occurred_before=2026-03-03T10:00:00Z", map[string]bool{day1: true}},
		// both: one calendar day.
		{"&occurred_after=2026-03-03T00:00:00Z&occurred_before=2026-03-04T00:00:00Z", map[string]bool{day2: true}},
	}
	for _, c := range cases {
		got := list(c.query)
		for _, id := range []string{day1, day2, day3} {
			if got[id] != c.want[id] {
				t.Errorf("GET ...%s: activity %q present=%v, want %v", c.query, id, got[id], c.want[id])
			}
		}
	}
}

// The record page draws the 360's own activities page and continues from its
// cursor through GET /activities. So the two must agree on the edge: the
// list's page after that cursor starts exactly where the section stopped, and
// nothing is shown twice or skipped. 27 mails, a 25-row section.
func TestThePerson360TimelineCursorContinuesIntoTheActivityList(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	person := createdRecord(t, e, "/v1/people", apptest.AnyMap{"full_name": "Dana Buyer"})
	const total = 27
	for i := range total {
		id := createdRecord(t, e, "/v1/activities", apptest.AnyMap{
			"kind": "email", "subject": fmt.Sprintf("Mail %02d", i), "direction": "inbound",
			"occurred_at": fmt.Sprintf("2026-03-%02dT10:00:00Z", i+1),
			"links":       []apptest.AnyMap{{"entity_type": "person", "entity_id": person}},
		})
		// The thread key is capture's to write, never the API's, so it is
		// stamped the way capture leaves it.
		if err := apptest.InWorkspace(e, t, e.Slug, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE activity SET thread_key = 'thread-1' WHERE id = $1`, id)
			return err
		}); err != nil {
			t.Fatalf("stamp thread_key on %s: %v", id, err)
		}
	}

	var view struct {
		Activities struct {
			Data []struct {
				ID        string  `json:"id"`
				ThreadKey *string `json:"thread_key"`
			} `json:"data"`
			Page struct {
				HasMore    bool    `json:"has_more"`
				NextCursor *string `json:"next_cursor"`
			} `json:"page"`
		} `json:"activities"`
	}
	path := "/v1/people/" + person + "/360"
	if status := e.Call(t, "GET", path, nil, nil, &view); status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, status)
	}
	section := view.Activities
	if !section.Page.HasMore || section.Page.NextCursor == nil || *section.Page.NextCursor == "" {
		t.Fatalf("360 activities: has_more=%v next_cursor=%v — want a cut section that says where it stopped",
			section.Page.HasMore, section.Page.NextCursor)
	}
	if section.Data[0].ThreadKey == nil || *section.Data[0].ThreadKey != "thread-1" {
		t.Errorf("360 activities: first row carries thread_key %v, want thread-1 so the page can fold the conversation", section.Data[0].ThreadKey)
	}

	var rest listedIDs
	path = "/v1/activities?entity_type=person&entity_id=" + person + "&cursor=" + *section.Page.NextCursor
	if status := e.Call(t, "GET", path, nil, nil, &rest); status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, status)
	}
	seen := map[string]bool{}
	for _, row := range section.Data {
		seen[row.ID] = true
	}
	for _, row := range rest.Data {
		if seen[row.ID] {
			t.Errorf("activity %s is in the 360 section AND the page after its cursor", row.ID)
		}
		seen[row.ID] = true
	}
	if len(seen) != total {
		t.Errorf("section + next page cover %d distinct activities, want all %d", len(seen), total)
	}
}

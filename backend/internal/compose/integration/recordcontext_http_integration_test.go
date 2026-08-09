// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for GET /records/{entity_type}/{id}/context: the
// search.Handlers.GetRecordContext handler and its wire mapping over the
// real handler stack. The mandatory assertion is RLS isolation — an
// anchor the caller cannot see (wrong workspace, or simply unknown)
// yields an empty picture, never another tenant's neighborhood; the
// graph walk's own visibility gate (auth.EnsureVisible, see graph.go)
// answers not-found for that case exactly like every other single-record
// read, so this suite treats 404 and "200 with zero sections" as equally
// acceptable proof of isolation.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

type contextRefWire struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type contextItemWire struct {
	Ref      contextRefWire `json:"ref"`
	Summary  *string        `json:"summary"`
	Evidence []struct {
		Snippet string `json:"snippet"`
		Source  string `json:"source"`
	} `json:"evidence"`
}
type contextSectionWire struct {
	Name  string            `json:"name"`
	Items []contextItemWire `json:"items"`
}
type contextResponseWire struct {
	Anchor   contextRefWire       `json:"anchor"`
	Sections []contextSectionWire `json:"sections"`
}

// seedPersonWithActivity creates a person and logs one activity linked to
// it through the real HTTP write path (the same create-person +
// log-activity-with-links shapes activity_lifecycle_integration_test.go
// and consent_integration_test.go already exercise), returning the
// person's id — the anchor this suite walks context from.
func seedPersonWithActivity(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Context Anchor",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "note", "body": "Discussed renewal terms",
		"links": []apptest.AnyMap{{"entity_type": "person", "entity_id": person.ID}},
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("log anchor activity → %d", status)
	}
	return person.ID
}

// seedMeetingLinkedTo logs a meeting against one person through the real HTTP
// write path, then adds the party the write path cannot express: an attendee
// address capture matched to nobody. Only the connectors write
// activity_participant rows, so a suite that needs one writes it as they do.
func seedMeetingLinkedTo(t *testing.T, e *apptest.AppEnv, personID string) string {
	t.Helper()
	var meeting struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/activities", apptest.AnyMap{
		"kind": "meeting", "subject": "Renewal review",
		"links": []apptest.AnyMap{{"entity_type": "person", "entity_id": personID}},
	}, nil, &meeting); status != http.StatusCreated {
		t.Fatalf("log meeting → %d", status)
	}
	var wsID string
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&wsID); err != nil {
		t.Fatalf("reading the workspace: %v", err)
	}
	if _, err := e.Owner.Exec(t.Context(), `
		INSERT INTO activity_participant (workspace_id, activity_id, role, address)
		VALUES ($1, $2, 'attendee', 'nobody@example.test')`, wsID, meeting.ID); err != nil {
		t.Fatalf("seeding the unmatched attendee: %v", err)
	}
	return meeting.ID
}

// sectionWire returns one named section's items, and nil when the walk emitted
// no such section.
func sectionWire(got contextResponseWire, name string) []contextItemWire {
	for _, section := range got.Sections {
		if section.Name == name {
			return section.Items
		}
	}
	return nil
}

func TestGetRecordContextReturnsAnchorAndIsRowScoped(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	pid := seedPersonWithActivity(t, e)

	var got contextResponseWire
	status := e.Call(t, "GET", "/v1/records/person/"+pid+"/context?max_items=5", nil, nil, &got)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got.Anchor.Type != "person" || got.Anchor.ID != pid {
		t.Fatalf("anchor = %+v, want person/%s", got.Anchor, pid)
	}
	if len(got.Sections) == 0 {
		t.Fatalf("sections = %+v, want at least the profile section", got.Sections)
	}

	// Isolation: a random uuid the caller cannot see yields an empty
	// picture, not an oracle that resurfaces another tenant's neighborhood.
	var empty contextResponseWire
	status = e.Call(t, "GET", "/v1/records/person/018f3a1b-0000-7000-8000-0000deadbeef/context", nil, nil, &empty)
	if status != http.StatusNotFound && (status != http.StatusOK || len(empty.Sections) != 0) {
		t.Fatalf("unknown anchor status = %d, sections = %+v — want 404 or an empty picture", status, empty.Sections)
	}

	t.Run("422 invalid entity_type", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.Call(t, "GET", "/v1/records/bogus/"+pid+"/context", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "entity_type", "invalid_entity_type")
	})

	// An activity is a valid anchor and is DEREFERENCED: the event names the
	// records it is about, and the prep is built around one of them. The wire
	// mapping has to carry the three sections that say which — this is the
	// HTTP half; the precedence and the row-scope refusals have their own
	// suite (activitycontext_integration_test.go).
	t.Run("200 activity anchor preps against the record it names", func(t *testing.T) {
		aid := seedMeetingLinkedTo(t, e, pid)
		var got contextResponseWire
		status := e.Call(t, "GET", "/v1/records/activity/"+aid+"/context", nil, nil, &got)
		if status != http.StatusOK {
			t.Fatalf("activity context status = %d, want 200", status)
		}
		if got.Anchor.Type != "activity" || got.Anchor.ID != aid {
			t.Fatalf("anchor = %+v, want activity/%s", got.Anchor, aid)
		}
		prepared := sectionWire(got, "prepared_for")
		if len(prepared) != 1 || prepared[0].Ref.Type != "person" || prepared[0].Ref.ID != pid {
			t.Fatalf("prepared_for = %+v, want the one person the meeting names", prepared)
		}
		unresolved := sectionWire(got, "unresolved_attendees")
		if len(unresolved) != 1 || unresolved[0].Summary == nil ||
			!strings.Contains(*unresolved[0].Summary, "nobody@example.test") {
			t.Fatalf("unresolved_attendees = %+v, want the address that matched no record", unresolved)
		}
		// The item has no record of its own to point at, so it points at the
		// event the address came from.
		if unresolved[0].Ref.Type != "activity" || unresolved[0].Ref.ID != aid {
			t.Fatalf("an unresolved attendee's ref = %+v, want the event %s", unresolved[0].Ref, aid)
		}
	})

	// The contract bounds max_items to [1, 25]; a value outside that range
	// must reject as a clean 422, never reach the graph walk's slice trim
	// where a negative bound would panic on a negative index.
	t.Run("422 max_items below the contract minimum", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.Call(t, "GET", "/v1/records/person/"+pid+"/context?max_items=-1", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "max_items", "out_of_range")
	})

	t.Run("422 max_items above the contract maximum", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.Call(t, "GET", "/v1/records/person/"+pid+"/context?max_items=999", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "max_items", "out_of_range")
	})

	// A lead is a valid anchor (it is in the path enum) but carries no
	// activity_link neighborhood — the link shape admits only
	// person/organization/deal — so its context is the profile alone: a
	// 200 with an honestly-empty timeline, never the 500 an unsupported
	// anchor would raise.
	t.Run("200 lead anchor yields profile-only context", func(t *testing.T) {
		var lead struct {
			ID string `json:"id"`
		}
		if s := e.Call(t, "POST", "/v1/leads", apptest.AnyMap{"full_name": "Context Lead"}, nil, &lead); s != http.StatusCreated {
			t.Fatalf("create lead → %d", s)
		}
		var got contextResponseWire
		status := e.Call(t, "GET", "/v1/records/lead/"+lead.ID+"/context", nil, nil, &got)
		if status != http.StatusOK {
			t.Fatalf("lead context status = %d, want 200", status)
		}
		if got.Anchor.Type != "lead" || got.Anchor.ID != lead.ID {
			t.Fatalf("anchor = %+v, want lead/%s", got.Anchor, lead.ID)
		}
		for _, section := range got.Sections {
			if section.Name != "profile" {
				t.Fatalf("lead context carried a %q section — a lead has no activity neighborhood", section.Name)
			}
		}
	})
}

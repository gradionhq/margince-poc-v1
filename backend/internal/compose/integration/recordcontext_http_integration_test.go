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
	"testing"
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
func seedPersonWithActivity(t *testing.T, e *env) string {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Context Anchor",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "note", "body": "Discussed renewal terms",
		"links": []anyMap{{"entity_type": "person", "entity_id": person.ID}},
	}, nil, nil); status != http.StatusCreated {
		t.Fatalf("log anchor activity → %d", status)
	}
	return person.ID
}

func TestGetRecordContextReturnsAnchorAndIsRowScoped(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	pid := seedPersonWithActivity(t, e)

	var got contextResponseWire
	status := e.call(t, "GET", "/v1/records/person/"+pid+"/context?max_items=5", nil, nil, &got)
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
	status = e.call(t, "GET", "/v1/records/person/018f3a1b-0000-7000-8000-0000deadbeef/context", nil, nil, &empty)
	if status != http.StatusNotFound && (status != http.StatusOK || len(empty.Sections) != 0) {
		t.Fatalf("unknown anchor status = %d, sections = %+v — want 404 or an empty picture", status, empty.Sections)
	}

	t.Run("422 invalid entity_type", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET", "/v1/records/bogus/"+pid+"/context", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "entity_type", "invalid_entity_type")
	})

	// The contract bounds max_items to [1, 25]; a value outside that range
	// must reject as a clean 422, never reach the graph walk's slice trim
	// where a negative bound would panic on a negative index.
	t.Run("422 max_items below the contract minimum", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET", "/v1/records/person/"+pid+"/context?max_items=-1", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "max_items", "out_of_range")
	})

	t.Run("422 max_items above the contract maximum", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET", "/v1/records/person/"+pid+"/context?max_items=999", nil, nil, &problem)
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
		if s := e.call(t, "POST", "/v1/leads", anyMap{"full_name": "Context Lead"}, nil, &lead); s != http.StatusCreated {
			t.Fatalf("create lead → %d", s)
		}
		var got contextResponseWire
		status := e.call(t, "GET", "/v1/records/lead/"+lead.ID+"/context", nil, nil, &got)
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

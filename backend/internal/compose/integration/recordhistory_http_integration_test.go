// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for GET /records/{entity_type}/{id}/history: the
// handler (privacy.Handlers.GetRecordHistory) and its wire mapping
// (recordHistoryEntryToWire) that recordhistory_integration_test.go never
// drives — that suite calls privacy.ListRecordHistory directly, so the
// entity_type path-param validation, the malformed-cursor 422, and the
// JSON shape only exist at the transport. This suite rides the same
// real-handler-stack e2e harness as e2e_integration_test.go (TLS httptest
// server, session cookie, workspace header) and reuses
// recordhistory_integration_test.go's seedRecordAuditRow/seedWorkspaceUser
// plus fieldhistory_integration_test.go's seedAuditDiffRow to write the
// audit rows the handler reads back.

import (
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// recordHistoryEntryWire mirrors the contract's AuditHistoryEntry field by
// field, decoded loosely so a wire-shape regression (a renamed or
// mistyped key) fails the assertions below instead of silently zeroing.
type recordHistoryEntryWire struct {
	ID                string         `json:"id"`
	ActorType         string         `json:"actor_type"`
	ActorID           string         `json:"actor_id"`
	OnBehalfOf        *string        `json:"on_behalf_of"`
	OnBehalfOfName    *string        `json:"on_behalf_of_name"`
	Action            string         `json:"action"`
	OccurredAt        time.Time      `json:"occurred_at"`
	AuthorizationRule *string        `json:"authorization_rule"`
	Before            map[string]any `json:"before"`
	After             map[string]any `json:"after"`
	Summary           string         `json:"summary"`
}

type recordHistoryListWire struct {
	Data []recordHistoryEntryWire `json:"data"`
	Page struct {
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	} `json:"page"`
}

// recordHistoryHTTPFixture is the seeded shape the happy-path subtest reads
// back: a person created through the real HTTP write path (its own
// create-audit row resolves the admin's display name — genuine genesis
// history, not a fixture), a human-actor phone diff, and — dated newest so
// it lands last in the chronological order — an agent-actor diff acting
// under Ada Authority's delegated authority.
type recordHistoryHTTPFixture struct {
	personID ids.UUID
	adaID    ids.UUID
}

func seedRecordHistoryHTTPFixture(t *testing.T, e *env, dbEnv *Env) recordHistoryHTTPFixture {
	t.Helper()
	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Record History Subject",
		"source":    "ui",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	personID, err := ids.Parse(person["id"].(string))
	if err != nil {
		t.Fatalf("parsing person id %q: %v", person["id"], err)
	}

	// Dated forward from the create row so ordering is unambiguous
	// (fieldhistory_http_integration_test.go's own convention).
	humanAt := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Microsecond)
	agentAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)
	seedAuditDiffRow(t, dbEnv, "person", personID, "human",
		map[string]any{"phone": "555-0100"}, map[string]any{"phone": "555-0199"}, humanAt)

	adaID := seedWorkspaceUser(t, dbEnv, "Ada Authority")
	seedRecordAuditRow(t, dbEnv, "update", personID, "agent", "agent:enrich", &adaID,
		map[string]any{"title": "VP"}, map[string]any{"title": "CTO"}, agentAt)

	return recordHistoryHTTPFixture{personID: personID, adaID: adaID}
}

// assertRecordHistoryHappyPath drives the GET and checks the wire shape:
// chronological ordering, the genesis line's resolved display name, the
// human diff's exact (no-phantom-key) before/after images, and the
// agent line's woven-in delegated authority.
func assertRecordHistoryHappyPath(t *testing.T, e *env, fx recordHistoryHTTPFixture) {
	t.Helper()
	var page recordHistoryListWire
	status := e.call(t, "GET", "/v1/records/person/"+fx.personID.String()+"/history", nil, nil, &page)
	if status != http.StatusOK {
		t.Fatalf("record-history status = %d, want 200: %+v", status, page)
	}
	if len(page.Data) != 3 {
		t.Fatalf("want exactly 3 entries (create genesis + human diff + agent diff): %+v", page.Data)
	}
	for i := 1; i < len(page.Data); i++ {
		if page.Data[i].OccurredAt.Before(page.Data[i-1].OccurredAt) {
			t.Fatalf("entries not chronological ASC at index %d: %+v", i, page.Data)
		}
	}

	genesis := page.Data[0]
	if genesis.Action != "create" {
		t.Fatalf("genesis action = %q, want create", genesis.Action)
	}
	if genesis.Summary != "Ada Admin created the record" {
		t.Errorf("genesis summary = %q, want the resolved admin display name woven in", genesis.Summary)
	}
	if genesis.Before != nil {
		t.Errorf("genesis before = %v, want absent (a create row has no prior image)", genesis.Before)
	}

	human := page.Data[1]
	if human.Action != "update" || human.ActorType != "human" {
		t.Fatalf("human entry = %+v, want the seeded update diff", human)
	}
	// No phantom keys: the seeded map is the whole of before/after, since
	// defaultFieldMasks is empty for person in this repo.
	if len(human.Before) != 1 || human.Before["phone"] != "555-0100" {
		t.Errorf("human before = %v, want exactly {phone: 555-0100}", human.Before)
	}
	if len(human.After) != 1 || human.After["phone"] != "555-0199" {
		t.Errorf("human after = %v, want exactly {phone: 555-0199}", human.After)
	}

	agent := page.Data[2]
	if agent.ActorType != "agent" {
		t.Fatalf("agent entry actor_type = %q, want agent", agent.ActorType)
	}
	if agent.OnBehalfOf == nil || *agent.OnBehalfOf != fx.adaID.String() {
		t.Errorf("agent on_behalf_of = %v, want %s", agent.OnBehalfOf, fx.adaID)
	}
	if agent.OnBehalfOfName == nil || *agent.OnBehalfOfName != "Ada Authority" {
		t.Errorf("agent on_behalf_of_name = %v, want Ada Authority", agent.OnBehalfOfName)
	}
	if agent.Summary != "Agent acting for Ada Authority updated the record" {
		t.Errorf("agent summary = %q, want the delegating authority woven in", agent.Summary)
	}

	// page.has_more is a required (non-pointer) field on the wire — its
	// mere presence in the decoded envelope is what this asserts.
	if page.Page.HasMore {
		t.Errorf("single page must report exhaustion: has_more=%v", page.Page.HasMore)
	}
	if page.Page.NextCursor != nil {
		t.Errorf("single page must carry no next_cursor: %v", *page.Page.NextCursor)
	}
}

func TestRecordHistoryHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	dbEnv := fieldHistoryHTTPEnv(t, e)
	fx := seedRecordHistoryHTTPFixture(t, e, dbEnv)

	t.Run("200 happy path with wire mapping", func(t *testing.T) {
		assertRecordHistoryHappyPath(t, e, fx)
	})

	t.Run("422 invalid entity_type", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET", "/v1/records/bogus/"+ids.NewV7().String()+"/history", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "entity_type", "invalid_entity_type")
	})

	t.Run("422 malformed cursor", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET",
			"/v1/records/person/"+fx.personID.String()+"/history?cursor=!!!notatoken", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "cursor", "malformed_cursor")
	})

	t.Run("keyset page walk over the wire", func(t *testing.T) {
		var walked []string
		var cursor string
		url := "/v1/records/person/" + fx.personID.String() + "/history?limit=1"
		for page := 1; page <= 3; page++ {
			var got recordHistoryListWire
			reqURL := url
			if cursor != "" {
				reqURL += "&cursor=" + cursor
			}
			status := e.call(t, "GET", reqURL, nil, nil, &got)
			if status != http.StatusOK {
				t.Fatalf("page %d status = %d: %+v", page, status, got)
			}
			if len(got.Data) != 1 {
				t.Fatalf("page %d entries = %d, want 1: %+v", page, len(got.Data), got.Data)
			}
			walked = append(walked, got.Data[0].ID)
			if page < 3 {
				if !got.Page.HasMore || got.Page.NextCursor == nil {
					t.Fatalf("page %d must report more rows follow: %+v", page, got.Page)
				}
				cursor = *got.Page.NextCursor
			} else if got.Page.HasMore || got.Page.NextCursor != nil {
				t.Fatalf("page 3 is genuine exhaustion — has_more must not lie: %+v", got.Page)
			}
		}
		if walked[0] == walked[1] || walked[1] == walked[2] || walked[0] == walked[2] {
			t.Fatalf("walked ids not distinct across pages: %v — keyset overlap", walked)
		}
	})
}

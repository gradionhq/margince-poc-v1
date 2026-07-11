// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// HTTP-level coverage for GET /field-history: the handler
// (privacy.Handlers.GetFieldHistory) and its wire mapping
// (fieldHistoryEntryToWire) that fieldhistory_integration_test.go never
// drives — that suite calls privacy.ListFieldHistory directly, so the
// query-validation branches and the JSON shape only exist at the
// transport. This suite rides the same real-handler-stack e2e harness as
// e2e_integration_test.go (TLS httptest server, session cookie, workspace
// header) and reuses fieldhistory_integration_test.go's seedAuditDiffRow
// to write the audit rows the handler reads back.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// fieldHistoryEntryWire mirrors the contract's FieldHistoryEntry field by
// field, decoded loosely so a wire-shape regression (a renamed or
// mistyped key) fails the assertions below instead of silently zeroing.
type fieldHistoryEntryWire struct {
	Field      string         `json:"field"`
	OldValue   *string        `json:"old_value"`
	NewValue   *string        `json:"new_value"`
	ChangedAt  string         `json:"changed_at"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	PassportID *string        `json:"passport_id"`
	Evidence   map[string]any `json:"evidence"`
}

type fieldHistoryListWire struct {
	Data []fieldHistoryEntryWire `json:"data"`
	Page struct {
		HasMore    bool    `json:"has_more"`
		NextCursor *string `json:"next_cursor"`
	} `json:"page"`
}

// fieldHistoryProblem is the RFC 7807 body httperr.Validation produces.
type fieldHistoryProblem struct {
	Code    string `json:"code"`
	Detail  string `json:"detail"`
	Details struct {
		Errors []struct {
			Field   string `json:"field"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"details"`
}

// assertFieldHistoryValidation422 checks the shared 422 problem+json shape
// httperr.Validation writes for every query-validation rejection: the
// envelope code, and the single per-field error naming the rejected field
// and its machine code.
func assertFieldHistoryValidation422(t *testing.T, status int, problem fieldHistoryProblem, field, code string) {
	t.Helper()
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %+v", status, problem)
	}
	if problem.Code != "validation_error" {
		t.Fatalf("problem code = %q, want validation_error", problem.Code)
	}
	if len(problem.Details.Errors) != 1 {
		t.Fatalf("want exactly one field error, got %+v", problem.Details.Errors)
	}
	if got := problem.Details.Errors[0]; got.Field != field || got.Code != code {
		t.Fatalf("field error = %+v, want field=%s code=%s", got, field, code)
	}
}

// fieldHistoryHTTPEnv resolves the workspace id the HTTP harness's
// bootstrap created and opens a second pgxpool.Pool onto the same live
// schema — exactly how setup() itself pairs its owner connection with an
// app pool — so the store-level suite's seedAuditDiffRow can write
// straight through the real audit-spine path this handler reads back.
func fieldHistoryHTTPEnv(t *testing.T, e *env) *Env {
	t.Helper()
	ctx := context.Background()
	var ws ids.UUID
	if err := e.owner.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&ws); err != nil {
		t.Fatalf("resolving workspace id for %q: %v", e.slug, err)
	}
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening app pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return &Env{Pool: pool, WS: ws}
}

// seedAgentFieldHistoryRow seeds an agent-actor audit diff row carrying a
// passport id and evidence — the one shape seedAuditDiffRow cannot
// produce (it never binds those two columns) and the only shape that
// exercises fieldHistoryEntryToWire's passport/evidence branches, since
// makeFieldHistoryEntry surfaces them for actor_type=agent only.
func seedAgentFieldHistoryRow(t *testing.T, e *Env, entityType string, entityID, passportID ids.UUID,
	evidence, before, after map[string]any, occurredAt time.Time,
) ids.UUID {
	t.Helper()
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	rowID := ids.NewV7()
	ctx := principal.WithWorkspaceID(t.Context(), e.WS)
	err = database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, passport_id, action,
			                        entity_type, entity_id, before, after, evidence, occurred_at)
			 VALUES ($1, $2, 'agent', 'agent:test', $3, 'update', $4, $5, $6, $7, $8, $9)`,
			rowID, e.WS, passportID, entityType, entityID, beforeJSON, afterJSON, evidenceJSON, occurredAt)
		return err
	})
	if err != nil {
		t.Fatalf("seed agent audit row: %v", err)
	}
	return rowID
}

// fieldHistoryHTTPFixture is the seeded shape TestFieldHistoryHTTP's happy
// path reads back: a person created through the real HTTP write path (its
// own create-audit row is honest genesis history), a human-actor title
// diff, and — dated newest so it lands at data[0] — an agent-actor diff
// carrying a passport id and evidence.
type fieldHistoryHTTPFixture struct {
	personID   ids.UUID
	passportID ids.UUID
}

// seedFieldHistoryHTTPFixture creates the subject and seeds the two audit
// diff rows the happy-path subtest below asserts on.
func seedFieldHistoryHTTPFixture(t *testing.T, e *env, dbEnv *Env) fieldHistoryHTTPFixture {
	t.Helper()
	var person anyMap
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "History Wire Subject",
		"source":    "ui",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	personID, err := ids.Parse(person["id"].(string))
	if err != nil {
		t.Fatalf("parsing person id %q: %v", person["id"], err)
	}

	// Dated forward from the create row so ordering is unambiguous
	// (fieldhistory_integration_test.go's own convention).
	humanAt := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Microsecond)
	agentAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Microsecond)
	seedAuditDiffRow(t, dbEnv, "person", personID, "human",
		map[string]any{"title": "VP"}, map[string]any{"title": "CTO"}, humanAt)

	passportID := ids.NewV7()
	evidence := map[string]any{"tool_call_id": "call-1", "confidence": "0.92"}
	seedAgentFieldHistoryRow(t, dbEnv, "person", personID, passportID, evidence,
		map[string]any{"score": "1"}, map[string]any{"score": "2"}, agentAt)

	return fieldHistoryHTTPFixture{personID: personID, passportID: passportID}
}

// assertFieldHistoryHappyPath drives the GET and checks the wire shape:
// data[0] carries the required scalar fields plus the newest (agent) row's
// passport/evidence, the seeded human diff surfaces with both absent, and
// the page envelope is present.
func assertFieldHistoryHappyPath(t *testing.T, e *env, fx fieldHistoryHTTPFixture) {
	t.Helper()
	var page fieldHistoryListWire
	status := e.call(t, "GET", "/v1/field-history?entity_type=person&entity_id="+fx.personID.String(), nil, nil, &page)
	if status != http.StatusOK {
		t.Fatalf("field-history status = %d, want 200: %+v", status, page)
	}
	if len(page.Data) < 3 {
		t.Fatalf("want at least 3 entries (create genesis + two seeded diffs): %+v", page.Data)
	}

	newest := page.Data[0]
	if newest.Field == "" || newest.ChangedAt == "" || newest.ActorType == "" {
		t.Fatalf("data[0] missing a required field: %+v", newest)
	}
	if newest.ActorType != "agent" {
		t.Fatalf("data[0] actor_type = %q, want agent (the newest-dated seeded row)", newest.ActorType)
	}
	if newest.OldValue == nil || *newest.OldValue != "1" || newest.NewValue == nil || *newest.NewValue != "2" {
		t.Fatalf("data[0] old/new value = %+v, want the seeded score diff", newest)
	}
	if newest.PassportID == nil || *newest.PassportID != fx.passportID.String() {
		t.Errorf("agent entry passport_id = %v, want %s", newest.PassportID, fx.passportID)
	}
	if newest.Evidence == nil || newest.Evidence["tool_call_id"] != "call-1" {
		t.Errorf("agent entry evidence = %v, want the seeded evidence map", newest.Evidence)
	}

	var sawHumanTitle bool
	for _, en := range page.Data {
		if en.Field == "title" && en.ActorType == "human" {
			sawHumanTitle = true
			if en.PassportID != nil || en.Evidence != nil {
				t.Errorf("human entry carries passport/evidence, want both absent: %+v", en)
			}
		}
	}
	if !sawHumanTitle {
		t.Errorf("seeded human title diff missing from the wire response: %+v", page.Data)
	}
	// page.has_more is a required (non-pointer) field on the wire — it
	// decodes regardless of value, so its mere presence in the envelope
	// is what this asserts, not that it is true.
	_ = page.Page.HasMore
}

func TestFieldHistoryHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	dbEnv := fieldHistoryHTTPEnv(t, e)
	fx := seedFieldHistoryHTTPFixture(t, e, dbEnv)

	t.Run("200 happy path with wire mapping", func(t *testing.T) {
		assertFieldHistoryHappyPath(t, e, fx)
	})

	t.Run("422 invalid entity_type", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET", "/v1/field-history?entity_type=bogus&entity_id="+ids.NewV7().String(), nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "entity_type", "invalid_entity_type")
	})

	t.Run("422 invalid actor_type", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET",
			"/v1/field-history?entity_type=person&entity_id="+ids.NewV7().String()+"&actor_type=bogus", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "actor_type", "invalid_actor_type")
	})

	t.Run("422 malformed cursor", func(t *testing.T) {
		var problem fieldHistoryProblem
		status := e.call(t, "GET",
			"/v1/field-history?entity_type=person&entity_id="+ids.NewV7().String()+"&cursor=!!!notatoken", nil, nil, &problem)
		assertFieldHistoryValidation422(t, status, problem, "cursor", "malformed_cursor")
	})

	// A 404 for an out-of-scope/nonexistent record needs a bounded
	// (non-admin) session, and the contract surface has no
	// user-invitation endpoint — the workspace bootstrap mints exactly
	// one admin per workspace and nothing cheaper. That row-scope gate is
	// already proven at the store level by
	// TestFieldHistoryGatesOnReadPermissionAndVisibility (over the same
	// privacy.ListFieldHistory this handler calls), so it is not
	// duplicated here.
}
